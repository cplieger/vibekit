package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// idemHandler returns a handler that counts invocations and writes the
// given status, Content-Type, and body. The returned counter reports how
// many times the handler actually ran — the core signal every dedup
// assertion turns on (cached → counter stays put; passthrough → it climbs).
func idemHandler(status int, ct, body string) (http.Handler, *atomic.Int32) {
	var calls atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
	return h, &calls
}

func idemReq(method, path, key string) *http.Request {
	req := httptest.NewRequest(method, "http://example.com"+path, http.NoBody)
	if key != "" {
		req.Header.Set(idempotencyHeader, key)
	}
	return req
}

func serveIdem(mw http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	return rec
}

// (1) + (2): a fresh key runs the handler once and caches the outcome;
// repeating the same method+path+key replays the cached status, body,
// and Content-Type without re-invoking the handler.
func TestIdempotency_replaysCachedResponse(t *testing.T) {
	c := newIdempotencyCache(idempotencyTTL)
	defer c.stop()
	h, calls := idemHandler(http.StatusCreated, "application/json", `{"ok":true}`)
	mw := c.middleware(h)

	rec1 := serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "k1"))
	if calls.Load() != 1 {
		t.Fatalf("first request: handler calls = %d, want 1", calls.Load())
	}
	if rec1.Code != http.StatusCreated || rec1.Body.String() != `{"ok":true}` {
		t.Fatalf("first response: %d %q", rec1.Code, rec1.Body.String())
	}

	rec2 := serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "k1"))
	if calls.Load() != 1 {
		t.Fatalf("replay: handler calls = %d, want 1 (should not re-run)", calls.Load())
	}
	if rec2.Code != rec1.Code {
		t.Errorf("replay status = %d, want %d", rec2.Code, rec1.Code)
	}
	if rec2.Body.String() != rec1.Body.String() {
		t.Errorf("replay body = %q, want %q", rec2.Body.String(), rec1.Body.String())
	}
	if got, want := rec2.Header().Get("Content-Type"), rec1.Header().Get("Content-Type"); got != want {
		t.Errorf("replay Content-Type = %q, want %q", got, want)
	}
}

// (3): a different key is a fresh request — the handler runs again.
func TestIdempotency_differentKeyReexecutes(t *testing.T) {
	c := newIdempotencyCache(idempotencyTTL)
	defer c.stop()
	h, calls := idemHandler(http.StatusOK, "application/json", "x")
	mw := c.middleware(h)

	serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "k1"))
	serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "k2"))
	if calls.Load() != 2 {
		t.Fatalf("handler calls = %d, want 2 (distinct keys)", calls.Load())
	}
}

// (4): the same key on a DIFFERENT path is not a hit — the composite
// cache key (method+path+key) keeps routes from colliding.
func TestIdempotency_sameKeyDifferentPathReexecutes(t *testing.T) {
	c := newIdempotencyCache(idempotencyTTL)
	defer c.stop()
	h, calls := idemHandler(http.StatusOK, "application/json", "x")
	mw := c.middleware(h)

	serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "same"))
	serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash-pop", "same"))
	if calls.Load() != 2 {
		t.Fatalf("handler calls = %d, want 2 (different path, same key)", calls.Load())
	}
}

// (5)+(6)+(7): requests that must never be deduped pass straight
// through — the handler runs on every call. Covers GET/HEAD/OPTIONS
// (even with a key), a missing header, a control-char key, and an
// over-length key.
func TestIdempotency_passthrough(t *testing.T) {
	longKey := strings.Repeat("a", maxIdempotencyKeyBytes+1)
	cases := []struct {
		name   string
		method string
		key    string
	}{
		{"GET with key", http.MethodGet, "g1"},
		{"HEAD with key", http.MethodHead, "h1"},
		{"OPTIONS with key", http.MethodOptions, "o1"},
		{"POST missing key", http.MethodPost, ""},
		{"POST control-char key", http.MethodPost, "bad\nkey"},
		{"POST oversized key", http.MethodPost, longKey},
		{"PUT oversized key", http.MethodPut, longKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newIdempotencyCache(idempotencyTTL)
			defer c.stop()
			h, calls := idemHandler(http.StatusOK, "application/json", "ok")
			mw := c.middleware(h)

			serveIdem(mw, idemReq(tc.method, "/api/git/stash", tc.key))
			serveIdem(mw, idemReq(tc.method, "/api/git/stash", tc.key))
			if calls.Load() != 2 {
				t.Fatalf("handler calls = %d, want 2 (no dedup)", calls.Load())
			}
		})
	}
}

