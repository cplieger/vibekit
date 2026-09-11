package agent

import (
	"context"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// newWithholdHub is a runtime whose push service HAS a subscriber, so a withheld
// notification is the only thing that can keep the channel empty.
func newWithholdHub(t *testing.T) (*Runtime, *runOutcomePush) {
	t.Helper()
	cs := newFakeChatStore()
	fp := newRunOutcomePush()
	h := New(context.Background(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
	cs.Bus = h
	h.mcpRegistry.SignalReady()
	t.Cleanup(func() { shutdownHub(t, h) })
	return h, fp
}

// seedRunLease records a lease exactly as grantLease would for an agent's own run:
// bounded (so it is the ordinary executing case) and parented on chatID.
func seedRunLease(t *testing.T, h *Runtime, workflowID, chatID string) {
	t.Helper()
	l := runlease.Lease{
		StartedAt:  time.Now(),
		Deadline:   time.Now().Add(time.Hour),
		WorkflowID: workflowID,
		ChatID:     chatID,
		Recipe:     "app-review",
		Origin:     runlease.OriginAgent,
	}
	if err := h.runs.leaseStore().Put(t.Context(), &l); err != nil {
		t.Fatalf("seed lease %s: %v", workflowID, err)
	}
}

// awaitNoPush is the withheld assertion. A push fans out on the lifecycle's own
// goroutine, so an immediate empty read would pass whether or not the notification
// was withheld — the window is what makes this test able to fail.
func awaitNoPush(t *testing.T, fp *runOutcomePush, why string) {
	t.Helper()
	select {
	case got := <-fp.sent:
		t.Errorf("push sent %q (kind %q); %s", got.body, got.kind, why)
	case <-time.After(300 * time.Millisecond):
	}
}

// THE REPORTED DEFECT, on the server's side. `run_workflow` returns as soon as the run
// is created, so the launching turn concludes cleanly while the run carries on — and
// for a reader who is not looking at the page this push is the only channel, so
// sending it says the work is done when it is not.
//
// The four cases are the whole severity partition plus the run's own state, because
// the withhold sits AHEAD of that switch: a clean turn and a broken one are the same
// class of claim while a run is live, and a cancel earns no push either way.
func TestPushTurnOutcome_WithheldWhileALaunchedRunIsLive(t *testing.T) {
	tests := map[string]struct {
		stop     vibekit.StopReason
		liveRun  bool
		wantPush bool
		why      string
	}{
		"a clean turn with a live run is withheld": {
			stop:     vibekit.StopReasonEndTurn,
			liveRun:  true,
			wantPush: false,
			why:      "the turn ended; the work the turn started has not",
		},
		"a BROKEN turn with a live run is withheld too": {
			stop:     vibekit.StopReasonError,
			liveRun:  true,
			wantPush: false,
			why: "the withhold leads the severity switch on purpose: a failed turn that " +
				"launched a still-running run makes the same false claim",
		},
		"a clean turn with no live run pushes": {
			stop:     vibekit.StopReasonEndTurn,
			liveRun:  false,
			wantPush: true,
			why:      "the control: nothing outstanding, so the ordinary notification is correct",
		},
		"a broken turn with no live run pushes": {
			stop:     vibekit.StopReasonError,
			liveRun:  false,
			wantPush: true,
			why:      "the other control, so a fix that silenced one severity cannot pass",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h, fp := newWithholdHub(t)
			if tc.liveRun {
				seedRunLease(t, h, "wf_1", "c1")
			}

			h.coord.pushTurnOutcome(t.Context(), "c1", vibekit.ConcludeStopReason(tc.stop), "")

			if !tc.wantPush {
				awaitNoPush(t, fp, tc.why)
				return
			}
			select {
			case got := <-fp.sent:
				if got.kind != vibekit.PushKindAgentFinished {
					t.Errorf("push kind = %q, want %q (%s)",
						got.kind, vibekit.PushKindAgentFinished, tc.why)
				}
			case <-time.After(2 * time.Second):
				t.Errorf("no push sent; %s", tc.why)
			}
		})
	}
}

// A run ANOTHER chat launched must not silence this chat's notification: the whole
// value of keying on the lease's ChatID is that one busy conversation does not mute
// the rest of the workspace.
func TestPushTurnOutcome_AnotherChatsRunDoesNotWithhold(t *testing.T) {
	h, fp := newWithholdHub(t)
	seedRunLease(t, h, "wf_1", "c2")

	h.coord.pushTurnOutcome(t.Context(), "c1", vibekit.ConcludeStopReason(vibekit.StopReasonEndTurn), "")

	select {
	case got := <-fp.sent:
		if got.subject.ChatID != "c1" {
			t.Errorf("push subject chat = %q, want c1", got.subject.ChatID)
		}
	case <-time.After(2 * time.Second):
		t.Error("c1's notification was withheld for a run c2 launched")
	}
}

// A PARENTLESS run — a manual or scheduled launch — has no chat, so its lease may
// not withhold anyone's notification. Its own outcome travels on `run_outcome`.
func TestPushTurnOutcome_AParentlessRunDoesNotWithhold(t *testing.T) {
	h, fp := newWithholdHub(t)
	seedRunLease(t, h, "wf_1", "")

	h.coord.pushTurnOutcome(t.Context(), "c1", vibekit.ConcludeStopReason(vibekit.StopReasonEndTurn), "")

	select {
	case <-fp.sent:
	case <-time.After(2 * time.Second):
		t.Error("a chat's notification was withheld for a run no chat launched")
	}
}

// A nil chatHasLiveRun is the documented pre-fix behaviour, which is what makes a
// BridgeCoordinator built without the runtime wiring safe to reason about: it asks
// nothing and withholds nothing, rather than silently claiming nothing is outstanding
// and reading as a working withhold.
func TestPushTurnOutcome_NilPredicateWithholdsNothing(t *testing.T) {
	h, fp := newWithholdHub(t)
	seedRunLease(t, h, "wf_1", "c1")
	h.coord.chatHasLiveRun = nil

	h.coord.pushTurnOutcome(t.Context(), "c1", vibekit.ConcludeStopReason(vibekit.StopReasonEndTurn), "")

	select {
	case <-fp.sent:
	case <-time.After(2 * time.Second):
		t.Error("an unwired coordinator withheld a notification; nil must mean nothing outstanding")
	}
}

// A cancel is what the reader asked for, so it earns no notification whether or not a
// run is live. Asserted from both sides so the withhold cannot be credited for the
// silence a STOPPED severity already produces.
func TestPushTurnOutcome_ACancelPushesNothingEitherWay(t *testing.T) {
	for name, live := range map[string]bool{"with a live run": true, "with no live run": false} {
		t.Run(name, func(t *testing.T) {
			h, fp := newWithholdHub(t)
			if live {
				seedRunLease(t, h, "wf_1", "c1")
			}

			h.coord.pushTurnOutcome(t.Context(), "c1",
				vibekit.ConcludeStopReason(vibekit.StopReasonCancelled), "")

			awaitNoPush(t, fp, "a cancel is the reader's own gesture, so no severity arm notifies")
		})
	}
}
