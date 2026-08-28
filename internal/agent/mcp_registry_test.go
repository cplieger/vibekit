package agent

// Tests for mcp_registry.go: the in-memory runtime view of MCP
// servers reported connected / needing OAuth / failed by kiro-cli.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// fakeMCPConfig is the registry filter tests' MCP name census.
//
// The three sets are independent fields rather than one set plus derived views,
// so a test can stage the case that matters: a name in `configured` but not
// `enabled` is a server the user switched off, and a name in `all` but not
// `configured` is a Power's. The fake does NOT auto-nest them, because a test
// asserting the guard's precedence needs to be able to stage each boundary on
// its own; newHubWithMCPConfig's helpers below build the nested shapes.
type fakeMCPConfig struct {
	enabled    map[string]struct{}
	configured map[string]struct{}
	all        map[string]struct{}
	mu         sync.Mutex
}

func (f *fakeMCPConfig) EnabledNames(_ context.Context) map[string]struct{} {
	return f.copyOf(f.enabled)
}

func (f *fakeMCPConfig) ConfiguredNames(_ context.Context) map[string]struct{} {
	return f.copyOf(f.configured)
}

func (f *fakeMCPConfig) AllNames(_ context.Context) map[string]struct{} {
	return f.copyOf(f.all)
}

func (f *fakeMCPConfig) copyOf(src map[string]struct{}) map[string]struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]struct{}, len(src))
	for k := range src {
		out[k] = struct{}{}
	}
	return out
}

func newHubWithMCPConfig(cfg mcpNameSets) *Runtime {
	cs := newFakeChatStore()
	factory := func() ACPBridge { return newFakeBridge() }
	var opts []Option
	if cfg != nil {
		opts = append(opts, WithMCPConfig(cfg))
	}
	h := New(context.Background(), "/tmp/work", factory, cs, opts...)
	cs.Bus = h
	return h
}

func TestMCPRegistry_RecordConnectedPopulatesSnapshot(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	h.mcpRegistry.RecordConnected(t.Context(), "github", nil, nil, nil)
	snap := h.mcpRegistry.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot = %+v, want 1 server", snap)
	}
	if snap[0].Name != "github" || snap[0].State != mcpStateConnected {
		t.Errorf("snapshot[0] = %+v", snap[0])
	}
}

func TestMCPRegistry_RecordOAuthOverridesState(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	h.mcpRegistry.RecordConnected(t.Context(), "linear", nil, nil, nil)
	h.mcpRegistry.RecordOAuth(t.Context(), "linear", "https://oauth.example/auth")

	snap := h.mcpRegistry.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap[0].State != mcpStateOAuth {
		t.Errorf("state = %q, want %q", snap[0].State, mcpStateOAuth)
	}
	if snap[0].OAuthURL != "https://oauth.example/auth" {
		t.Errorf("oauth url = %q", snap[0].OAuthURL)
	}
}

func TestMCPRegistry_RecordInitFailureRecordsError(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	_, before := h.bus.fanout.Bounds()
	h.mcpRegistry.RecordInitFailure(t.Context(), "broken", "connection refused")

	snap := h.mcpRegistry.Snapshot()
	if len(snap) != 1 || snap[0].Name != "broken" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap[0].State != mcpStateFailed {
		t.Errorf("state = %q, want %q", snap[0].State, mcpStateFailed)
	}
	if snap[0].Error != "connection refused" {
		t.Errorf("error = %q", snap[0].Error)
	}
	// mcp_failed SSE must be emitted.
	types := extractTypes(t, bufferedSince(h, before))
	found := false
	for _, tp := range types {
		if tp == "mcp_failed" {
			found = true
		}
	}
	if !found {
		t.Errorf("mcp_failed event not emitted, got %v", types)
	}
}

