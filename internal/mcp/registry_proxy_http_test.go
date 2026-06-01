package mcp

// HTTP + cache + eviction coverage for RegistryProxy. The production
// proxy hits registry.modelcontextprotocol.io; these tests swap the
// client Transport for a redirect Transport that retargets every
// request at a local httptest server. No real network.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// redirectTransport rewrites any request to a fake upstream URL. The
// production code only issues one GET pattern (to registryBaseURL +
// "/servers?..."), so we just splice the path+query onto the fake
// server's base URL.
type redirectTransport struct {
	base string
}

func (r *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u := r.base + req.URL.Path
	if req.URL.RawQuery != "" {
		u += "?" + req.URL.RawQuery
	}
	out, err := http.NewRequestWithContext(req.Context(), req.Method, u, req.Body)
	if err != nil {
		return nil, err
	}
	out.Header = req.Header
	return http.DefaultTransport.RoundTrip(out)
}

func newProxyAgainst(t *testing.T, upstream *httptest.Server) (*RegistryProxy, *http.ServeMux) {
	t.Helper()
	p := NewRegistryProxy()
	// Keep the production CheckRedirect to cover the allowlist test.
	p.client = &http.Client{
		Timeout:       2 * time.Second,
		Transport:     &redirectTransport{base: upstream.URL},
		CheckRedirect: p.client.CheckRedirect,
	}
	mux := http.NewServeMux()
	p.RegisterRoutes(mux)
	return p, mux
}

// --- handleSearch branches ---

func TestRegistryProxy_Search_success(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if got := r.URL.Query().Get("search"); got != "github" {
			t.Errorf("upstream search=%q, want github", got)
		}
		if got := r.URL.Query().Get("limit"); got != "5" {
			t.Errorf("upstream limit=%q, want 5", got)
		}
		_, _ = io.WriteString(w, `{"servers":[{"server":{"name":"x","packages":[
			{"registryType":"npm","identifier":"@x/y","transport":{"type":"stdio"}}
		]}}]}`)
	}))
	defer upstream.Close()
	_, mux := newProxyAgainst(t, upstream)

	req := httptest.NewRequest(http.MethodGet, "/api/mcp/registry/search?q=github&limit=5", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Servers []RegistryEntry `json:"servers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Servers) != 1 || body.Servers[0].Name != "x" {
		t.Errorf("normalised body = %+v, want 1 entry named x", body.Servers)
	}
}

func TestRegistryProxy_Search_rejections(t *testing.T) {
	long := strings.Repeat("a", maxSearchQueryLen+1)

	cases := []struct {
		name           string
		method         string
		path           string
		wantBody       string
		wantStatus     int
		upstreamStatus int
	}{
		{
			name:       "POST_method_not_allowed",
			method:     http.MethodPost,
			path:       "/api/mcp/registry/search?q=x",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "query_too_long",
			method:     http.MethodGet,
			path:       "/api/mcp/registry/search?q=" + long,
			wantStatus: http.StatusBadRequest,
			wantBody:   "query too long",
		},
		{
			name:       "query_control_chars",
			method:     http.MethodGet,
			path:       "/api/mcp/registry/search?q=a%0Ab",
			wantStatus: http.StatusBadRequest,
			wantBody:   "control characters",
		},
		{
			name:       "bad_limit",
			method:     http.MethodGet,
			path:       "/api/mcp/registry/search?q=a&limit=abc",
			wantStatus: http.StatusBadRequest,
			wantBody:   "invalid limit",
		},
		{
			name:           "upstream_500_is_502",
			method:         http.MethodGet,
			path:           "/api/mcp/registry/search?q=x",
			wantStatus:     http.StatusBadGateway,
			wantBody:       "registry unavailable",
			upstreamStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.upstreamStatus == 0 {
					t.Error("upstream should not be called")
					return
				}
				w.WriteHeader(tc.upstreamStatus)
				_, _ = io.WriteString(w, "error")
			}))
			defer upstream.Close()
			_, mux := newProxyAgainst(t, upstream)

			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("body = %q, want substring %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestRegistryProxy_Search_limitClamping(t *testing.T) {
	var seenLimit string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenLimit = r.URL.Query().Get("limit")
		_, _ = io.WriteString(w, `{"servers":[]}`)
	}))
	defer upstream.Close()
	_, mux := newProxyAgainst(t, upstream)

	req := httptest.NewRequest(http.MethodGet,
		"/api/mcp/registry/search?q=a&limit=9999", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if seenLimit != fmt.Sprint(maxSearchLimit) {
		t.Errorf("limit=9999 → upstream limit=%q, want %d", seenLimit, maxSearchLimit)
	}

	// Zero → clamps up to 1. Use q=b so this query maps to a distinct
	// cache key (otherwise the first test's result is still cached).
	req = httptest.NewRequest(http.MethodGet,
		"/api/mcp/registry/search?q=b&limit=0", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if seenLimit != "1" {
		t.Errorf("limit=0 → upstream limit=%q, want 1", seenLimit)
	}
}

// SEC-u12c1-003 regression: CheckRedirect allowlist. Upstream tries
// to redirect us to a non-registry host; http.Client must refuse
// and the handler must surface 502.
func TestRegistryProxy_Search_refusesNonAllowlistedRedirect(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://127.0.0.1/metadata")
		w.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()

	p := NewRegistryProxy() // keeps production CheckRedirect (allows only registryHost)
	p.client.Transport = &redirectTransport{base: upstream.URL}
	p.client.Timeout = 2 * time.Second
	mux := http.NewServeMux()
	p.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/mcp/registry/search?q=x", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 on refused redirect", rec.Code)
	}
}

// --- fetchSearch cache behaviour ---

func TestRegistryProxy_fetchSearch_cachesIdenticalQueries(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"servers":[]}`)
	}))
	defer upstream.Close()
	p, _ := newProxyAgainst(t, upstream)

	if _, _, err := p.fetchSearch(t.Context(), "same", 5); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if _, cached, err := p.fetchSearch(t.Context(), "same", 5); err != nil {
		t.Fatalf("second fetch: %v", err)
	} else if !cached {
		t.Error("second fetch did not report cached=true")
	}

	if got := hits.Load(); got != 1 {
		t.Errorf("upstream hit %d times, want 1 (second call must be cached)", got)
	}
}

