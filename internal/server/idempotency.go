// Package server — REST Idempotency-Key dedup middleware.
//
// Wraps the mux so a retried mutation (POST/PUT/PATCH/DELETE) carrying
// a stable `Idempotency-Key` header replays the first response instead
// of re-executing the handler. The @cplieger/actions client sends the
// same key across an action's network retries, so a request that timed
// out client-side (but actually succeeded server-side) replays the
// cached outcome on retry rather than duplicating the mutation.
//
// This is the HTTP/REST sibling of the POST /api/command request_id
// dedup in internal/command (which caches a JSON body keyed by
// request_id via internal/dedup.Cache). The REST path needs a richer
// entry — status code + Content-Type + body, plus an in-flight marker
// — so it carries its own focused cache rather than reusing
// dedup.Cache (which stores only []byte and has no in-flight concept).
// The TTL, mutex-guarded map, lazy-eviction-on-access, and periodic
// janitor goroutine all mirror dedup.Cache.
package server

import (
	"bytes"
	"net/http"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/webhttp"
)

// idempotencyHeader is the request header carrying the client's
// idempotency key, set by @cplieger/actions apiAction.
const idempotencyHeader = "Idempotency-Key"

// idempotencyTTL mirrors dedup.DefaultTTL (the command-path request_id
// cache): 5 minutes is long enough to cover a client's network-retry
// window after a transient failure, short enough that a stale replay
// can't outlive the user's intent.
const idempotencyTTL = 5 * time.Minute

// idempotencyMaxEntries bounds the number of cached entries as a hard
// memory ceiling between janitor sweeps. Mirrors dedup.DefaultMaxEntries.
const idempotencyMaxEntries = 10_000

// idempotencyMaxBody caps the response body buffered for replay. A
// response larger than this is written through to the client normally
// but NOT cached, so a single fat response can't pin a megabyte per
// cached key. 1 MiB matches the repo's MaxHeaderBytes / MaxJSONBody
// sizing norm.
const idempotencyMaxBody = 1 << 20 // 1 MiB

// maxIdempotencyKeyBytes caps the Idempotency-Key header. Client keys
// are opaque: some are framework-generated, others are composite
// strings built from args (e.g. "files.rename:dir/old->dir/new"), so the
// charset legitimately includes '/', ':', '->', and spaces. 256 bytes
// comfortably fits the
// composite filename keys while bounding map-key memory. Mirrors the
// bounded style of ids.ValidRequestID with a wider bound and an opaque
// charset.
const maxIdempotencyKeyBytes = 256

// idempotentMethod reports whether the method participates in dedup.
// GET/HEAD/OPTIONS (and anything else) pass straight through: they are
// either safe/idempotent already or carry no mutation to replay.
func idempotentMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// validIdempotencyKey reports whether key is safe to use as a dedup
// cache key. Empty → false (no dedup). Rejects control characters
// (newline/CR/NUL/tab and DEL) to prevent header-injection / log-forging
// and anything over the byte cap. Everything else printable is accepted
// because the client key is opaque (framework-generated or arg-composite
// with '/', ':', spaces); rejecting a valid key would silently disable
// dedup, so the check is deliberately permissive about charset and strict
// only about control chars + length. Iterating bytes (not runes) is
// correct for the control-char gate: UTF-8 continuation/lead bytes are
// all >= 0x80, never in the 0x00–0x1f / 0x7f range, so unicode filenames
// pass unharmed.
func validIdempotencyKey(key string) bool {
	if key == "" || len(key) > maxIdempotencyKeyBytes {
		return false
	}
	for i := range len(key) {
		if key[i] < 0x20 || key[i] == 0x7f {
			return false
		}
	}
	return true
}

// idempotencyCompositeKey scopes the cache key to method+path+key so
// the same client key reused on different routes can't collide. NUL
// separators keep the three parts unambiguous regardless of the
// opaque key's own charset.
func idempotencyCompositeKey(method, path, key string) string {
	return method + "\x00" + path + "\x00" + key
}

// idempotencyEntry is a single cache slot. An in-flight slot (inflight
// = true) marks a request currently executing under the key; a
// completed slot carries the captured response for replay until ts+ttl.
type idempotencyEntry struct {
	ts       time.Time
	ct       string
	body     []byte
	status   int
	inflight bool
}