func TestMCPRegistry_ClearAllEmitsDisconnect(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	h.mcpRegistry.RecordConnected(t.Context(), "a", nil, nil, nil)
	h.mcpRegistry.RecordConnected(t.Context(), "b", nil, nil, nil)

	_, before := h.bus.fanout.Bounds()
	h.mcpRegistry.clearAll(t.Context())

	if len(h.mcpRegistry.Snapshot()) != 0 {
		t.Error("clearAll left entries in registry")
	}
	types := extractTypes(t, bufferedSince(h, before))
	got := 0
	for _, tp := range types {
		if tp == "mcp_disconnected" {
			got++
		}
	}
	if got != 2 {
		t.Errorf("got %d mcp_disconnected events, want 2 (types=%v)", got, types)
	}
}

func TestMCPRegistry_ClearAllOnEmptyNoEvents(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	_, before := h.bus.fanout.Bounds()
	h.mcpRegistry.clearAll(t.Context())
	if _, head := h.bus.fanout.Bounds(); head != before {
		t.Error("clearAll on empty registry emitted events")
	}
}

func TestMCPRegistry_FiltersDisabledServerNotifications(t *testing.T) {
	cfg := enabledConfig("github")
	// The two names below are vibekit's own, switched off — the only case the
	// guard still drops. A name in NEITHER set is a different verdict entirely
	// (see TestMCPRegistry_RecordsUnconfiguredServerWithOrigin).
	cfg.configured["disabled-server"] = struct{}{}
	cfg.all["disabled-server"] = struct{}{}
	cfg.configured["another-disabled"] = struct{}{}
	cfg.all["another-disabled"] = struct{}{}
	h := newHubWithMCPConfig(cfg)
	h.mcpRegistry.RecordConnected(t.Context(), "github", nil, nil, nil)
	h.mcpRegistry.RecordConnected(t.Context(), "disabled-server", nil, nil, nil)
	h.mcpRegistry.RecordInitFailure(t.Context(), "another-disabled", "x")

	snap := h.mcpRegistry.Snapshot()
	if len(snap) != 1 || snap[0].Name != "github" {
		t.Errorf("snapshot = %+v, want only github", snap)
	}
}

// TestMCPRegistry_RecordsUnconfiguredServerWithOrigin is the T15 core: a server
// vibekit never configured is RECORDED (not dropped like a user-disabled one),
// carrying the origin that tells the UI the row is read-only. Before the guard
// was narrowed, every one of these cases produced no row at all while the
// server's tools sat in the agent's tool list.
func TestMCPRegistry_RecordsUnconfiguredServerWithOrigin(t *testing.T) {
	cases := map[string]struct {
		inAllNames bool
		wantOrigin vibekit.Origin
	}{
		"a power's server is named by the config file's powers block": {
			inAllNames: true, wantOrigin: vibekit.OriginPower,
		},
		"a server from a source vibekit cannot read is unattributable": {
			inAllNames: false, wantOrigin: vibekit.OriginUnknown,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := enabledConfig("mine")
			if tc.inAllNames {
				cfg.all["theirs"] = struct{}{}
			}
			h := newHubWithMCPConfig(cfg)
			h.mcpRegistry.RecordConnected(t.Context(), "theirs", []string{"do_thing"}, nil, nil)

			snap := h.mcpRegistry.Snapshot()
			if len(snap) != 1 {
				t.Fatalf("snapshot = %+v, want the unconfigured server recorded", snap)
			}
			if snap[0].Name != "theirs" || snap[0].State != mcpStateConnected {
				t.Errorf("snapshot[0] = %+v", snap[0])
			}
			if snap[0].Origin != tc.wantOrigin {
				t.Errorf("origin = %q, want %q", snap[0].Origin, tc.wantOrigin)
			}
		})
	}
}

// TestMCPRegistry_StampsUserOriginOnConfiguredServers pins the other side of the
// same rule: the row for the user's own server must NOT claim a foreign origin,
// or the UI would withhold its edit and delete affordances.
func TestMCPRegistry_StampsUserOriginOnConfiguredServers(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("github"))
	ctx := t.Context()
	h.mcpRegistry.RecordConnected(ctx, "github", nil, nil, nil)
	h.mcpRegistry.RecordOAuth(ctx, "github", "https://oauth.example/auth")
	if got := h.mcpRegistry.Snapshot()[0].Origin; got != vibekit.OriginUser {
		t.Errorf("origin after recordOAuth = %q, want %q", got, vibekit.OriginUser)
	}
	h.mcpRegistry.RecordInitFailure(ctx, "github", "boom")
	if got := h.mcpRegistry.Snapshot()[0].Origin; got != vibekit.OriginUser {
		t.Errorf("origin after recordInitFailure = %q, want %q", got, vibekit.OriginUser)
	}
}

