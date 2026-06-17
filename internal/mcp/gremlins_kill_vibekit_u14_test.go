package mcp

// Mutant-killing tests for unit vibekit-u14 (internal/mcp).
//
// Each test pins the EXACT operator at a surviving gremlins mutant so
// that applying the mutation changes an asserted observable. Helpers
// and local identifiers are prefixed gk_vibekit_u14_ to avoid colliding
// with sibling units sharing this package. Tests only; no production
// code is modified.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// gk_vibekit_u14_captureLogs redirects slog.Default to an in-memory
// buffer at Debug level for the duration of the returned restore func.
// Several mutants here only change WHICH branch logs (or whether it
// logs at all), so capturing the log is the only observable.
func gk_vibekit_u14_captureLogs(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	prev := slog.Default()
	buf := &bytes.Buffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf, func() { slog.SetDefault(prev) }
}

// gk_vibekit_u14_errReader always fails on Read with a non-EOF error so
// io.Copy in drainRegistryBody returns a non-nil error.
type gk_vibekit_u14_errReader struct{}

func (gk_vibekit_u14_errReader) Read(_ []byte) (int, error) {
	return 0, errors.New("gk read boom")
}

// gk_vibekit_u14_serverByName returns the stored *Server with the given
// name (or nil). Reads s.servers directly; safe because the tests using
// it pass nil onChange so SetKnownTools spawns no goroutine.
func gk_vibekit_u14_serverByName(s *Store, name string) *Server {
	for _, sv := range s.servers {
		if sv.Name == name {
			return sv
		}
	}
	return nil
}

// ids.go:22:14 CONDITIONALS_BOUNDARY — `len(raw) > 32`.
// A 32-char id is the boundary: `>` accepts it, `>=` rejects it.
func Test_gk_vibekit_u14_ParseServerID_lengthBoundary(t *testing.T) {
	at := strings.Repeat("a", 32)
	id, err := ParseServerID(at)
	if err != nil {
		t.Errorf("ParseServerID(32 chars) = err %v, want nil (mutation > to >= rejects the 32-char boundary)", err)
	}
	if string(id) != at {
		t.Errorf("ParseServerID(32 chars) id = %q, want %q", string(id), at)
	}
	// 33 chars must still be rejected, pinning the comparison direction.
	if _, err := ParseServerID(strings.Repeat("a", 33)); err == nil {
		t.Error("ParseServerID(33 chars) = nil err, want too-long error")
	}
}