// idempotencyCache deduplicates REST mutations by composite key. It
// owns its own mutex so check/record never contends with other
// subsystems, and never holds the lock across the wrapped handler.
type idempotencyCache struct {
	entries    map[string]*idempotencyEntry
	done       chan struct{}
	ttl        time.Duration
	maxEntries int
	maxBody    int
	mu         sync.Mutex
	stopOnce   sync.Once
}

// newIdempotencyCache constructs a cache with the given TTL and starts
// its janitor goroutine. Body and entry caps use the package defaults;
// tests override the unexported fields directly (same package). Call
// stop() to halt the janitor.
func newIdempotencyCache(ttl time.Duration) *idempotencyCache {
	c := &idempotencyCache{
		entries:    make(map[string]*idempotencyEntry),
		done:       make(chan struct{}),
		ttl:        ttl,
		maxEntries: idempotencyMaxEntries,
		maxBody:    idempotencyMaxBody,
	}
	go c.janitor()
	return c
}

// janitor runs a periodic sweep of expired COMPLETED entries. Mirrors
// dedup.Cache.StartCleaner: a 1-minute ticker, stoppable via done. The
// TTL governs expiry; the tick is just how often the backstop runs
// (lazy eviction in begin handles the on-access case).
func (c *idempotencyCache) janitor() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.sweep(time.Now())
		}
	}
}

// stop halts the janitor goroutine. Idempotent.
func (c *idempotencyCache) stop() {
	c.stopOnce.Do(func() { close(c.done) })
}

// sweep removes expired completed entries. In-flight markers are never
// swept by age: an in-flight slot is cleared by the owning request when
// it returns (begin's defer-driven abort/complete), so age-sweeping one
// would open a double-execution window for a long-running handler.
func (c *idempotencyCache) sweep(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if !e.inflight && now.Sub(e.ts) >= c.ttl {
			delete(c.entries, k)
		}
	}
}

// begin atomically transitions the slot for key. It returns either a
// completed entry to replay, an in-flight signal, or claims the key as
// in-flight for the caller to run. Exactly one of the three outcomes:
//   - (entry, false): a fresh completed entry exists → replay it.
//   - (nil, true):    another request holds the key → 409.
//   - (nil, false):   key claimed in-flight; caller owns it and must
//     resolve via complete() or abort().
func (c *idempotencyCache) begin(key string) (*idempotencyEntry, bool) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		if e.inflight {
			return nil, true
		}
		if now.Sub(e.ts) < c.ttl {
			return e, false
		}
		// Completed but expired: lazy-evict and fall through to claim.
		delete(c.entries, key)
	}
	if len(c.entries) >= c.maxEntries {
		c.evictOldestCompletedLocked()
	}
	c.entries[key] = &idempotencyEntry{ts: now, inflight: true}
	return nil, false
}

// complete replaces the in-flight marker with a cached response. Only
// called for status < 500 within the body cap. The body is copied so a
// later reuse of the handler's buffer can't alias the cached bytes.
func (c *idempotencyCache) complete(key string, status int, ct string, body []byte) {
	cp := make([]byte, len(body))
	copy(cp, body)
	c.mu.Lock()
	c.entries[key] = &idempotencyEntry{ts: time.Now(), status: status, ct: ct, body: cp}
	c.mu.Unlock()
}

// abort clears an in-flight marker without storing a completed entry,
// so a retry can re-execute. Used for 5xx and over-cap responses, and
// as the defer-driven safety net if the handler panics. Never clobbers
// a completed entry (in case complete already ran).
func (c *idempotencyCache) abort(key string) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && e.inflight {
		delete(c.entries, key)
	}
	c.mu.Unlock()
}

