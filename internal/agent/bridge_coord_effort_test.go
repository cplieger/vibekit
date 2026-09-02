// Tests for the reactive reasoning-effort repair (BridgeCoordinator.healEffort).
//
// Its own file rather than an addition to bridge_coord_test.go, which is already
// past the length at which a test file is split by behavior. The prompt-time
// repairEffort and effortFor tests still live there.

package agent

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// configOptionFrame builds a config_option_update carrying one effortLevel
// option, which is the frame KAS sends when the session's tier changes.
//
// sessionID and workflow are the two facts ClassifyFrame reads: an empty
// sessionID is the chat's own frame, and a non-empty one plus a workflow marker
// is a run STEP's.
func configOptionFrame(t *testing.T, running, sessionID string, workflow bool) *vibekit.RPCResponse {
	t.Helper()
	update := map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateConfigOption),
		"configOptions": []any{
			map[string]any{
				"id":           "effortLevel",
				"currentValue": running,
				"options": []any{
					map[string]any{"value": "high", "name": "high"},
					map[string]any{"value": "max", "name": "max"},
				},
			},
		},
	}
	if workflow {
		update["_meta"] = map[string]any{"kiro": map[string]any{
			"workflow": map[string]any{"workflowId": "wf1", "nodeId": "n1", "type": "step"},
		}}
	}
	params := mustJSON(t, map[string]any{
		"sessionId": sessionID,
		"update":    mustJSON(t, update),
	})
	return &vibekit.RPCResponse{Method: vibekit.MethodSessionUpdate, Params: params}
}

// healEffortFixture wires a chat that chose `chose` plus an open bridge.
func healEffortFixture(t *testing.T, chose string) (*Runtime, *fakeBridge) {
	t.Helper()
	h, cs, br := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Effort = chose
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	return h, br
}

// settleHeals joins the repair goroutine, so an assertion that nothing was
// applied is a fact rather than a race the test happened to win.
//
// The repair runs on its own goroutine — the frame arrives on the forward
// goroutine and a bridge Call issued inline would block the drain the reply
// arrives on — and it is registered on the runtime's in-flight group, so the
// runtime's own shutdown is the join point. A sleep here would let every negative
// case below pass for the wrong reason.
func settleHeals(t *testing.T, h *Runtime) {
	t.Helper()
	shutdownHub(t, h)
}

// A config_option_update reporting a level the chat did not choose is repaired.
//
// This is the hole repairEffort cannot reach: it runs on OpenBridge's
// already-open path, so the turn that SPAWNS the bridge — the one KAS's own
// first-prompt model pin moves the level during — never gets it. A chat whose
// whole life is a single turn kept the wrong level, which is what was measured on
// the live volume.
func TestHealEffort_RepairsALevelTheSessionMovedOnItsOwn(t *testing.T) {
	h, br := healEffortFixture(t, "max")

	h.handleSessionUpdate(t.Context(), "c1", configOptionFrame(t, "high", "", false))
	settleHeals(t, h)

	if got := br.lastEffort(); got != "max" {
		t.Errorf("effort applied = %q, want %q", got, "max")
	}
	if got := br.lastObservedEffort(); got != "high" {
		t.Errorf("observed level = %q, want %q — the bridge must be told what the session reports, or its differs-only cache skips the repair", got, "high")
	}
}

// The repair is LATCHED once per bridge: it asserts a level, KAS answers with
// another config_option_update, and an unbounded reactive repair is a loop. Past
// the latch the prompt-time repairEffort owns it.
//
// The latch is claimed SYNCHRONOUSLY, before the goroutine starts, so spending it
// in the fixture is what makes "the second frame did nothing" provable rather
// than merely unobserved.
func TestHealEffort_RepairsOncePerBridge(t *testing.T) {
	h, br := healEffortFixture(t, "max")
	sb := h.coord.Bridge("c1")
	if !sb.claimEffortHeal() {
		t.Fatal("fixture: the latch was already spent")
	}

	h.handleSessionUpdate(t.Context(), "c1", configOptionFrame(t, "high", "", false))
	settleHeals(t, h)

	if got := br.lastEffort(); got != "" {
		t.Errorf("applied %q on a spent latch; the repair must run once per bridge", got)
	}
	if got := br.lastObservedEffort(); got != "high" {
		t.Errorf("observed level = %q, want %q — the OBSERVATION is not latched, only the repair", got, "high")
	}
}

// Nothing is repaired when there is nothing to repair, and each of the three
// "nothing" cases means something different.
func TestHealEffort_SendsNothingWhenThereIsNothingToRepair(t *testing.T) {
	tests := map[string]struct {
		chose   string
		running string
	}{
		"the session already runs at the chosen level": {chose: "max", running: "max"},
		"the chat chose nothing and has no seed":       {chose: "", running: "high"},
		"the session reports no level at all":          {chose: "max", running: ""},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			h, br := healEffortFixture(t, test.chose)

			h.handleSessionUpdate(t.Context(), "c1", configOptionFrame(t, test.running, "", false))
			settleHeals(t, h)

			if got := br.lastEffort(); got != "" {
				t.Errorf("applied %q, want no call", got)
			}
		})
	}
}

// A workflow STEP's frame reports the level THAT session runs at, which says
// nothing about the level this chat chose. Repairing from it would assert the
// chat's tier because a step happened to run at another one.
func TestHealEffort_IgnoresAWorkflowStepsFrame(t *testing.T) {
	h, br := healEffortFixture(t, "max")

	h.handleSessionUpdate(t.Context(), "c1", configOptionFrame(t, "high", "sess-step", true))
	settleHeals(t, h)

	if got := br.lastEffort(); got != "" {
		t.Errorf("applied %q from a step's frame, want no call", got)
	}
	if got := br.lastObservedEffort(); got != "" {
		t.Errorf("observed %q from a step's frame; a step's level is not this session's", got)
	}
}