func TestRegistryProxy_fetchSearch_expiryForcesRefetch(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"servers":[]}`)
	}))
	defer upstream.Close()
	p, _ := newProxyAgainst(t, upstream)

	if _, _, err := p.fetchSearch(t.Context(), "same", 5); err != nil {
		t.Fatalf("first fetch: %v", err)
	}

	// Force expiry by backdating insertedAt past the TTL.
	p.cache.mu.Lock()
	for k, e := range p.cache.entries {
		e.insertedAt = time.Now().Add(-2 * registryCacheTTL)
		p.cache.entries[k] = e
	}
	p.cache.mu.Unlock()

	if _, _, err := p.fetchSearch(t.Context(), "same", 5); err != nil {
		t.Fatalf("second fetch: %v", err)
	}

	if got := hits.Load(); got != 2 {
		t.Errorf("upstream hit %d times, want 2 (expired cache must refetch)", got)
	}
}

func TestRegistryProxy_fetchSearch_differentLimitsAreDistinctKeys(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"servers":[]}`)
	}))
	defer upstream.Close()
	p, _ := newProxyAgainst(t, upstream)

	_, _, _ = p.fetchSearch(t.Context(), "q", 5)
	_, _, _ = p.fetchSearch(t.Context(), "q", 10)

	if got := hits.Load(); got != 2 {
		t.Errorf("different limits shared cache key; upstream hit %d times, want 2", got)
	}
}

// --- evictLocked ---

func TestEvictLocked_dropsExpiredFirst(t *testing.T) {
	p := NewRegistryProxy()

	now := time.Now()
	// 32 expired + 32 fresh = 64; at-cap path runs.
	for i := range 32 {
		p.cache.entries[fmt.Sprintf("exp%d", i)] = registryCacheEntry{
			insertedAt: now.Add(-time.Hour), body: []byte("x"),
		}
	}
	for i := range 32 {
		p.cache.entries[fmt.Sprintf("fresh%d", i)] = registryCacheEntry{
			insertedAt: now, body: []byte("x"),
		}
	}

	p.cache.mu.Lock()
	p.cache.evictLocked()
	p.cache.mu.Unlock()

	// All 32 expired dropped; 32 fresh remain (under-cap, so no
	// further eviction).
	if got := len(p.cache.entries); got != 32 {
		t.Errorf("after evict len(cache) = %d, want 32 (all expired dropped)", got)
	}
	for k, entry := range p.cache.entries {
		if time.Since(entry.insertedAt) >= registryCacheTTL {
			t.Errorf("expired entry %q survived: insertedAt=%v", k, entry.insertedAt)
		}
	}
}

