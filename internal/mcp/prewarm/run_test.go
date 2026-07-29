package prewarm

// Coverage for Runner.Run orchestration + the NewRunner constructor.
// `installOne` is not exercised — it execs real `npm` which is out of
// scope for unit tests (testing.md: skip hard I/O, configurable-I/O
// path uses the in-flight dedup seam instead).
//
// These live in-package so the in-flight set (p.mu / p.running) is
// reachable directly; the Store side of the seam — that
// mcp.Store.EnabledServers reports exactly the enabled servers as
// ServerInfo — is pinned by TestEnabledServers_FeedsPrewarmLister in
// package mcp.

import (
	"context"
	"testing"
)

// fakeLister is a ServerLister that returns a fixed set, standing in
// for mcp.Store (importing it here would be an import cycle).
type fakeLister struct{ servers []ServerInfo }

func (f fakeLister) EnabledServers(context.Context) []ServerInfo { return f.servers }

// npxServer is the canonical prewarm candidate: enabled, stdio, npx.
func npxServer() ServerInfo {
	return ServerInfo{
		Transport: "stdio", Command: "npx",
		Args: []string{"-y", "@scope/pkg"}, Enabled: true, Prewarm: true,
	}
}

// TestRun_DedupsAgainstInFlight verifies the dedup branch: pre-seeding
// the in-flight set means Run returns without spawning a goroutine.
// If dedup ever broke, this test would flake by occasionally seeing
// running[pkg] disappear (installOne's defer would fire).
func TestRun_DedupsAgainstInFlight(t *testing.T) {
	p := NewRunner(context.Background(), fakeLister{servers: []ServerInfo{npxServer()}})

	// Pre-seed the in-flight set so Run's dedup branch returns without
	// ever spawning a goroutine.
	p.mu.Lock()
	p.running["@scope/pkg"] = struct{}{}
	p.mu.Unlock()

	p.Run(context.Background())

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.running["@scope/pkg"]; !ok {
		t.Error("in-flight entry was removed by Run; dedup branch failed")
	}
	if len(p.running) != 1 {
		t.Errorf("running has %d entries after dedup, want 1", len(p.running))
	}
}

// TestRun_IgnoresServersWithoutNpxPackage: disabled servers, non-npx
// commands, and HTTP-transport servers never schedule installs.
func TestRun_IgnoresServersWithoutNpxPackage(t *testing.T) {
	p := NewRunner(context.Background(), fakeLister{servers: []ServerInfo{
		// Disabled — ExtractNpxPackage returns "" (the store also filters
		// these out upstream; both gates are load-bearing).
		{
			Transport: "stdio", Command: "npx",
			Args: []string{"-y", "@off/pkg"}, Enabled: false, Prewarm: true,
		},
		// Not npx — ExtractNpxPackage returns "".
		{
			Transport: "stdio", Command: "node",
			Args: []string{"server.js"}, Enabled: true, Prewarm: true,
		},
		// HTTP transport — ExtractNpxPackage returns "".
		{
			Transport: "http", Command: "", Enabled: true, Prewarm: true,
		},
	}})
	// Seed disabled so Run returns without probing exec.LookPath("npm")
	// — the test machine may or may not have npm. We only want to
	// exercise the enumeration + ExtractNpxPackage gating path.
	p.Disabled.Store(true)

	p.Run(context.Background())

	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.running) != 0 {
		t.Errorf("Run scheduled installs for non-npx servers: %v", p.running)
	}
}

// TestNewRunner_initialisesEmpty: non-nil map invariant (a nil running
// map would panic on the first write).
func TestNewRunner_initialisesEmpty(t *testing.T) {
	lister := fakeLister{}

	p := NewRunner(context.Background(), lister)

	if p.Lister == nil {
		t.Error("lister not wired through")
	}
	if p.running == nil {
		t.Error("running map is nil (nil map would panic on write)")
	}
	if len(p.running) != 0 {
		t.Errorf("new runner has %d in-flight entries, want 0", len(p.running))
	}
	if p.Disabled.Load() {
		t.Error("new runner starts disabled; expected enabled")
	}
}

// TestRun_DisabledShortCircuits: once npm is missing, subsequent Runs
// return immediately without log spam or enumeration.
func TestRun_DisabledShortCircuits(t *testing.T) {
	p := NewRunner(context.Background(), fakeLister{servers: []ServerInfo{npxServer()}})
	p.Disabled.Store(true)

	p.Run(context.Background())

	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.running) != 0 {
		t.Errorf("disabled runner scheduled installs: %v", p.running)
	}
}

// npm-missing graceful degradation: a Run with no npm on PATH must
// queue nothing, but must NOT permanently latch Disabled — npm
// (runtimes.node) is opt-in and can be installed mid-process via the
// tools UI, after which a later Run should come alive. This covers the
// LookPath-miss branch.
func TestRun_SkipsWhenNpmMissingButDoesNotLatch(t *testing.T) {
	// Empty PATH so exec.LookPath("npm") always fails on any host.
	// t.Setenv restores the prior value at test end.
	t.Setenv("PATH", "")

	p := NewRunner(context.Background(), fakeLister{servers: []ServerInfo{npxServer()}})
	if p.Disabled.Load() {
		t.Fatal("new runner starts disabled; expected enabled")
	}

	p.Run(context.Background())

	p.mu.Lock()
	gotDisabled := p.Disabled.Load()
	gotRunning := len(p.running)
	p.mu.Unlock()
	// Must NOT latch — npm can appear later in the same process.
	if gotDisabled {
		t.Error("Run with no npm latched Disabled; it must re-probe on the next Run")
	}
	if gotRunning != 0 {
		t.Errorf("Run with no npm queued %d installs, want 0", gotRunning)
	}

	// A second Run (still no npm) must again skip cleanly and STILL not
	// latch — proving re-probe semantics rather than one-shot disable.
	p.Run(context.Background())
	p.mu.Lock()
	stillNotLatched := !p.Disabled.Load()
	running2 := len(p.running)
	p.mu.Unlock()
	if !stillNotLatched {
		t.Error("second npm-missing Run latched Disabled; must keep re-probing")
	}
	if running2 != 0 {
		t.Errorf("second Run queued installs: %d", running2)
	}
}
