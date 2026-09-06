package agent

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// WHICH statuses a verb is legal from is pinned by run_affordance_test.go, over the one
// table run_affordance.go holds.

// TestRunVerbsAreWired guards the halves that can silently drift apart: a verb with no
// issuer would 200 without doing anything, a verb with no name would log and error as the
// empty string, and cancel losing its unrestricted status would error on a tab close.
func TestRunVerbsAreWired(t *testing.T) {
	for _, verb := range []runVerb{runVerbCancel, runVerbPause, runVerbResume, runVerbDelete} {
		if verb.name == "" {
			t.Error("a run verb has no name; its 409 and its log line would both be blank")
		}
		if verb.issue == nil {
			t.Errorf("run verb %q has no issuer: the route would answer ok without calling KAS", verb.name)
		}
	}
	// Cancel is the tab-close gesture and must never be the verb that fails (KAS is
	// idempotent on a terminal run); delete is the only way a row leaves History.
	for _, verb := range []runVerb{runVerbCancel, runVerbDelete} {
		if verb.gated {
			t.Errorf("run verb %q is gated; it must reach a run from any status", verb.name)
		}
	}
}

// An AGENT-launched run has no bridge of its own and never will: KAS parents it on the
// calling chat's session, so the LAUNCHING CHAT's bridge is the process that registered
// the run. The negative cases matter as much as the positive one: falling back to the
// utility bridge would hand the verb a text-only carrier that cannot execute the run.
func TestHostBridge_ReachesAnAgentLaunchedRunThroughItsChat(t *testing.T) {
	setup := func(t *testing.T, parentSession string, sessions ...string) (*Runtime, *fakeBridge) {
		t.Helper()
		h, cs, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowList: kasRuns(t, map[string]any{
				"workflowId": "wf_1", "status": "paused", "parentSessionId": parentSession,
			}),
			methodKiroWorkflowPause: json.RawMessage(`{"paused":true}`),
		}
		if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
			c.Name = "A"
			for _, s := range sessions {
				c.RecordSession(s)
			}
			return true
		}); err != nil {
			t.Fatalf("seed the chat: %v", err)
		}
		return h, br
	}

	t.Run("the launching chat's live bridge hosts the run", func(t *testing.T) {
		h, _ := setup(t, "sess_owned", "sess_owned")
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge: %v", err)
		}
		if sb := h.runs.hostBridge(t.Context(), "wf_1"); sb == nil {
			t.Fatal("hostBridge = nil for a run parented on a chat with a live bridge; " +
				"every pause and resume on an agent-launched run would answer 409")
		}
	})

	// A chat changes session on a failed load, a model-switch fallback and empty-turn
	// recovery, so a run launched before such a change is parented on a RETIRED id.
	t.Run("a run parented on a RETIRED session in the chain still resolves", func(t *testing.T) {
		h, _ := setup(t, "sess_old", "sess_old", "sess_current")
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge: %v", err)
		}
		if sb := h.runs.hostBridge(t.Context(), "wf_1"); sb == nil {
			t.Fatal("hostBridge = nil for a run parented on a session the chat has since retired")
		}
	})

	t.Run("a chat with no live bridge is no carrier", func(t *testing.T) {
		// Seeded but never opened. Resolving the chat id here and calling on nothing
		// would panic; answering with the utility bridge would run the run toolless.
		h, _ := setup(t, "sess_owned", "sess_owned")
		if sb := h.runs.hostBridge(t.Context(), "wf_1"); sb != nil {
			t.Error("hostBridge returned a bridge for a chat that has none")
		}
	})

	t.Run("a PARENTLESS run resolves to nothing", func(t *testing.T) {
		h, _ := setup(t, "", "sess_owned")
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge: %v", err)
		}
		if sb := h.runs.hostBridge(t.Context(), "wf_1"); sb != nil {
			t.Error("hostBridge matched a parentless run to a chat; an empty parent session " +
				"must never match a chat's chain")
		}
	})

	t.Run("a session no open chat owns resolves to nothing", func(t *testing.T) {
		h, _ := setup(t, "sess_stranger", "sess_owned")
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge: %v", err)
		}
		if sb := h.runs.hostBridge(t.Context(), "wf_1"); sb != nil {
			t.Error("hostBridge matched a run parented on a session this chat does not own")
		}
	})

	// The whole point of the change, asserted through the real verb: the RPC reaches
	// KAS instead of the handler answering errRunNotHosted.
	t.Run("Pause reaches KAS through the chat's bridge", func(t *testing.T) {
		h, br := setup(t, "sess_owned", "sess_owned")
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge: %v", err)
		}
		if err := h.runs.Pause(t.Context(), "wf_1"); err != nil {
			t.Fatalf("Pause(agent-launched run) = %v, want nil", err)
		}
		if !slices.Contains(br.callLog(), methodKiroWorkflowPause) {
			t.Error("Pause returned nil without issuing the RPC")
		}
	})

	t.Run("an unreadable run inventory refuses rather than guessing", func(t *testing.T) {
		h, cs, br := newTestHub()
		br.callErrs = map[string]error{methodKiroWorkflowList: errRecipeBusy}
		if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
			c.Name = "A"
			c.RecordSession("sess_owned")
			return true
		}); err != nil {
			t.Fatalf("seed the chat: %v", err)
		}
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge: %v", err)
		}
		if sb := h.runs.hostBridge(t.Context(), "wf_1"); sb != nil {
			t.Error("hostBridge returned a bridge while the run inventory was unreadable")
		}
	})
}