// (8): a 5xx is transient and must not be cached, so a retry can
// re-execute against a possibly-recovered backend.
func TestIdempotency_serverErrorNotCached(t *testing.T) {
	c := newIdempotencyCache(idempotencyTTL)
	defer c.stop()
	h, calls := idemHandler(http.StatusServiceUnavailable, "application/json", `{"error":"x"}`)
	mw := c.middleware(h)

	rec1 := serveIdem(mw, idemReq(http.MethodPost, "/api/git/push", "boom"))
	rec2 := serveIdem(mw, idemReq(http.MethodPost, "/api/git/push", "boom"))
	if calls.Load() != 2 {
		t.Fatalf("handler calls = %d, want 2 (5xx must re-execute)", calls.Load())
	}
	if rec1.Code != http.StatusServiceUnavailable || rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("statuses = %d, %d, want 503, 503", rec1.Code, rec2.Code)
	}
}

// (9): a 4xx is a deterministic outcome — cache and replay it.
func TestIdempotency_clientErrorCachedAndReplayed(t *testing.T) {
	c := newIdempotencyCache(idempotencyTTL)
	defer c.stop()
	h, calls := idemHandler(http.StatusConflict, "application/json", `{"error":"dup"}`)
	mw := c.middleware(h)

	rec1 := serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "c4"))
	rec2 := serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "c4"))
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1 (4xx should replay)", calls.Load())
	}
	if rec1.Code != http.StatusConflict || rec2.Code != http.StatusConflict {
		t.Fatalf("statuses = %d, %d, want 409, 409", rec1.Code, rec2.Code)
	}
	if rec2.Body.String() != `{"error":"dup"}` {
		t.Errorf("replay body = %q", rec2.Body.String())
	}
}

// (10): once the TTL elapses, the cached entry is lazily evicted on the
// next access and the handler re-executes.
func TestIdempotency_ttlExpiryReexecutes(t *testing.T) {
	c := newIdempotencyCache(15 * time.Millisecond)
	defer c.stop()
	h, calls := idemHandler(http.StatusOK, "application/json", "x")
	mw := c.middleware(h)

	serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "ttl"))
	serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "ttl")) // replay, within TTL
	if calls.Load() != 1 {
		t.Fatalf("within TTL: handler calls = %d, want 1", calls.Load())
	}

	time.Sleep(40 * time.Millisecond) // exceed TTL

	serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "ttl"))
	if calls.Load() != 2 {
		t.Fatalf("after TTL: handler calls = %d, want 2 (expired → re-execute)", calls.Load())
	}
}

// (11): two truly-concurrent requests under the same key — the first
// claims the in-flight marker and runs; the second gets 409. Exactly
// one handler invocation. (Chosen contract: 409 for concurrent
// duplicates; genuine idempotent retries are sequential.)
func TestIdempotency_concurrentDuplicateGets409(t *testing.T) {
	c := newIdempotencyCache(idempotencyTTL)
	defer c.stop()

	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	mw := c.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		once.Do(func() { close(entered) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))

	rec1 := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		mw.ServeHTTP(rec1, idemReq(http.MethodPost, "/api/git/stash", "cc"))
		close(done)
	}()

	<-entered // first request is now inside the handler, holding in-flight
	rec2 := serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "cc"))
	close(release)
	<-done

	if rec2.Code != http.StatusConflict {
		t.Fatalf("concurrent duplicate: status = %d, want 409", rec2.Code)
	}
	if rec1.Code != http.StatusOK || rec1.Body.String() != `{"ok":true}` {
		t.Fatalf("first request: %d %q", rec1.Code, rec1.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", calls.Load())
	}
}