// registry_proxy.go:118:12 CONDITIONALS_BOUNDARY — `len(q) > maxSearchQueryLen`.
// registry_proxy.go:129:8  CONDITIONALS_BOUNDARY — `c < 0x20` (control-char scan).
func Test_gk_vibekit_u14_RegistryQueryLengthAndControlBoundaries(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"servers":[]}`)
	}))
	defer upstream.Close()
	_, mux := newProxyAgainst(t, upstream)

	// 118: a query of exactly maxSearchQueryLen must NOT be rejected as
	// "too long" — boundary is len > cap, not >=.
	t.Run("query_at_max_len_accepted", func(t *testing.T) {
		q := strings.Repeat("a", maxSearchQueryLen)
		req := httptest.NewRequest(http.MethodGet, "/api/mcp/registry/search?q="+q, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d body=%q, want 200 (exact-cap query must be accepted; mutation > to >= rejects it)",
				rec.Code, rec.Body.String())
		}
	})

	// 129: a space (0x20) inside the query is NOT a control char —
	// boundary is c < 0x20, not <=. It must be accepted.
	t.Run("interior_space_accepted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/mcp/registry/search?q=a%20b", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d body=%q, want 200 (space is not a control char; mutation < to <= rejects it)",
				rec.Code, rec.Body.String())
		}
	})
}

// registry_proxy.go:95:17 CONDITIONALS_BOUNDARY — `len(via) >= 3`.
// Same-host redirect chain: with `>= 3` exactly 3 upstream requests are
// made before the cap fires; with `> 3` a 4th request leaks through.
func Test_gk_vibekit_u14_RegistryRedirectHopBoundary(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Location", r.URL.Path+"x")
		w.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()
	_, mux := newProxyAgainst(t, upstream)

	req := httptest.NewRequest(http.MethodGet, "/api/mcp/registry/search?q=x", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 on refused redirect chain", rec.Code)
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("upstream hits = %d, want exactly 3 (cap fires at len(via) >= 3; mutation >= to > leaks a 4th)", got)
	}
}

// registry_proxy.go:244:24 CONDITIONALS_BOUNDARY — `resp.ContentLength > maxRegistryBody`.
// registry_proxy.go:255:22 CONDITIONALS_BOUNDARY — `int64(len(body)) > maxRegistryBody`.
// A body of EXACTLY maxRegistryBody must be accepted (200). Mutating
// either `>` to `>=` flips that exact-cap case to a 502.
func Test_gk_vibekit_u14_RegistryBodyExactCapAccepted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(maxRegistryBody))
		w.WriteHeader(http.StatusOK)
		chunk := make([]byte, 32*1024)
		for i := range chunk {
			chunk[i] = 'a'
		}
		remaining := maxRegistryBody
		for remaining > 0 {
			n := len(chunk)
			if n > remaining {
				n = remaining
			}
			if _, err := w.Write(chunk[:n]); err != nil {
				return
			}
			remaining -= n
		}
	}))
	defer upstream.Close()
	_, mux := newProxyAgainst(t, upstream)

	req := httptest.NewRequest(http.MethodGet, "/api/mcp/registry/search?q=x", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: a body of exactly maxRegistryBody must be accepted "+
			"(mutation > to >= on either the ContentLength check or the post-read size check rejects it)", rec.Code)
	}
}

// registry_proxy.go:267:71 CONDITIONALS_NEGATION — `err != nil` in drainRegistryBody.
// With a reader that errors, the original logs "registry drain stopped";
// the negated `err == nil` would not log.
func Test_gk_vibekit_u14_DrainRegistryBodyLogsOnError(t *testing.T) {
	buf, restore := gk_vibekit_u14_captureLogs(t)
	defer restore()
	drainRegistryBody(gk_vibekit_u14_errReader{})
	if !strings.Contains(buf.String(), "registry drain stopped") {
		t.Errorf("drainRegistryBody on a read error did not log; mutation flipped err != nil to err == nil. log=%q",
			buf.String())
	}
}

// registry_proxy.go:346:11 INCREMENT_DECREMENT — `evicted++` in the expired-delete loop.
// registry_proxy.go:363:13 CONDITIONALS_NEGATION — `evicted > 0`.
// Five expired entries get deleted; with the original increment evicted=5
// and the cache-evicted log fires. `evicted--` (→ -5) or negating
// `evicted > 0` (→ `evicted <= 0`, false for 5) both suppress the log.
func Test_gk_vibekit_u14_EvictLockedExpiredLogs(t *testing.T) {
	c := newRegistryCache(64)
	old := time.Now().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		c.entries[fmt.Sprintf("gkexp%d", i)] = registryCacheEntry{insertedAt: old, body: []byte("x")}
	}
	buf, restore := gk_vibekit_u14_captureLogs(t)
	defer restore()
	c.mu.Lock()
	c.evictLocked()
	c.mu.Unlock()

	if len(c.entries) != 0 {
		t.Errorf("expired entries remaining = %d, want 0", len(c.entries))
	}
	if !strings.Contains(buf.String(), "registry cache evicted") {
		t.Errorf("eviction occurred but no evict log; mutation (evicted-- at 346, or evicted>0 negated at 363) suppressed it. log=%q",
			buf.String())
	}
}

// registry_proxy.go:361:10 INCREMENT_DECREMENT — `evicted++` in the at-cap block.
// A full cache of fresh entries triggers the oldest-eviction branch;
// the original increment makes evicted=1 so the log fires. `evicted--`
// (→ -1) suppresses it.
func Test_gk_vibekit_u14_EvictLockedAtCapLogs(t *testing.T) {
	c := newRegistryCache(8)
	now := time.Now()
	for i := 0; i < 8; i++ {
		c.entries[fmt.Sprintf("gkfresh%02d", i)] = registryCacheEntry{
			insertedAt: now.Add(time.Duration(i) * time.Millisecond),
			body:       []byte("x"),
		}
	}
	buf, restore := gk_vibekit_u14_captureLogs(t)
	defer restore()
	c.mu.Lock()
	c.evictLocked()
	c.mu.Unlock()

	if len(c.entries) != 7 {
		t.Errorf("after at-cap evict len = %d, want 7", len(c.entries))
	}
	if !strings.Contains(buf.String(), "registry cache evicted") {
		t.Errorf("at-cap eviction occurred but no evict log; mutation (evicted-- at 361) suppressed it. log=%q",
			buf.String())
	}
}

// registry_proxy.go:363:13 CONDITIONALS_BOUNDARY — `evicted > 0`.
// An under-cap, all-fresh cache evicts nothing (evicted=0). The original
// `> 0` does NOT log; the boundary mutation `>= 0` logs spuriously.
func Test_gk_vibekit_u14_EvictLockedUnderCapNoLog(t *testing.T) {
	c := newRegistryCache(64)
	now := time.Now()
	for i := 0; i < 3; i++ {
		c.entries[fmt.Sprintf("gku%d", i)] = registryCacheEntry{insertedAt: now, body: []byte("x")}
	}
	buf, restore := gk_vibekit_u14_captureLogs(t)
	defer restore()
	c.mu.Lock()
	c.evictLocked()
	c.mu.Unlock()

	if len(c.entries) != 3 {
		t.Errorf("under-cap evict changed len = %d, want 3", len(c.entries))
	}
	if strings.Contains(buf.String(), "registry cache evicted") {
		t.Errorf("no eviction happened but evict log emitted; mutation (evicted>0 to >=0 at 363) logged spuriously. log=%q",
			buf.String())
	}
}

// store.go:223:46 CONDITIONALS_NEGATION — `chErr != nil` when tightening perms.
// A 0644 file is chmod'd to 0600 successfully (chErr == nil), so the
// original logs NO warning. The negated `chErr == nil` would warn.
func Test_gk_vibekit_u14_LoadChmodSuccessNoWarn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"servers":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force 0644 regardless of umask so load()'s tighten path runs.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	buf, restore := gk_vibekit_u14_captureLogs(t)
	defer restore()
	if _, err := New(context.Background(), dir, nil); err != nil {
		t.Fatalf("New: %v", err)
	}

	if strings.Contains(buf.String(), "tighten mcp.json perms failed") {
		t.Errorf("chmod succeeded but warn logged; mutation flipped chErr != nil to chErr == nil. log=%q", buf.String())
	}
	// Sanity: the chmod path actually ran (perms tightened).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perms = %v, want 0600 (tighten path not exercised)", info.Mode().Perm())
	}
}

// store.go:233:51 CONDITIONALS_NEGATION — `rErr != nil` after renaming a
// corrupt file aside. The rename succeeds (rErr == nil), so the original
// logs the Warn "moved aside". The negated `rErr == nil` takes the Error
// "preserve corrupt mcp.json failed" branch instead.
func Test_gk_vibekit_u14_LoadCorruptRenameSuccessLogsMovedAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}

	buf, restore := gk_vibekit_u14_captureLogs(t)
	defer restore()
	if _, err := New(context.Background(), dir, nil); err != nil {
		t.Fatalf("New: %v", err)
	}

	log := buf.String()
	if !strings.Contains(log, "moved aside") {
		t.Errorf("corrupt file renamed but Warn 'moved aside' not logged; mutation flipped rErr != nil to rErr == nil. log=%q", log)
	}
	if strings.Contains(log, "preserve corrupt mcp.json failed") {
		t.Errorf("rename succeeded but Error 'preserve corrupt' logged; mutation took the failure branch. log=%q", log)
	}
}

// store_crud.go:225:15 CONDITIONALS_NEGATION — `srv.Name == name` lookup.
// SetKnownTools("gkb") must set gkb's tools (== match). The negated
// `srv.Name != name` matches the first NON-target server (gka) instead.
func Test_gk_vibekit_u14_SetKnownToolsMatchesByName(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "gka", Command: "bash"}); err != nil {
		t.Fatalf("create gka: %v", err)
	}
	if _, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "gkb", Command: "bash"}); err != nil {
		t.Fatalf("create gkb: %v", err)
	}

	s.SetKnownTools(context.Background(), "gkb", []string{"gk_tool"})

	b := gk_vibekit_u14_serverByName(s, "gkb")
	a := gk_vibekit_u14_serverByName(s, "gka")
	if b == nil || a == nil {
		t.Fatal("servers missing after create")
	}
	if len(b.KnownTools) != 1 || b.KnownTools[0] != "gk_tool" {
		t.Errorf("SetKnownTools(gkb) updated the wrong server; gkb.KnownTools = %v, want [gk_tool] (mutation matched != instead of ==)", b.KnownTools)
	}
	if len(a.KnownTools) != 0 {
		t.Errorf("gka.KnownTools = %v, want empty (only the named server should be updated)", a.KnownTools)
	}
}

// store_crud.go:231:11 CONDITIONALS_NEGATION — `found == nil`.
// When the server IS found, the original proceeds to persist. The
// negated `found != nil` returns early and never persists, so the new
// tool list never reaches disk.
func Test_gk_vibekit_u14_SetKnownToolsPersistsOnFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "gka", Command: "bash"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	s.SetKnownTools(context.Background(), "gka", []string{"gk_persist_tool"})

	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatalf("read mcp.json: %v", err)
	}
	if !strings.Contains(string(data), "gk_persist_tool") {
		t.Errorf("SetKnownTools(existing) did not persist the tool; mutation flipped found == nil to found != nil and returned early. file=%s", data)
	}
}

// store_crud.go:241:32 CONDITIONALS_NEGATION — `err != nil` after persist.
// Persist succeeds (err == nil), so the original logs NO failure warn.
// The negated `err == nil` logs "persist after SetKnownTools failed".
func Test_gk_vibekit_u14_SetKnownToolsNoWarnOnPersistSuccess(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "gka", Command: "bash"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	buf, restore := gk_vibekit_u14_captureLogs(t)
	defer restore()
	s.SetKnownTools(context.Background(), "gka", []string{"gk_tool2"})

	if strings.Contains(buf.String(), "persist after SetKnownTools failed") {
		t.Errorf("persist succeeded but failure warn logged; mutation flipped err != nil to err == nil. log=%q", buf.String())
	}
}

// validate.go boundary mutants (all `>` → `>=`). Each case sits exactly
// at the cap, which the original accepts (nil error) and which every
// boundary mutation would reject:
//
//	120:16 `len(tools) > maxDisabledTools`
//	128:13 `len(t)     > disabledToolMax`
//	143:20 `len(s.Command) > commandMax`
//	152:17 `len(s.Args)    > maxArgs`
//	159:13 `len(a)         > argMax`
//	170:16 `len(s.URL)     > urlMax`
func Test_gk_vibekit_u14_ValidateBoundaries(t *testing.T) {
	const urlPrefix = "https://x.example/"
	urlAtMax := urlPrefix + strings.Repeat("a", urlMax-len(urlPrefix))

	cases := []struct {
		srv  *Server
		name string
	}{
		{
			name: "disabled_tools_at_max_count",
			srv:  &Server{Transport: TransportStdio, Name: "gkok", Command: "bash", DisabledTools: make([]string, maxDisabledTools)},
		},
		{
			name: "disabled_tool_at_max_len",
			srv:  &Server{Transport: TransportStdio, Name: "gkok", Command: "bash", DisabledTools: []string{strings.Repeat("a", disabledToolMax)}},
		},
		{
			name: "command_at_max_len",
			srv:  &Server{Transport: TransportStdio, Name: "gkok", Command: strings.Repeat("a", commandMax)},
		},
		{
			name: "args_at_max_count",
			srv:  &Server{Transport: TransportStdio, Name: "gkok", Command: "bash", Args: make([]string, maxArgs)},
		},
		{
			name: "arg_at_max_len",
			srv:  &Server{Transport: TransportStdio, Name: "gkok", Command: "bash", Args: []string{strings.Repeat("a", argMax)}},
		},
		{
			name: "url_at_max_len",
			srv:  &Server{Transport: TransportHTTP, Name: "gkok", URL: urlAtMax},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(tc.srv); err != nil {
				t.Errorf("Validate(%s) = %v, want nil (boundary value must be accepted; mutation > to >= rejects it)", tc.name, err)
			}
		})
	}
}
