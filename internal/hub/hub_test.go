package hub

// Tests for hub.go: top-level hub lifecycle (Shutdown) and the compile-time
// interface-satisfaction check for the shared fakes.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

func TestShutdownCompletesWithoutHanging(t *testing.T) {
	h, _, _ := newTestHub()
	done := make(chan struct{})
	go func() {
		h.Shutdown()
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

func (b *hangingBridge) Call(_ context.Context, _ string, _ any) (*api.RPCResponse, error) {
	<-b.released
	return &api.RPCResponse{}, nil
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
	factory := func() api.ACPBridge { return hb }
	h := New("/tmp/work", factory, cs)
	cs.Bus = h

	_ = cs.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

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
		h.Shutdown()
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
	var _ api.ChatStore = (*fakeChatStore)(nil)
	var _ api.ACPBridge = (*fakeBridge)(nil)
}