// TestMCPRegistry_RecordDisabled covers the amendment: KAS's "disabled" status
// becomes a read-only row for a server vibekit never configured, and stays
// discarded for one it did. The second half is what keeps the narrowed guard
// from resurrecting a server the user switched off mid-session — the whole
// reason the early return was kept rather than deleted.
func TestMCPRegistry_RecordDisabled(t *testing.T) {
	cases := map[string]struct {
		cfg        func() *fakeMCPConfig
		wantRow    bool
		wantOrigin vibekit.Origin
	}{
		"the user's own server, enabled: the config row already says off-or-on": {
			cfg:     func() *fakeMCPConfig { return enabledConfig("mine") },
			wantRow: false,
		},
		"the user's own server, switched off: must not gain a runtime row": {
			cfg: func() *fakeMCPConfig {
				c := enabledConfig()
				c.configured["mine"] = struct{}{}
				c.all["mine"] = struct{}{}
				return c
			},
			wantRow: false,
		},
		"a power's server: the only evidence it exists": {
			cfg: func() *fakeMCPConfig {
				c := enabledConfig()
				c.all["mine"] = struct{}{}
				return c
			},
			wantRow: true, wantOrigin: vibekit.OriginPower,
		},
		"an unattributable server: still shown, origin unknown": {
			cfg:     func() *fakeMCPConfig { return enabledConfig() },
			wantRow: true, wantOrigin: vibekit.OriginUnknown,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHubWithMCPConfig(tc.cfg())
			h.mcpRegistry.RecordDisabled(t.Context(), "mine")

			snap := h.mcpRegistry.Snapshot()
			if !tc.wantRow {
				if len(snap) != 0 {
					t.Fatalf("snapshot = %+v, want no row", snap)
				}
				return
			}
			if len(snap) != 1 {
				t.Fatalf("snapshot = %+v, want one row", snap)
			}
			if snap[0].State != mcpStateDisabled {
				t.Errorf("state = %q, want %q", snap[0].State, mcpStateDisabled)
			}
			if snap[0].Origin != tc.wantOrigin {
				t.Errorf("origin = %q, want %q", snap[0].Origin, tc.wantOrigin)
			}
		})
	}
}