func TestEvictLocked_whenAllFresh_dropsOldest(t *testing.T) {
	p := NewRegistryProxy()

	now := time.Now()
	// 64 fresh entries, staggered in time (k00 is oldest).
	for i := range maxCacheEntries {
		p.cache.entries[fmt.Sprintf("k%02d", i)] = registryCacheEntry{
			insertedAt: now.Add(time.Duration(i) * time.Millisecond),
			body:       []byte("x"),
		}
	}

	p.cache.mu.Lock()
	p.cache.evictLocked()
	p.cache.mu.Unlock()

	if got := len(p.cache.entries); got != maxCacheEntries-1 {
		t.Errorf("after evict len(cache) = %d, want %d", got, maxCacheEntries-1)
	}
	if _, ok := p.cache.entries["k00"]; ok {
		t.Error("oldest entry k00 should have been evicted")
	}
}

func TestEvictLocked_underCap_isNoop(t *testing.T) {
	p := NewRegistryProxy()
	now := time.Now()
	for i := range 10 {
		p.cache.entries[fmt.Sprintf("k%d", i)] = registryCacheEntry{
			insertedAt: now,
		}
	}

	p.cache.mu.Lock()
	p.cache.evictLocked()
	p.cache.mu.Unlock()

	if got := len(p.cache.entries); got != 10 {
		t.Errorf("under-cap evict dropped entries: len=%d, want 10", got)
	}
}

// F2 (test-review u12c1): singleflight coalescing. Two concurrent
// fetchSearch calls with the same (q, limit) key must coalesce to a
// single upstream GET; the follower returns the leader's result with
// cached=true.
//
// Contract: fetchSearch godoc — "N browser tabs typing the same
// query no longer hold N outbound goroutines waiting on the same
// 10-second HTTP call."
func TestRegistryProxy_fetchSearch_coalescesConcurrentCallers(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	first := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		// Signal once that the leader is inside the handler (inflight
		// entry is registered) then block until we release.
		select {
		case first <- struct{}{}:
		default:
		}
		<-release
		_, _ = io.WriteString(w, `{"servers":[]}`)
	}))
	defer upstream.Close()
	p, _ := newProxyAgainst(t, upstream)

	type result struct {
		err    error
		body   []byte
		cached bool
	}
	out := make(chan result, 2)

	// Leader.
	go func() {
		b, c, e := p.fetchSearch(t.Context(), "q", 5)
		out <- result{e, b, c}
	}()

	// Wait until the leader is past the lock + into the upstream
	// handler — guarantees the inflight map holds the barrier.
	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("leader never entered upstream handler")
	}

	// Follower.
	go func() {
		b, c, e := p.fetchSearch(t.Context(), "q", 5)
		out <- result{e, b, c}
	}()
	// Let the follower reach its select on bar.done. Correctness does
	// not depend on this sleep: the final hits==1 assertion proves
	// coalescing regardless.
	time.Sleep(50 * time.Millisecond)

	close(release)

	var r1, r2 result
	select {
	case r1 = <-out:
	case <-time.After(2 * time.Second):
		t.Fatal("first caller never completed")
	}
	select {
	case r2 = <-out:
	case <-time.After(2 * time.Second):
		t.Fatal("second caller never completed")
	}

	if r1.err != nil || r2.err != nil {
		t.Fatalf("errs: %v %v", r1.err, r2.err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream hits = %d, want 1 (followers must coalesce)", got)
	}
	// DoChan reports Shared=true for all callers when the result was
	// coalesced, so both should see cached=true.
	if !r1.cached || !r2.cached {
		t.Errorf("cached = (%v, %v), want (true, true) — coalesced calls share the result", r1.cached, r2.cached)
	}
}

