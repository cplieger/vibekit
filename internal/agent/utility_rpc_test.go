package agent

import (
	"context"
	"errors"
	"testing"
)

// startedRPCSession builds an already-started session over br, so rawCall's
// acquire is a no-op and a test observes only the error path.
func startedRPCSession(t *testing.T, br *fakeBridge) *utilitySession {
	t.Helper()
	return &utilitySession{shutdownCtx: t.Context(), bridge: br, started: true, gen: 1}
}

func sessionAlive(t *testing.T, us *utilitySession, br *fakeBridge) (started, stopped bool) {
	t.Helper()
	us.mu.Lock()
	started = us.started
	us.mu.Unlock()
	br.mu.Lock()
	stopped = br.stopped
	br.mu.Unlock()
	return started, stopped
}

func TestRawCall_ACallerCancellationLeavesTheSessionRunning(t *testing.T) {
	br := newFakeBridge()
	// What the real bridge returns for a cancelled request: Call selects
	// ctx.Done() and hands back ctx.Err() (bridge_rpc.go).
	br.callErrs = map[string]error{methodKiroConfigTemplate: context.Canceled}
	us := startedRPCSession(t, br)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := us.rawCall(ctx, "config template call", methodKiroConfigTemplate,
		callerParams(nil)); err == nil {
		t.Fatal("rawCall over a cancelled context reported success")
	}

	started, stopped := sessionAlive(t, us, br)
	if !started || stopped {
		t.Errorf("started = %v, bridge stopped = %v; want a live session. A client that "+
			"aborts must not tear down the utility bridge — the spawn runs on the runtime's "+
			"own lifetime, so the session it left behind is alive, and stopping it makes "+
			"every later utility-backed endpoint pay a full KAS respawn",
			started, stopped)
	}
}

func TestRawCall_ATransportFailureStillResetsTheSession(t *testing.T) {
	br := newFakeBridge()
	br.callErrs = map[string]error{methodKiroConfigTemplate: errors.New("kas gone")}
	us := startedRPCSession(t, br)

	if _, err := us.rawCall(t.Context(), "config template call", methodKiroConfigTemplate,
		callerParams(nil)); err == nil {
		t.Fatal("rawCall over a dead bridge reported success")
	}

	started, stopped := sessionAlive(t, us, br)
	if started || !stopped {
		t.Errorf("started = %v, bridge stopped = %v; want the session reset. A bridge that "+
			"failed on a live context may be dead, and the next caller has to get a fresh one",
			started, stopped)
	}
}

func TestRawCall_ADeadlineStillResetsTheSession(t *testing.T) {
	br := newFakeBridge()
	br.callErrs = map[string]error{methodKiroConfigTemplate: context.DeadlineExceeded}
	us := startedRPCSession(t, br)

	ctx, cancel := context.WithTimeout(t.Context(), -1)
	defer cancel()
	if _, err := us.rawCall(ctx, "config template call", methodKiroConfigTemplate,
		callerParams(nil)); err == nil {
		t.Fatal("rawCall over an expired context reported success")
	}

	started, stopped := sessionAlive(t, us, br)
	if started || !stopped {
		t.Errorf("started = %v, bridge stopped = %v; want the session reset. The cancellation "+
			"carve-out is for a caller that walked away, and a session that spent the whole "+
			"budget without answering is the case it must not cover",
			started, stopped)
	}
}