// TestMCPRegistry_StatusJSONCarriesOrigin pins origin onto the wire. It is not
// omitempty on purpose: the client decides read-only from this field, so an
// absent one would make it guess.
func TestMCPRegistry_StatusJSONCarriesOrigin(t *testing.T) {
	cfg := enabledConfig("mine")
	cfg.all["theirs"] = struct{}{}
	h := newHubWithMCPConfig(cfg)
	h.mcpRegistry.RecordConnected(t.Context(), "mine", nil, nil, nil)
	h.mcpRegistry.RecordConnected(t.Context(), "theirs", nil, nil, nil)

	rec := httptest.NewRecorder()
	h.mcpRegistry.handleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/status", nil))

	raw := rec.Body.String()
	if !strings.Contains(raw, `"origin":"user"`) || !strings.Contains(raw, `"origin":"power"`) {
		t.Fatalf("body = %s, want both origins on the wire", raw)
	}
	var body struct {
		Servers []statusServer `json:"servers"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	// Alphabetical: mine, theirs.
	if body.Servers[0].Origin != vibekit.OriginUser || body.Servers[1].Origin != vibekit.OriginPower {
		t.Errorf("origins = %q / %q", body.Servers[0].Origin, body.Servers[1].Origin)
	}
}

func TestMCPRegistry_SnapshotIsStableAlphabetically(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	h.mcpRegistry.RecordConnected(t.Context(), "zulu", nil, nil, nil)
	h.mcpRegistry.RecordConnected(t.Context(), "alpha", nil, nil, nil)
	h.mcpRegistry.RecordConnected(t.Context(), "mike", nil, nil, nil)

	names := make([]string, 0)
	for _, s := range h.mcpRegistry.Snapshot() {
		names = append(names, s.Name)
	}
	if !slices.IsSorted(names) {
		t.Errorf("snapshot not sorted: %v", names)
	}
}

func TestMCPRegistry_OnChangeFiresOutsideLock(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	var mu sync.Mutex
	count := 0
	done := make(chan struct{}, 10)
	h.mcpRegistry.SetOnChange(func() {
		mu.Lock()
		count++
		mu.Unlock()
		done <- struct{}{}
	})
	h.mcpRegistry.RecordConnected(t.Context(), "a", nil, nil, nil)
	h.mcpRegistry.RecordOAuth(t.Context(), "a", "url")
	h.mcpRegistry.RecordInitFailure(t.Context(), "a", "err")
	h.mcpRegistry.clearAll(t.Context())

	// With debounced onChange, rapid-fire mutations coalesce into
	// fewer callbacks. Wait for at least one callback to confirm
	// the notifier fires outside the lock.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("onChange never fired")
	}
	// Allow debounce to settle.
	time.Sleep(200 * time.Millisecond)
	// Drain any additional callbacks.
	for {
		select {
		case <-done:
		default:
			goto drained
		}
	}
drained:
	mu.Lock()
	defer mu.Unlock()
	if count < 1 {
		t.Errorf("onChange count = %d, want >= 1", count)
	}
	// Shutdown to clean up the notifier goroutine.
	close(h.lifecycle.done)
}

// A burst of mutations is ONE change to the subscriber. Any real config edit
// mutates the registry several times in a row (a server recorded connected, then
// its tools, then a sibling's failure), and the subscriber is the steering
// generator: it walks .kiro and rewrites environment.md per call, so a burst that
// arrived as separate invocations would rewrite the file three times for one
// edit.
//
// On the bubble's clock the window is exact rather than probabilistic, which is
// what makes the count assertable at all: the same test on a real clock can only
// sleep and hope.
func TestMCPRegistry_SignalsInsideOneWindowFireOneCallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHubWithMCPConfig(nil)
		// Ends the notifier and New's two ticker loops. A defer rather than
		// t.Cleanup on purpose: Test waits for the bubble's goroutines to exit
		// BEFORE running cleanups, so a cleanup-time close deadlocks the bubble,
		// and the defer also runs on the t.Fatalf path.
		defer close(h.lifecycle.done)
		start := time.Now()
		var mu sync.Mutex
		var firedAt []time.Duration
		h.mcpRegistry.SetOnChange(func() {
			mu.Lock()
			firedAt = append(firedAt, time.Since(start))
			mu.Unlock()
		})

		h.mcpRegistry.signalChange()
		synctest.Sleep(30 * time.Millisecond)
		h.mcpRegistry.signalChange()
		synctest.Sleep(30 * time.Millisecond)
		h.mcpRegistry.signalChange()
		// Past the window the FIRST signal opened, so a coalescing notifier has
		// fired once by now and one that does not coalesce has fired per signal.
		synctest.Sleep(150 * time.Millisecond)

		mu.Lock()
		defer mu.Unlock()
		if len(firedAt) != 1 {
			t.Errorf("three signals 30ms apart fired onChange %d times (at %v), want 1", len(firedAt), firedAt)
		}
	})
}

// The other half of the same rule: the window coalesces a burst, it does not
// swallow a later edit. Two signals further apart than the window are two
// changes, and the subscriber must see both — a config edit made after the
// steering file was rewritten still has to reach it.
func TestMCPRegistry_SignalsBeyondTheWindowFireSeparateCallbacks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHubWithMCPConfig(nil)
		defer close(h.lifecycle.done) // see the sibling test for why not t.Cleanup
		var mu sync.Mutex
		calls := 0
		h.mcpRegistry.SetOnChange(func() {
			mu.Lock()
			calls++
			mu.Unlock()
		})

		h.mcpRegistry.signalChange()
		synctest.Sleep(150 * time.Millisecond)
		h.mcpRegistry.signalChange()
		synctest.Sleep(150 * time.Millisecond)

		mu.Lock()
		defer mu.Unlock()
		if calls != 2 {
			t.Errorf("two signals 150ms apart fired onChange %d times, want 2", calls)
		}
	})
}

func TestMCPRegistry_HandleStatusReturnsJSON(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	h.mcpRegistry.RecordConnected(t.Context(), "github", nil, nil, nil)
	h.mcpRegistry.RecordInitFailure(t.Context(), "broken", "no auth")

	req := httptest.NewRequest(http.MethodGet, "/api/mcp/status", nil)
	rec := httptest.NewRecorder()
	h.mcpRegistry.handleStatus(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Servers []statusServer `json:"servers"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Servers) != 2 {
		t.Fatalf("body = %+v", body)
	}
	// Alphabetical sort: broken then github.
	if body.Servers[0].Name != "broken" || body.Servers[0].State != "failed" {
		t.Errorf("body[0] = %+v", body.Servers[0])
	}
	if body.Servers[0].Error != "no auth" {
		t.Errorf("error = %q", body.Servers[0].Error)
	}
	if body.Servers[1].Name != "github" || body.Servers[1].State != "connected" {
		t.Errorf("body[1] = %+v", body.Servers[1])
	}
}

