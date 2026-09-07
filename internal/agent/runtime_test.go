package agent

// Tests for agent.go: top-level agent lifecycle (Shutdown) and the compile-time
// interface-satisfaction check for the shared fakes.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/kirosession"
	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestShutdownCompletesWithoutHanging(t *testing.T) {
	h, _, _ := newTestHub()
	done := make(chan struct{})
	go func() {
		// An unbounded context: the assertion is that Shutdown returns on its
		// OWN, so a budget here would satisfy it by expiring.
		_ = h.Shutdown(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Shutdown hung")
	}
}

// hangingBridge's Call blocks forever until Stop() is invoked. Used to
// exercise the Shutdown ordering fix: Stop must fire before inflight.Wait
// so stuck Calls can unblock via the bridge's own teardown.
type hangingBridge struct {
	*fakeBridge

	released   chan struct{}
	once       sync.Once
	stopCalled atomic.Bool
}

func newHangingBridge() *hangingBridge {
	return &hangingBridge{
		fakeBridge: newFakeBridge(),
		released:   make(chan struct{}),
	}
}

func (b *hangingBridge) Call(_ context.Context, _ string, _ any) (*vibekit.RPCResponse, error) {
	<-b.released
	return &vibekit.RPCResponse{}, nil
}

func (b *hangingBridge) Stop() {
	b.stopCalled.Store(true)
	b.once.Do(func() { close(b.released) })
	b.fakeBridge.Stop()
}

func TestShutdown_StopsBridgesBeforeWaitingOnInflight(t *testing.T) {
	// Reproduces the pre-fix deadlock: an in-flight Call that can only
	// return when the bridge is Stop'd. Shutdown must Stop first,
	// otherwise inflight.Wait blocks forever.
	cs := newFakeChatStore()
	hb := newHangingBridge()
	factory := func() ACPBridge { return hb }
	h := New(t.Context(), "/tmp/work", factory, cs)
	cs.Bus = h

	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	// Register the bridge directly so we don't have to drive a full
	// cmdPrompt flow; we're testing Shutdown ordering, not prompt
	// mechanics.
	sb := &sharedBridge{bridge: hb}
	h.bridge.mgr.mu.Lock()
	h.bridge.mgr.bridges["c1"] = sb
	h.bridge.mgr.mu.Unlock()

	// Simulate an in-flight prompt holding sb.mu and awaiting the Call.
	h.lifecycle.inflight.Add(1)
	callDone := make(chan struct{})
	go func() {
		defer h.lifecycle.inflight.Done()
		_, _ = hb.Call(t.Context(), "session/prompt", nil)
		close(callDone)
	}()

	done := make(chan struct{})
	go func() {
		// An unbounded context: the assertion is that Shutdown returns on its
		// OWN, so a budget here would satisfy it by expiring.
		_ = h.Shutdown(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown deadlocked waiting for in-flight Call")
	}
	select {
	case <-callDone:
	case <-time.After(1 * time.Second):
		t.Error("Call never returned despite Stop")
	}
	if !hb.stopCalled.Load() {
		t.Error("Stop was not called on the bridge")
	}
}

// Compile-time check that the shared fakes still satisfy the public
// interfaces they impersonate. Breaks the build, not just the test suite,
// if the interfaces drift.
func TestInterfaceSatisfaction(_ *testing.T) {
	var _ chatRecords = (*fakeChatStore)(nil)
	var _ ACPBridge = (*fakeBridge)(nil)
}

// TestShutdown_BoundsAWedgedHandler is what the context arm is for: a prompt
// handler that never decrements inflight must not take the shutdown with it.
// Unbounded, this wait blocked forever — and because webhttp.Run calls the
// pre-drain hook Shutdown runs in SYNCHRONOUSLY, that also meant srv.Shutdown
// never ran and the process died to SIGKILL with the HTTP drain unspent.
func TestShutdown_BoundsAWedgedHandler(t *testing.T) {
	h, _, _ := newTestHub()

	// A handler that will never come back: the wedged prompt, minus kiro-cli.
	h.lifecycle.inflight.Add(1)
	t.Cleanup(h.lifecycle.inflight.Done)

	const budget = 150 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	start := time.Now()
	err := h.Shutdown(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Shutdown reported success while a handler was still in flight")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v does not carry context.DeadlineExceeded", err)
	}
	if !strings.Contains(err.Error(), "in-flight handlers") {
		t.Errorf("error %q does not name the wait that expired", err)
	}
	if elapsed > 20*budget {
		t.Errorf("Shutdown took %v on a %v budget", elapsed, budget)
	}
}

// TestShutdown_WaitsForARunningSweep holds sweepSessionsLoop inside its boot
// sweep and asserts Shutdown notices. Signalling alone cannot: closing
// lifecycle.done tells the loop to leave and says nothing about whether it has.
func TestShutdown_WaitsForARunningSweep(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	refs := func(context.Context) (map[string]struct{}, bool) {
		once.Do(func() { close(entered) })
		<-release
		return nil, false
	}
	cs := newFakeChatStore()
	h := New(context.Background(), t.TempDir(),
		func() ACPBridge { return newFakeBridge() }, cs,
		WithSessionReaper(kirosession.New(t.TempDir(), testReaperWorkDir), refs))
	cs.Bus = h

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the orphan sweep never ran, so the fixture is holding nothing")
	}

	const budget = 150 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	if err := h.Shutdown(ctx); err == nil {
		t.Error("Shutdown reported success while the orphan sweep was still running")
	} else if !strings.Contains(err.Error(), "background loops") {
		t.Errorf("error %q does not name the background-loop wait", err)
	}

	// Released, the loop returns and a second pass joins it cleanly — the
	// control that keeps the assertion above from passing on a bound that can
	// never succeed.
	close(release)
	shutdownHub(t, h)
}

