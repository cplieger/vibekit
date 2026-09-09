package agent

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// A bridge whose frame stream ends while it is still REGISTERED died on its own, and
// Forward is the only site that observes every such death. readLoop reaps the paths it
// takes itself, so a bridge object replaced without a teardown reaches here as the last
// chance to reclaim its process tree; an unreaped acp-server keeps KAS's workflow
// ownership lease, which then refuses every later resume for as long as it lives.
func TestForward_ReapsARegisteredBridgeThatDied(t *testing.T) {
	h, _, br := newTestHub()
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})

	// endStream, not Stop: a fixture that stopped the bridge itself would satisfy the
	// assertion without Forward doing anything.
	br.endStream()
	h.coord.Forward("c1", br)

	if !br.isStopped() {
		t.Error("bridge.isStopped() = false, want true (a registered bridge that died must be reaped)")
	}
}

// A bridge removed from the map first was torn down deliberately and has its own
// closer, so Forward must not reap it a second time on that path's behalf.
func TestForward_DoesNotReapAnUnregisteredBridge(t *testing.T) {
	h, _, br := newTestHub()

	br.endStream()
	h.coord.Forward("c1", br)

	if br.isStopped() {
		t.Error("bridge.isStopped() = true, want false (an unregistered exit is a deliberate teardown)")
	}
}

// waiting_on_user claims a person still owes the AGENT an answer, and chatStatusCache
// retains it past turn end for exactly that reason. When the agent dies nobody owes an
// answer, so the claim is false and the amber dot has nothing behind it.
func TestForward_DischargesWaitingOnUserWhenTheAgentDies(t *testing.T) {
	h, _, br := newTestHub()
	h.bus.chatStatus.Merge("c1", vibekit.ChatStatusPayload{
		Status:      vibekit.ChatStatusWaitingOnUser,
		Description: "needs a decision",
	})
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})

	br.endStream()
	h.coord.Forward("c1", br)

	if got := h.bus.chatStatus.Get("c1").Status; got != "" {
		t.Errorf("chat status = %q, want %q (a dead agent cannot still be awaiting an answer)", got, "")
	}
}

// A status the running turn declared belongs to that turn, and ClearAtTurnEnd owns its
// removal. Only the retained waiting_on_user claim is Forward's to discharge.
func TestForward_LeavesANonWaitingStatusAlone(t *testing.T) {
	h, _, br := newTestHub()
	h.bus.chatStatus.Merge("c1", vibekit.ChatStatusPayload{
		Status:      "in_progress",
		Description: "working",
	})
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})

	br.endStream()
	h.coord.Forward("c1", br)

	if got := h.bus.chatStatus.Get("c1").Status; got != "in_progress" {
		t.Errorf("chat status = %q, want %q", got, "in_progress")
	}
}