// evictOldestCompletedLocked drops the oldest completed entry to keep
// the map under maxEntries. Callers hold c.mu. In-flight markers are
// never evicted (evicting one would let a concurrent retry re-execute);
// if every entry is in-flight, the map is allowed to exceed the cap
// briefly until those requests complete and the janitor drains them.
func (c *idempotencyCache) evictOldestCompletedLocked() {
	var oldestKey string
	var oldestTS time.Time
	for k, e := range c.entries {
		if e.inflight {
			continue
		}
		if oldestKey == "" || e.ts.Before(oldestTS) {
			oldestKey, oldestTS = k, e.ts
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

// middleware wraps next with idempotent-replay behavior. Non-deduped
// requests (wrong method, missing/invalid key) pass straight through to
// next with the real ResponseWriter — no buffering, so streaming stays
// correct. Deduped requests run through a capturing writer that writes
// through to the client AND buffers (up to maxBody) for caching.
func (c *idempotencyCache) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !idempotentMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		key := r.Header.Get(idempotencyHeader)
		if !validIdempotencyKey(key) {
			// Empty or malformed key: skip dedup rather than 400 — an
			// unparseable key shouldn't break an otherwise valid request.
			next.ServeHTTP(w, r)
			return
		}

		ck := idempotencyCompositeKey(r.Method, r.URL.Path, key)
		replay, inflight := c.begin(ck)
		if replay != nil {
			writeIdempotentReplay(w, replay)
			return
		}
		if inflight {
			// A concurrent request already holds this key. Genuine
			// idempotent retries are sequential (a retry fires after the
			// first attempt's network failure), so true-concurrent
			// duplicates are not the retry pattern we replay; 409 is the
			// safe, simple answer.
			httpreply.Conflict(w, "request already in progress")
			return
		}

		// We own the in-flight marker. Guarantee it is resolved even if
		// the handler panics (the defer runs during unwind).
		cw := &idempotencyWriter{rec: webhttp.NewStatusRecorder(w), limit: c.maxBody}
		settled := false
		defer func() {
			if !settled {
				c.abort(ck)
			}
		}()
		next.ServeHTTP(cw, r)
		settled = true

		// Cache only deterministic outcomes (<500) that fit the body
		// cap. 5xx is transient → leave the key clear so a retry can
		// re-execute; an over-cap body was already written through, just
		// don't pin it in memory.
		if cw.rec.Status() < 500 && !cw.overflow {
			c.complete(ck, cw.rec.Status(), cw.Header().Get("Content-Type"), cw.buf.Bytes())
		} else {
			c.abort(ck)
		}
	})
}

// writeIdempotentReplay writes a cached response. securityMiddleware
// wraps the idempotency layer from outside, so it already set the
// baseline security headers (X-Content-Type-Options / Referrer-Policy /
// X-Frame-Options) on the response before this runs; only the
// per-response Content-Type needs restoring here.
func writeIdempotentReplay(w http.ResponseWriter, e *idempotencyEntry) {
	if e.ct != "" {
		w.Header().Set("Content-Type", e.ct)
	}
	w.WriteHeader(e.status)
	_, _ = w.Write(e.body)
}

// idempotencyWriter writes through to the client while buffering the
// response (status + body) for caching. Status capture and the
// first-WriteHeader-wins guard are delegated to a shared
// webhttp.StatusRecorder rather than hand-rolled; this writer layers the
// capped body buffer on top. It implements only http.ResponseWriter
// (Header/WriteHeader/Write) and deliberately NOT Flusher/Hijacker/
// ReaderFrom, so every response byte flows through Write and into the
// capture buffer — the recorder's zero-copy ReadFrom would otherwise
// stream straight to the client and bypass the buffer. Once the buffer
// would exceed limit it sets overflow and stops buffering (the client
// still receives the full stream); an overflowed response is not cached.
type idempotencyWriter struct {
	rec      *webhttp.StatusRecorder
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (cw *idempotencyWriter) Header() http.Header { return cw.rec.Header() }

func (cw *idempotencyWriter) WriteHeader(code int) { cw.rec.WriteHeader(code) }

func (cw *idempotencyWriter) Write(p []byte) (int, error) {
	// Write through to the client first (streaming correctness) via the
	// recorder, then buffer what was actually written, up to the cap.
	n, err := cw.rec.Write(p)
	if !cw.overflow {
		if cw.buf.Len()+n > cw.limit {
			cw.overflow = true
			cw.buf.Reset() // won't cache → free the partial buffer
		} else {
			cw.buf.Write(p[:n])
		}
	}
	return n, err
}