// TestSweepSessionsLoop_WaitsForTheListenerToBind is the destructive sweep's ownership
// precondition. The reaper's root comes from $KIRO_HOME and never from the config dir, so a
// boot pointed at the wrong one reaps the REAL instance's KAS session trees — and the sweep
// starts inside agent.New, long before App.Run binds anything.
//
// The held-gate half needs no negative time window: Shutdown closes lifecycle.done and then
// WAITS on the loop group, and an ungated loop calls refs before it could reach a select, so
// zero calls after Shutdown returns can only mean the gate held.
func TestSweepSessionsLoop_WaitsForTheListenerToBind(t *testing.T) {
	newGatedHub := func(t *testing.T, gate <-chan struct{}, refs func(context.Context) (map[string]struct{}, bool)) *Runtime {
		t.Helper()
		cs := newFakeChatStore()
		h := New(context.Background(), t.TempDir(),
			func() ACPBridge { return newFakeBridge() }, cs,
			WithSessionReaper(kirosession.New(t.TempDir(), testReaperWorkDir), refs),
			WithSessionSweepGate(gate))
		cs.Bus = h
		return h
	}
	// Non-empty and complete, so the sweep reaches Reaper.Sweep rather than
	// declining on its own keep-list guard.
	keepList := func(context.Context) (map[string]struct{}, bool) {
		return map[string]struct{}{"sess_live": {}}, true
	}

	t.Run("a gate that never closes reaps nothing", func(t *testing.T) {
		var calls atomic.Int32
		h := newGatedHub(t, make(chan struct{}), func(ctx context.Context) (map[string]struct{}, bool) {
			calls.Add(1)
			return keepList(ctx)
		})

		shutdownHub(t, h)

		if got := calls.Load(); got != 0 {
			t.Errorf("the keep-list was read %d time(s) with the listener unbound, want 0: "+
				"a boot that cannot bind is not the owner of its config dir and must not "+
				"reap another live process's session trees", got)
		}
	})

	t.Run("a closed gate releases the sweep", func(t *testing.T) {
		gate := make(chan struct{})
		asked := make(chan struct{}, 1)
		h := newGatedHub(t, gate, func(ctx context.Context) (map[string]struct{}, bool) {
			select {
			case asked <- struct{}{}:
			default:
			}
			return keepList(ctx)
		})
		t.Cleanup(func() { shutdownHub(t, h) })

		close(gate)

		select {
		case <-asked:
		case <-time.After(5 * time.Second):
			t.Fatal("the sweep never ran after the listener bound, so the gate is a " +
				"dead end and nothing reclaims KAS session state at all")
		}
	})
}

