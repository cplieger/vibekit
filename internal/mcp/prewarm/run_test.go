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
	"strconv"
	"sync/atomic"
	"testing"
	"time"
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
	p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{npxServer()}})

	// Pre-seed the in-flight set so Run's dedup branch returns without
	// ever spawning a goroutine.
	p.mu.Lock()
	p.running["@scope/pkg"] = struct{}{}
	p.mu.Unlock()

	p.Run(t.Context())

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
	p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{
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

	p.Run(t.Context())

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

	p := NewRunner(t.Context(), lister)

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
	p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{npxServer()}})
	p.Disabled.Store(true)

	p.Run(t.Context())

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

	p := NewRunner(t.Context(), fakeLister{servers: []ServerInfo{npxServer()}})
	if p.Disabled.Load() {
		t.Fatal("new runner starts disabled; expected enabled")
	}

	p.Run(t.Context())

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
	p.Run(t.Context())
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

// A dead context must not acquire an install slot, even with every slot free.
// This is the property the slot CHANNEL could not hold: `select` picks at random
// among ready cases, so a cancelled pass raced the free slot and won it about
// half the time, starting an install for work whose caller had already given up.
// semaphore.Weighted tests the context before its fast path, so the refusal is
// decided rather than drawn.
//
// It is asserted over a batch because a single draw proves nothing about a coin:
// the channel shape passes one attempt half the time, and 64 (2^-64) is where
// "it never installs" stops being luck. queue is called directly rather than
// through Run so the assertion needs no handshake with a goroutine.
func TestQueue_DeadContextNeverTakesASlot(t *testing.T) {
	const attempts = 64

	p := NewRunner(t.Context(), fakeLister{})
	var installs atomic.Int64
	p.OnStatus = func(_ string, state State) {
		if state == Installing {
			installs.Add(1)
		}
	}

	dead, cancel := context.WithCancel(t.Context())
	cancel()

	for i := range attempts {
		pkg := "@scope/pkg" + strconv.Itoa(i)
		if !p.reserve(pkg) {
			t.Fatalf("reserve(%q) = false on a fresh runner", pkg)
		}
		p.queue(dead, pkg)
	}

	if got := installs.Load(); got != 0 {
		t.Errorf("queue started %d installs under a cancelled context, want 0", got)
	}
	// The reservation is handed back, so a later pass with a live context
	// retries the package instead of treating it as permanently in flight.
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.running) != 0 {
		t.Errorf("abandoned waits left %d in-flight reservations, want 0: %v", len(p.running), p.running)
	}
}

// Every slot is given back after an install, so capacity does not leak across
// passes: a runner that has put twice its cap through queue must still be able
// to take the cap at once. This guards the conversion rather than the defect —
// the slot channel released too — and it is the assertion a dropped
// `defer p.sem.Release(1)` fails.
//
// An empty PATH is what keeps installOne out of scope: exec resolves "npm" at
// construction, so the install fails before any process is spawned, and the
// acquire/release pair is all that runs. Each wait carries its own budget so a
// leaked slot reports the leak instead of parking the package's test binary on
// an acquire that can never succeed.
func TestQueue_ReleasesEverySlot(t *testing.T) {
	t.Setenv("PATH", "")

	p := NewRunner(t.Context(), fakeLister{})
	for i := range maxConcurrentInstalls * 2 {
		pkg := "@scope/pkg" + strconv.Itoa(i)
		if !p.reserve(pkg) {
			t.Fatalf("reserve(%q) = false on a fresh runner", pkg)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		p.queue(ctx, pkg)
		cancel()
	}

	if !p.sem.TryAcquire(maxConcurrentInstalls) {
		t.Errorf("the runner cannot take its %d-slot cap after %d installs, so a slot leaked",
			maxConcurrentInstalls, maxConcurrentInstalls*2)
	}
}