// pausedFrame builds a `_kiro/workflow/paused` notification: `{workflowId,
// pauseReason}`, which is the whole payload KAS sends for a run-level pause.
func pausedFrame(t *testing.T, workflowID, reason string) *vibekit.RPCResponse {
	t.Helper()
	params, err := json.Marshal(map[string]any{
		"workflowId": workflowID, "pauseReason": reason,
	})
	if err != nil {
		t.Fatalf("marshal paused frame: %v", err)
	}
	return &vibekit.RPCResponse{Params: params}
}

// TestHealPaused_ResumesAnInvoluntaryPauseTheMomentKASReportsIt.
//
// The trigger the recovery model was missing. `resumeInterruptedRuns` fires off
// `onSessionRehydrated`, so it only runs when a chat's bridge comes BACK — a run
// that pauses on a transient network error while its bridge is still alive had no
// trigger at all and waited for the next respawn. KAS emits
// `_kiro/workflow/paused` on the hosting bridge the moment it parks a run, with
// the reason in the payload, so the signal was already arriving unread.
func TestHealPaused_ResumesAnInvoluntaryPauseTheMomentKASReportsIt(t *testing.T) {
	const transient = "Transient connection error (EAI_AGAIN); the run is paused and can be resumed."

	// Milliseconds rather than the real 5s backoff, so the whole path runs inside a
	// unit test's budget. Restored by Cleanup, not defer: a defer does not run on a
	// subtest's failure path and would leak the override into the package.
	fastHeal := func(t *testing.T) {
		t.Helper()
		prev := healBaseDelay
		healBaseDelay = time.Millisecond
		t.Cleanup(func() { healBaseDelay = prev })
	}

	// A chat with a live bridge, its session owning wf_1, and a fake that answers
	// the inspect the heal's own guard performs.
	seed := func(t *testing.T, reason string) (*Runtime, *fakeBridge) {
		t.Helper()
		h, cs, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowInspect: inspectPaused(t, "wf_1", reason),
			methodKiroWorkflowResume:  json.RawMessage(`{}`),
		}
		if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
			c.Name = "A"
			c.RecordSession("sess_owned")
			return true
		}); err != nil {
			t.Fatalf("seed the chat: %v", err)
		}
		if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge: %v", err)
		}
		return h, br
	}

	// Deadline-bounded poll rather than a sleep: it fails closed with a diagnostic
	// and cannot flake into a false pass.
	waitForResume := func(t *testing.T, br *fakeBridge) bool {
		t.Helper()
		stop := time.Now().Add(3 * time.Second)
		for !slices.Contains(br.callLog(), methodKiroWorkflowResume) {
			if time.Now().After(stop) {
				return false
			}
			time.Sleep(time.Millisecond)
		}
		return true
	}

	t.Run("a transient network pause is resumed", func(t *testing.T) {
		fastHeal(t)
		h, br := seed(t, transient)
		var forwarded bool
		heal := h.runs.healPaused(func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {
			forwarded = true
		})
		heal(t.Context(), "c1", pausedFrame(t, "wf_1", transient))
		if !forwarded {
			t.Error("the frame was not forwarded; the client must render the pause before " +
				"anything undoes it")
		}
		if !waitForResume(t, br) {
			t.Fatalf("no resume was issued for an involuntary pause; calls were %v", br.callLog())
		}
	})

	for name, reason := range map[string]string{
		"a step waiting on a human": "Step requested user input via send_message.",
		"a policy stop":             "Repeat 'implement' reached maxIterations.",
		"a recorded failure":        "Run failed: the reviewer never approved",
	} {
		t.Run(name+" is left alone", func(t *testing.T) {
			fastHeal(t)
			h, br := seed(t, reason)
			heal := h.runs.healPaused(func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {})
			heal(t.Context(), "c1", pausedFrame(t, "wf_1", reason))
			// Give a scheduled heal every chance to fire before concluding none was.
			time.Sleep(50 * time.Millisecond)
			if slices.Contains(br.callLog(), methodKiroWorkflowResume) {
				t.Errorf("resumed a run paused for %q; that overrides a decision somebody made", reason)
			}
			// The BUDGET, not just the outcome. Asserting only "no resume happened"
			// passes with the reason gate deleted, because the callback's own state
			// re-read refuses this fixture too — measured, that mutant survived. The
			// attempt number is what proves healPaused declined at the gate: nothing
			// was claimed, so the next claim is the first.
			if attempt, _ := h.runs.claimHeal("wf_1"); attempt != 1 {
				t.Errorf("attempt number = %d, want 1; healPaused spent a heal on a run paused "+
					"for %q instead of declining at the reason gate", attempt, reason)
			}
		})
	}

	// The callback re-reads the run rather than trusting the frame that scheduled
	// it, which is what makes the untracked timer safe: a run cancelled, resumed or
	// finished inside the backoff must not be re-driven.
	t.Run("a run that left the paused state inside the backoff is not resumed", func(t *testing.T) {
		fastHeal(t)
		h, br := seed(t, transient)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", "aborted", transient))
		heal := h.runs.healPaused(func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {})
		heal(t.Context(), "c1", pausedFrame(t, "wf_1", transient))
		time.Sleep(50 * time.Millisecond)
		if slices.Contains(br.callLog(), methodKiroWorkflowResume) {
			t.Error("resumed a run KAS now reports aborted")
		}
	})

	t.Run("a frame with no chat id or no run id does nothing", func(t *testing.T) {
		fastHeal(t)
		h, br := seed(t, transient)
		heal := h.runs.healPaused(func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {})
		heal(t.Context(), "", pausedFrame(t, "wf_1", transient))
		heal(t.Context(), "c1", pausedFrame(t, "", transient))
		time.Sleep(50 * time.Millisecond)
		if slices.Contains(br.callLog(), methodKiroWorkflowResume) {
			t.Error("a frame missing its addressing still reached a resume")
		}
		// Same reasoning as the reason-gate cases: the outcome alone is protected by a
		// second guard, so assert nothing was claimed.
		if attempt, _ := h.runs.claimHeal("wf_1"); attempt != 1 {
			t.Errorf("attempt number = %d, want 1; a frame with no chat id spent a heal", attempt)
		}
	})
}