// BenchmarkMCPRegistrySnapshot measures the sorted-snapshot hot path
// with varying numbers of registered servers. Snapshot is called on
// every SSE reconnect and bridge spawn.
func BenchmarkMCPRegistrySnapshot(b *testing.B) {
	for _, n := range []int{1, 5, 20} {
		b.Run(fmt.Sprintf("servers=%d", n), func(b *testing.B) {
			h := newHubWithMCPConfig(nil)
			for i := range n {
				h.mcpRegistry.RecordConnected(b.Context(), fmt.Sprintf("server-%02d", i), nil, nil, nil)
			}
			b.ResetTimer()
			for b.Loop() {
				_ = h.mcpRegistry.Snapshot()
			}
		})
	}
}

// The real *mcp.Store's run of MCPConfigContractTest lives in internal/mcp's
// own test binary (TestStore_MCPConfigContract). It used to be duplicated here
// as well, which spent a second run on the identical assertion and made the
// runtime's test binary import internal/mcp for nothing else.

// TestMCPRegistry_PendingSummaryPartitionsByCause is the join a readiness timeout
// used to lack: the prompt path logged a chat id and nothing else, while every
// cause an operator acts on was already distinguishable per server here.
//
// One case per bucket, and the ENABLED-but-unreported case is the one that needs
// the census rather than the registry: a server that never said anything appears
// nowhere in the registry at all, so "absent from the reported set" is the only
// way it can be named. A server that reported CONNECTED is in none of the
// buckets, which is what stops the summary reporting a healthy fleet as pending.
func TestMCPRegistry_PendingSummaryPartitionsByCause(t *testing.T) {
	cfg := &fakeMCPConfig{
		enabled: map[string]struct{}{
			"quiet": {}, "broken": {}, "needsauth": {}, "working": {},
		},
	}
	cfg.configured = cfg.copyOf(cfg.enabled)
	cfg.all = cfg.copyOf(cfg.enabled)
	h := newHubWithMCPConfig(cfg)
	ctx := t.Context()

	h.mcpRegistry.RecordConnected(ctx, "working", nil, nil, nil)
	h.mcpRegistry.RecordInitFailure(ctx, "broken", "connection refused")
	h.mcpRegistry.RecordOAuth(ctx, "needsauth", "https://oauth.example/auth")

	got := h.mcpRegistry.PendingSummary(ctx)

	if want := []string{"quiet"}; !slices.Equal(got.Silent, want) {
		t.Errorf("Silent = %v, want %v: an enabled server that reported nothing is nameable only from the census", got.Silent, want)
	}
	if want := []string{"needsauth"}; !slices.Equal(got.AwaitingAuth, want) {
		t.Errorf("AwaitingAuth = %v, want %v", got.AwaitingAuth, want)
	}
	if len(got.Failed) != 1 {
		t.Fatalf("Failed = %v, want one entry", got.Failed)
	}
	if !strings.Contains(got.Failed[0], "broken") || !strings.Contains(got.Failed[0], "connection refused") {
		t.Errorf("Failed[0] = %q, want the server name and the upstream error; the error is the whole diagnostic", got.Failed[0])
	}
	for _, bucket := range [][]string{got.Silent, got.Failed, got.AwaitingAuth} {
		if slices.ContainsFunc(bucket, func(s string) bool { return strings.Contains(s, "working") }) {
			t.Errorf("a connected server appears in %v; a readiness timeout must not name the servers that answered", bucket)
		}
	}
}