// TestShutdown_JoinsTheTickerLoops covers the two loops no fixture can hold
// inside their work, both of which sit in a ticker select until lifecycle.done
// closes. The reaper is deliberately unwired, so sweepSessionsLoop returns at
// once and the group's occupants are exactly those two: a group that is empty
// while the runtime runs means New never registered them.
func TestShutdown_JoinsTheTickerLoops(t *testing.T) {
	h, _, _ := newTestHub()

	waited := make(chan struct{})
	go func() {
		defer close(waited)
		h.lifecycle.loops.Wait()
	}()
	select {
	case <-waited:
		t.Fatal("the loop group is empty while the runtime runs: New's background loops are unjoinable")
	case <-time.After(100 * time.Millisecond):
	}

	shutdownHub(t, h)

	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Error("Shutdown returned with a background loop still running")
	}
}

// TestShutdown_WaitsForARunningMCPNotifier holds the debounced notifier inside its callback
// — in production the environment.md generator, a .kiro walk plus an atomic write — and
// asserts Shutdown notices. Nothing else catches an unjoined loop here: it exits on
// lifecycle.done correctly, so no test hangs and no linter reports it, and the only symptom
// is that Shutdown's wait is silent about one of the three loops it claims to cover.
func TestShutdown_WaitsForARunningMCPNotifier(t *testing.T) {
	h := newHubWithMCPConfig(nil)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.SetMCPOnChange(func() {
		once.Do(func() { close(entered) })
		<-release
	})
	// Any mutation signals the notifier; it debounces 100ms before calling back.
	h.mcpRegistry.RecordConnected(t.Context(), "a", nil, nil, nil)

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the notifier callback never ran, so the fixture is holding nothing")
	}

	const budget = 150 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	if err := h.Shutdown(ctx); err == nil {
		t.Error("Shutdown reported success while the MCP notifier was still in its callback")
	} else if !strings.Contains(err.Error(), "background loops") {
		t.Errorf("error %q does not name the background-loop wait", err)
	}

	// The control that keeps the assertion above off a bound that can never be
	// met: released, the loop returns and a second pass joins it cleanly.
	close(release)
	shutdownHub(t, h)
}

// lifetimeWatchingBridge records what the runtime's lifetime looked like at the
// instant its Stop landed, which is the one observation the ordering question
// turns on.
type lifetimeWatchingBridge struct {
	*fakeBridge

	lifetime  context.Context
	stopErr   error
	sawLive   atomic.Bool
	stopCount atomic.Int32
}

func (b *lifetimeWatchingBridge) Stop() {
	b.stopErr = b.lifetime.Err()
	b.sawLive.Store(b.stopErr == nil)
	b.stopCount.Add(1)
	b.fakeBridge.Stop()
}

// TestShutdown_CancelsTheLifetimeAfterDrainingBridges closes the bridge-death
// door for the WHOLE shutdown rather than almost all of it.
//
// drain() removes every bridge from the map before any Stop, so the death closer
// is skipped for each one this teardown kills. Cancelling the lifetime FIRST left
// a window between that cancel and the drain where a bridge dying on its own
// still reached that closer, on a bare goroutine holding a dead context.
// close(lifecycle.done) stays at step 0: the tickers select on it.
func TestShutdown_CancelsTheLifetimeAfterDrainingBridges(t *testing.T) {
	cs := newFakeChatStore()
	watcher := &lifetimeWatchingBridge{fakeBridge: newFakeBridge()}
	h := New(t.Context(), t.TempDir(), func() ACPBridge { return watcher }, cs)
	watcher.lifetime = h.lifecycle.shutdownCtx
	cs.Bus = h

	sb := &sharedBridge{bridge: watcher}
	h.bridge.mgr.mu.Lock()
	h.bridge.mgr.bridges["c1"] = sb
	h.bridge.mgr.mu.Unlock()

	shutdownHub(t, h)

	if watcher.stopCount.Load() == 0 {
		t.Fatal("the bridge was never stopped, so the ordering was not exercised")
	}
	if !watcher.sawLive.Load() {
		t.Errorf("the lifetime was already %v when the bridge was stopped; a bridge dying in that "+
			"window reaches the death closer with a dead context on an untracked goroutine",
			watcher.stopErr)
	}
	// And the cancel still happens: the reorder moves it, it does not drop it.
	if h.lifecycle.shutdownCtx.Err() == nil {
		t.Error("the lifetime is still live after Shutdown returned")
	}
}