// F2 (test-review u12c1): follower sees the leader's error when the
// upstream fails. Coverage for the `bar.err != nil` branch in
// fetchSearch.
func TestRegistryProxy_fetchSearch_followerReceivesLeaderError(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	first := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		select {
		case first <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	p, _ := newProxyAgainst(t, upstream)

	type result struct{ err error }
	out := make(chan result, 2)
	go func() { _, _, e := p.fetchSearch(t.Context(), "q", 5); out <- result{e} }()
	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("leader never hit upstream")
	}
	go func() { _, _, e := p.fetchSearch(t.Context(), "q", 5); out <- result{e} }()
	time.Sleep(50 * time.Millisecond)
	close(release)

	r1 := <-out
	r2 := <-out
	if r1.err == nil || r2.err == nil {
		t.Errorf("both callers expected non-nil error, got (%v, %v)", r1.err, r2.err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream hit %d times, want 1 (error path must coalesce too)", got)
	}
}

// F5 (test-review u12c1): doFetch body-size guards. The
// declared-length path (Content-Length > cap) and the undeclared
// length path (chunked body past cap) both terminate with a 502 via
// the handler, never a silently-truncated "empty results" lie.
func TestRegistryProxy_doFetch_rejectsOversizeDeclaredLength(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Honest-but-oversize: declares Content-Length above the cap.
		w.Header().Set("Content-Length", fmt.Sprint(maxRegistryBody+1))
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	_, mux := newProxyAgainst(t, upstream)

	req := httptest.NewRequest(http.MethodGet, "/api/mcp/registry/search?q=x", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("oversize declared length status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "registry unavailable") {
		t.Errorf("body = %q, want 'registry unavailable'", rec.Body.String())
	}
}

func TestRegistryProxy_doFetch_rejectsOversizeUndeclaredBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Undeclared length: no Content-Length header; body streams
		// past the cap. Go's net/http sends Transfer-Encoding: chunked
		// when no Content-Length is set and the response is not yet
		// complete.
		chunk := strings.Repeat("a", 64*1024)
		written := 0
		for written <= maxRegistryBody {
			n, err := io.WriteString(w, chunk)
			if err != nil {
				return
			}
			written += n
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer upstream.Close()
	_, mux := newProxyAgainst(t, upstream)

	req := httptest.NewRequest(http.MethodGet, "/api/mcp/registry/search?q=x", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("oversize streamed body status = %d, want 502", rec.Code)
	}
}

// u12c2-f4: same-host redirect under the hop cap is allowed. Pins the
// "return nil" branch at the bottom of the CheckRedirect func — a
// legitimate 301/302 to a sibling path on the registry host itself
// must still work after the allowlist check.
func TestRegistryProxy_CheckRedirect_sameHostAllowed(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if strings.HasSuffix(r.URL.Path, "/redirected") {
			_, _ = io.WriteString(w, `{"servers":[]}`)
			return
		}
		// Same-host redirect: redirectTransport rewrites the outbound
		// URL back to this httptest server.
		w.Header().Set("Location", r.URL.Path+"/redirected")
		w.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()
	_, mux := newProxyAgainst(t, upstream)

	req := httptest.NewRequest(http.MethodGet,
		"/api/mcp/registry/search?q=x", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("same-host redirect status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := hits.Load(); got < 2 {
		t.Errorf("upstream hit %d times, want >=2 (redirect followed)", got)
	}
}

// u12c2-f4: chain of 3+ same-host redirects is refused. Pins the
// "too many redirects" branch. Without the cap, a cooperative-but-
// chatty upstream could keep the proxy chasing redirects far longer
// than the 10s Timeout.
func TestRegistryProxy_CheckRedirect_rejectsTooManyHops(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// Every path redirects to another same-host path → chain.
		w.Header().Set("Location", r.URL.Path+"x")
		w.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()
	_, mux := newProxyAgainst(t, upstream)

	req := httptest.NewRequest(http.MethodGet,
		"/api/mcp/registry/search?q=x", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("chain redirect status = %d, want 502", rec.Code)
	}
	// Upstream should have been hit 3-4 times (initial + up to 3
	// follows before the cap fires). Anything under 3 means the cap
	// fired too early; anything above 5 means the cap didn't fire at
	// all.
	if got := hits.Load(); got < 3 || got > 5 {
		t.Errorf("hop count = %d, want 3-5 (cap at 3 redirects)", got)
	}
}

// u12c2-f6: fetchSearch calls evictLocked when a successful fetch
// would push the cache over maxCacheEntries. Pins the
// "len(p.cache.entries) >= maxCacheEntries → evictLocked" branch.
func TestRegistryProxy_fetchSearch_triggersEvictAtCap(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"servers":[]}`)
	}))
	defer upstream.Close()
	p, _ := newProxyAgainst(t, upstream)

	// Pre-fill the cache to exactly maxCacheEntries with fresh
	// staggered entries (evictLocked would no-op if they were
	// expired so we need them fresh).
	now := time.Now()
	p.cache.mu.Lock()
	for i := range maxCacheEntries {
		p.cache.entries[fmt.Sprintf("seed%02d", i)] = registryCacheEntry{
			insertedAt: now.Add(time.Duration(i) * time.Millisecond),
			body:       []byte("x"),
		}
	}
	p.cache.mu.Unlock()

	// Fetch a new key — eviction must fire on insert so the cache
	// stays at maxCacheEntries (not maxCacheEntries+1).
	if _, _, err := p.fetchSearch(t.Context(), "new-key", 7); err != nil {
		t.Fatalf("fetchSearch: %v", err)
	}

	p.cache.mu.Lock()
	defer p.cache.mu.Unlock()
	if got := len(p.cache.entries); got != maxCacheEntries {
		t.Errorf("cache size after at-cap insert = %d, want %d (eviction did not fire)",
			got, maxCacheEntries)
	}
	// The new entry must be present under the composed key.
	if _, ok := p.cache.entries["new-key|7"]; !ok {
		t.Error("new entry missing from cache after at-cap insert")
	}
	// The oldest seed (seed00) should have been the one evicted.
	if _, ok := p.cache.entries["seed00"]; ok {
		t.Error("oldest seed survived eviction; evictLocked picked wrong victim")
	}
}

// u12c2-f7: follower waiting on the inflight barrier aborts cleanly
// when its own ctx is cancelled; leader still completes untainted.
// Pins the ctx.Done() branch of the follower select in fetchSearch.
func TestRegistryProxy_fetchSearch_followerRespectsCtxCancel(t *testing.T) {
	release := make(chan struct{})
	first := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case first <- struct{}{}:
		default:
		}
		<-release
		_, _ = io.WriteString(w, `{"servers":[]}`)
	}))
	defer upstream.Close()
	p, _ := newProxyAgainst(t, upstream)

	// Leader blocked in upstream handler.
	leaderDone := make(chan error, 1)
	go func() {
		_, _, err := p.fetchSearch(t.Context(), "q", 5)
		leaderDone <- err
	}()
	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("leader never hit upstream")
	}

	// Follower with a pre-cancelled ctx must bail immediately via
	// the ctx.Done() branch of the follower select.
	followerCtx, followerCancel := context.WithCancel(t.Context())
	followerCancel()
	_, _, err := p.fetchSearch(followerCtx, "q", 5)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("follower with cancelled ctx err = %v, want context.Canceled", err)
	}

	// Leader continues to successful completion when released.
	close(release)
	select {
	case err := <-leaderDone:
		if err != nil {
			t.Errorf("leader error after follower bailed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("leader never completed after follower cancellation")
	}
}

func BenchmarkRegistryCacheGetOrFetch(b *testing.B) {
	payload := []byte(`{"servers":[{"name":"test","url":"http://example.com"}]}`)

	b.Run("hit_same_key", func(b *testing.B) {
		cache := newRegistryCache(maxCacheEntries)
		// Pre-seed.
		cache.mu.Lock()
		cache.entries["bench-key"] = registryCacheEntry{insertedAt: time.Now(), body: payload}
		cache.mu.Unlock()

		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_, _, _ = cache.GetOrFetch(context.Background(), "bench-key", func() ([]byte, error) {
					return payload, nil
				})
			}
		})
	})

	b.Run("miss_same_key_singleflight", func(b *testing.B) {
		cache := newRegistryCache(maxCacheEntries)
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_, _, _ = cache.GetOrFetch(context.Background(), "sf-key", func() ([]byte, error) {
					return payload, nil
				})
			}
		})
	})

	b.Run("miss_different_keys", func(b *testing.B) {
		cache := newRegistryCache(10000)
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				key := fmt.Sprintf("key-%d", i)
				i++
				_, _, _ = cache.GetOrFetch(context.Background(), key, func() ([]byte, error) {
					return payload, nil
				})
			}
		})
	})
}
