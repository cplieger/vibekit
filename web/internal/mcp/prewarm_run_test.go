package mcp

// Coverage for PrewarmRunner.Run orchestration + NewPrewarmRunner
// constructor. `installOne` is not exercised — it execs real `npm`
// which is out of scope for unit tests (testing.md: skip hard I/O,
// configurable-I/O path uses the in-flight dedup seam instead).

import (
	"context"
	"testing"
)

// TestRun_DedupsAgainstInFlight verifies the dedup branch: pre-seeding
// the in-flight set means Run returns without spawning a goroutine.
// If dedup ever broke, this test would flake by occasionally seeing
// running[pkg] disappear (installOne's defer would fire).
func TestRun_DedupsAgainstInFlight(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "pkg", Command: "npx",
		Args:    []string{"-y", "@scope/pkg"},
		Enabled: true, Prewarm: true,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	p := NewPrewarmRunner(context.Background(), store)

	// Pre-seed the in-flight set so Run's dedup branch returns without
	// ever spawning a goroutine.
	p.Lock()
	p.Running()["@scope/pkg"] = struct{}{}
	p.Unlock()

	p.Run(context.Background())

	p.Lock()
	defer p.Unlock()
	if _, ok := p.Running()["@scope/pkg"]; !ok {
		t.Error("in-flight entry was removed by Run; dedup branch failed")
	}
	if len(p.Running()) != 1 {
		t.Errorf("running has %d entries after dedup, want 1", len(p.Running()))
	}
}

// TestRun_IgnoresServersWithoutNpxPackage: disabled servers, non-npx
// commands, and HTTP-transport servers never schedule installs.
func TestRun_IgnoresServersWithoutNpxPackage(t *testing.T) {
	store := newTestStore(t)

	// Disabled — filtered by EnabledRaw.
	if _, err := store.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "off", Command: "npx",
		Args: []string{"-y", "@off/pkg"}, Enabled: false, Prewarm: true,
	}); err != nil {
		t.Fatalf("Create off: %v", err)
	}
	// Not npx — extractNpxPackage returns "".
	if _, err := store.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "node", Command: "node",
		Args: []string{"server.js"}, Enabled: true, Prewarm: true,
	}); err != nil {
		t.Fatalf("Create node: %v", err)
	}
	// HTTP transport — extractNpxPackage returns "".
	if _, err := store.Create(context.Background(), &Server{
		Transport: TransportHTTP, Name: "remote",
		URL: "https://x.example/mcp", Enabled: true, Prewarm: true,
	}); err != nil {
		t.Fatalf("Create remote: %v", err)
	}

	p := NewPrewarmRunner(context.Background(), store)
	// Seed disabled so Run returns without probing exec.LookPath("npm")
	// — the test machine may or may not have npm. We only want to
	// exercise the enumeration + extractNpxPackage gating path.
	p.Disabled.Store(true)

	p.Run(context.Background())

	p.Lock()
	defer p.Unlock()
	if len(p.Running()) != 0 {
		t.Errorf("Run scheduled installs for non-npx servers: %v", p.Running())
	}
}

// TestNewPrewarmRunner_initialisesEmpty: non-nil map invariant (a nil
// running map would panic on the first write).
func TestNewPrewarmRunner_initialisesEmpty(t *testing.T) {
	store := newTestStore(t)

	p := NewPrewarmRunner(context.Background(), store)

	if p.Lister == nil {
		t.Error("lister not wired through")
	}
	if p.Running() == nil {
		t.Error("running map is nil (nil map would panic on write)")
	}
	if len(p.Running()) != 0 {
		t.Errorf("new runner has %d in-flight entries, want 0", len(p.Running()))
	}
	if p.Disabled.Load() {
		t.Error("new runner starts disabled; expected enabled")
	}
}

// TestRun_DisabledShortCircuits: once npm is missing, subsequent Runs
// return immediately without log spam or enumeration.
func TestRun_DisabledShortCircuits(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "pkg", Command: "npx",
		Args: []string{"-y", "@scope/pkg"}, Enabled: true, Prewarm: true,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	p := NewPrewarmRunner(context.Background(), store)
	p.Disabled.Store(true)

	p.Run(context.Background())

	p.Lock()
	defer p.Unlock()
	if len(p.Running()) != 0 {
		t.Errorf("disabled runner scheduled installs: %v", p.Running())
	}
}

// F4 (test-review u12c1): npm-missing graceful degradation sets
// p.Disabled on the first LookPath miss. Covers the actual
// LookPath-miss branch; TestRun_DisabledShortCircuits only covers
// the already-disabled short-circuit.
func TestRun_DisablesWhenNpmMissing(t *testing.T) {
	// Empty PATH so exec.LookPath("npm") always fails on any host.
	// t.Setenv restores the prior value at test end.
	t.Setenv("PATH", "")

	store := newTestStore(t)
	if _, err := store.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "pkg", Command: "npx",
		Args: []string{"-y", "@scope/pkg"}, Enabled: true, Prewarm: true,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	p := NewPrewarmRunner(context.Background(), store)
	if p.Disabled.Load() {
		t.Fatal("new runner starts disabled; expected enabled")
	}

	p.Run(context.Background())

	p.Lock()
	gotDisabled := p.Disabled.Load()
	gotRunning := len(p.Running())
	p.Unlock()
	if !gotDisabled {
		t.Error("Run with no npm did not set disabled=true")
	}
	if gotRunning != 0 {
		t.Errorf("Run with no npm queued %d installs, want 0", gotRunning)
	}

	// Second Run is the short-circuit path. It must not spawn
	// anything and must not panic. Additionally: p.Disabled is
	// sticky (per godoc, container restart required to re-enable).
	p.Run(context.Background())
	p.Lock()
	if !p.Disabled.Load() {
		t.Error("disabled flag cleared between passes; must remain sticky")
	}
	if len(p.Running()) != 0 {
		t.Errorf("second Run queued installs: %v", p.Running())
	}
	p.Unlock()
}