// TestHealBudget_BoundsThePauseHealLoopAndProgressRefillsIt.
//
// A heal and a pause can drive each other: a network that is genuinely down fails
// the step again the moment the run resumes, and the frame that reports that is
// the same frame that triggers the next heal. So the budget is what stops the
// loop, and PROGRESS is what refills it — without the refill the budget would be
// per-process, and a job running for hours would spend its three attempts on one
// morning blip with nothing left for an unrelated one that afternoon.
func TestHealBudget_BoundsThePauseHealLoopAndProgressRefillsIt(t *testing.T) {
	t.Run("three attempts, then the run is left paused", func(t *testing.T) {
		h, _, _ := newTestHub()
		for i := 1; i <= maxAutoHeals; i++ {
			attempt, ok := h.runs.claimHeal("wf_1")
			if !ok {
				t.Fatalf("claimHeal attempt %d = refused, want granted", i)
			}
			if attempt != i {
				t.Errorf("claimHeal attempt number = %d, want %d; the backoff is computed from it", attempt, i)
			}
		}
		if _, ok := h.runs.claimHeal("wf_1"); ok {
			t.Errorf("claimHeal granted a %dth attempt; the pause-heal loop is unbounded", maxAutoHeals+1)
		}
	})

	t.Run("a completed node gives the whole budget back", func(t *testing.T) {
		h, _, _ := newTestHub()
		for range maxAutoHeals {
			if _, ok := h.runs.claimHeal("wf_1"); !ok {
				t.Fatal("setup: the budget was refused before it was spent")
			}
		}
		progress := h.runs.healProgress(func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {})
		progress(t.Context(), "c1", pausedFrame(t, "wf_1", ""))

		attempt, ok := h.runs.claimHeal("wf_1")
		if !ok {
			t.Error("a run that made progress got no heal budget back, so one blip writes it " +
				"off for the rest of the process")
		}
		if attempt != 1 {
			t.Errorf("attempt number after progress = %d, want 1 (a full reset)", attempt)
		}
	})

	t.Run("the budget is per run", func(t *testing.T) {
		h, _, _ := newTestHub()
		for range maxAutoHeals {
			if _, ok := h.runs.claimHeal("wf_1"); !ok {
				t.Fatal("setup: the budget was refused before it was spent")
			}
		}
		if _, ok := h.runs.claimHeal("wf_2"); !ok {
			t.Error("one run exhausting its budget denied another run's first attempt")
		}
	})

	t.Run("a run ending clears its counter", func(t *testing.T) {
		h, _, _ := newTestHub()
		if _, ok := h.runs.claimHeal("wf_1"); !ok {
			t.Fatal("setup: the first attempt was refused")
		}
		h.runs.forgetBounds(t.Context(), "wf_1")
		if attempt, _ := h.runs.claimHeal("wf_1"); attempt != 1 {
			t.Errorf("attempt number after the run ended = %d, want 1; a workflow id KAS reuses "+
				"would inherit a spent budget", attempt)
		}
	})
}