// The bounds are the method's, not the caller's, because the server list is
// backend-controlled: a Power's servers reach this registry through KAS's own
// config file, so an unbounded list would push the attributes an operator needs
// off the end of a log record.
//
// Both halves are asserted together: the cap keeps mcpSummaryNameCap names, and
// the marker keeps the total honest. Truncating silently would report a fleet of
// nine as a fleet of eight.
func TestMCPRegistry_PendingSummaryBoundsEachBucket(t *testing.T) {
	const extra = 3
	cfg := &fakeMCPConfig{enabled: map[string]struct{}{}}
	for i := range mcpSummaryNameCap + extra {
		cfg.enabled[fmt.Sprintf("server-%02d", i)] = struct{}{}
	}
	cfg.configured = cfg.copyOf(cfg.enabled)
	cfg.all = cfg.copyOf(cfg.enabled)
	h := newHubWithMCPConfig(cfg)

	got := h.mcpRegistry.PendingSummary(t.Context())

	if len(got.Silent) != mcpSummaryNameCap+1 {
		t.Fatalf("Silent has %d entries, want %d names plus one marker", len(got.Silent), mcpSummaryNameCap)
	}
	if marker := got.Silent[mcpSummaryNameCap]; marker != fmt.Sprintf("+%d more", extra) {
		t.Errorf("last entry = %q, want a +%d more marker", marker, extra)
	}
	// Sorted, so two reads of identical state produce the identical line. An
	// unsorted bucket comes off a map iteration and reorders itself, which reads
	// as two different situations.
	if !slices.IsSorted(got.Silent[:mcpSummaryNameCap]) {
		t.Errorf("Silent = %v, want the names sorted", got.Silent)
	}
}

// Every name and every upstream error goes through the log-safety helper, because
// both are text this process did not write and the summary's only consumer is a
// log record. A raw newline there forges a line, and a C1 introducer writes
// terminal escapes into whatever reads the log.
func TestMCPRegistry_PendingSummarySanitizesBackendText(t *testing.T) {
	cfg := &fakeMCPConfig{enabled: map[string]struct{}{"ok\nlevel=ERROR msg=forged": {}}}
	cfg.configured = cfg.copyOf(cfg.enabled)
	cfg.all = cfg.copyOf(cfg.enabled)
	h := newHubWithMCPConfig(cfg)
	ctx := t.Context()
	h.mcpRegistry.RecordInitFailure(ctx, "broken", "boom\nlevel=ERROR msg=forged")

	got := h.mcpRegistry.PendingSummary(ctx)

	for _, bucket := range [][]string{got.Silent, got.Failed} {
		for _, entry := range bucket {
			if strings.ContainsAny(entry, "\n\r") {
				t.Errorf("entry %q carries a raw newline; a backend-controlled string can forge a log record", entry)
			}
		}
	}
}

// A registry with no name census answers empty rather than guessing. That is the
// build a test hub has, and it is also the state a composition with no MCP config
// leaves: with nothing to compare the reported set against, "which enabled server
// is silent" has no answer, and inventing one would name every server the agent
// reached through a Power as missing.
func TestMCPRegistry_PendingSummaryWithNoCensusNamesNoSilentServers(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	ctx := t.Context()
	h.mcpRegistry.RecordInitFailure(ctx, "broken", "connection refused")

	got := h.mcpRegistry.PendingSummary(ctx)

	if len(got.Silent) != 0 {
		t.Errorf("Silent = %v, want empty: there is no census to subtract the reported set from", got.Silent)
	}
	if len(got.Failed) != 1 {
		t.Errorf("Failed = %v, want the reported failure: a missing census must not suppress what IS known", got.Failed)
	}
}