// (12): a response larger than the body cap is written through to the
// client in full but not cached, so a single fat response can't pin
// memory — and the next request with the same key re-executes.
func TestIdempotency_oversizeBodyNotCached(t *testing.T) {
	c := newIdempotencyCache(idempotencyTTL)
	c.maxBody = 16 // tiny cap so the test stays cheap
	defer c.stop()
	big := strings.Repeat("x", 100)
	h, calls := idemHandler(http.StatusOK, "application/json", big)
	mw := c.middleware(h)

	rec1 := serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "fat"))
	rec2 := serveIdem(mw, idemReq(http.MethodPost, "/api/git/stash", "fat"))
	if calls.Load() != 2 {
		t.Fatalf("handler calls = %d, want 2 (oversize must not cache)", calls.Load())
	}
	if rec1.Body.String() != big || rec2.Body.String() != big {
		t.Fatalf("client did not receive full body through the cap")
	}
}

// sweep drops expired COMPLETED entries while leaving fresh ones and
// in-flight markers untouched (in-flight is cleared by its owning
// request, never age-swept).
func TestIdempotency_sweepDropsExpiredKeepsInflightAndFresh(t *testing.T) {
	c := newIdempotencyCache(time.Hour) // long TTL; sweep is driven directly
	defer c.stop()
	now := time.Now()
	c.mu.Lock()
	c.entries["old"] = &idempotencyEntry{ts: now.Add(-2 * time.Hour), status: http.StatusOK}
	c.entries["fresh"] = &idempotencyEntry{ts: now, status: http.StatusOK}
	c.entries["busy"] = &idempotencyEntry{ts: now.Add(-2 * time.Hour), inflight: true}
	c.mu.Unlock()

	c.sweep(now)

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries["old"]; ok {
		t.Error("expired completed entry was not swept")
	}
	if _, ok := c.entries["fresh"]; !ok {
		t.Error("fresh completed entry was wrongly swept")
	}
	if _, ok := c.entries["busy"]; !ok {
		t.Error("in-flight marker was wrongly age-swept")
	}
}

// validIdempotencyKey gates which keys participate in dedup. Behavioral
// table: opaque composite keys (slashes, colons, arrows, spaces, UTF-8)
// must pass; empty, control-char, and over-length keys must not.
func TestValidIdempotencyKey(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want bool
	}{
		{"uuid-like", "018f-2a1c-7e", true},
		{"composite file rename", "files.rename:dir/old.txt->dir/new.txt", true},
		{"composite with spaces", "plan.run:chat-1:Do the thing now", true},
		{"utf8 filename", "files.create:dir/café.txt", true},
		{"empty", "", false},
		{"newline", "abc\ndef", false},
		{"carriage return", "abc\rdef", false},
		{"nul", "abc\x00def", false},
		{"tab", "abc\tdef", false},
		{"del", "abc\x7fdef", false},
		{"too long", strings.Repeat("a", maxIdempotencyKeyBytes+1), false},
		{"at limit", strings.Repeat("a", maxIdempotencyKeyBytes), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validIdempotencyKey(tc.key); got != tc.want {
				t.Errorf("validIdempotencyKey(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

// idempotentMethod gates which methods participate. Mutations dedup;
// safe methods and anything else pass through.
func TestIdempotentMethod(t *testing.T) {
	cases := map[string]bool{
		http.MethodPost:    true,
		http.MethodPut:     true,
		http.MethodPatch:   true,
		http.MethodDelete:  true,
		http.MethodGet:     false,
		http.MethodHead:    false,
		http.MethodOptions: false,
		http.MethodConnect: false,
		http.MethodTrace:   false,
	}
	for method, want := range cases {
		if got := idempotentMethod(method); got != want {
			t.Errorf("idempotentMethod(%q) = %v, want %v", method, got, want)
		}
	}
}
