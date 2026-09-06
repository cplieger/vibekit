package agent

// The liveness split: only an ACTIVELY EXECUTING run holds a process, every run stays
// reachable, and the verbs that drive a parked run re-host it on demand.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// The `_kiro/workflow/run_complete` notification KAS sends when a run stops. One frame
// reports terminal and paused alike; the status is the only thing separating them.
func runCompleteFrame(t *testing.T, workflowID, status string) *vibekit.RPCResponse {
	t.Helper()
	params, err := json.Marshal(map[string]any{"workflowId": workflowID, "status": status})
	if err != nil {
		t.Fatalf("marshal run_complete: %v", err)
	}
	return &vibekit.RPCResponse{Method: methodWFRunComplete, Params: params}
}

// A run whose bridge is registered and whose lease is granted and bounded: EXECUTING.
func hostedRun(t *testing.T, workflowID string) (*Runtime, *fakeBridge) {
	t.Helper()
	h, _, br := newTestHub()
	h.bridge.mgr.insert(runChatID(workflowID), &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.grantLease(t.Context(), workflowID, "nightly", manualLaunch())
	h.runs.armDeadline(t.Context(), workflowID)
	return h, br
}

// Deadline-bounded rather than a sleep: closeStoppedBridge hands the close to a
// goroutine (it runs FROM the bridge's own forward loop), so the effect is async.
func waitForBridge(t *testing.T, h *Runtime, workflowID string, want bool) bool {
	t.Helper()
	stop := time.Now().Add(3 * time.Second)
	for {
		if (h.bridge.mgr.get(runChatID(workflowID)) != nil) == want {
			return true
		}
		if time.Now().After(stop) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}

// The ruling in one table: no process needs to run for a run that cannot be resumed,
// and every run stays reachable. The lease a PAUSED run keeps is not an inconsistency
// (vibekit-runtime.md's liveness-split block). Driven through the real dispatch path
// because observeComplete decides the lease half on that same frame.
func TestRunStopped_DropsTheProcessAndAPausedRunKeepsItsLease(t *testing.T) {
	cases := map[string]struct {
		status     string
		wantBridge bool
		wantLease  bool
	}{
		// The ruling's own row.
		"a pause drops the process and keeps the lease": {runStatusPaused, false, true},
		// Here so a mutation widening the close cannot pass on the pause row alone.
		"a completed run drops both":  {"completed", false, false},
		"a failed run drops both":     {"failed", false, false},
		"an aborted run drops both":   {"aborted", false, false},
		"a cancelled run drops both":  {"cancelled", false, false},
		"an executing run keeps both": {"running", true, true},
		// An unknown status keeps the bridge: a leaked process costs memory, a wrongly
		// closed one loses the frames a live run is still emitting.
		"an unrecognised status keeps both": {"reticulating", true, true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, _ := hostedRun(t, "wf_1")
			h.dispatch(t.Context(), runChatID("wf_1"), runCompleteFrame(t, "wf_1", tc.status))

			if !waitForBridge(t, h, "wf_1", tc.wantBridge) {
				t.Errorf("bridge present = %v for status %q, want %v",
					!tc.wantBridge, tc.status, tc.wantBridge)
			}
			if _, held := h.runs.lease("wf_1"); held != tc.wantLease {
				t.Errorf("lease held = %v for status %q, want %v", held, tc.status, tc.wantLease)
			}
		})
	}
}

// The second half of the pause row: presence alone would be satisfied by a lease
// stripped of everything it answers. The DEADLINE is deliberately absent from the
// wanted set — a pause parks it, and a re-arm on resume bounds the next stretch.
func TestRunStopped_APausedRunKeepsTheFieldsItsLeaseIsFor(t *testing.T) {
	h, _, br := newTestHub()
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
	// A SCHEDULED launch, because that is the origin the unattended mark rides.
	h.runs.grantLease(t.Context(), "wf_1", "nightly",
		scheduledLaunch("sched_1", time.Now().Add(time.Hour)))

	h.dispatch(t.Context(), runChatID("wf_1"), runCompleteFrame(t, "wf_1", runStatusPaused))
	if !waitForBridge(t, h, "wf_1", false) {
		t.Fatal("the parked run kept its process")
	}

	l, held := h.runs.lease("wf_1")
	if !held {
		t.Fatal("the parked run lost its lease, so nothing knows it may not start twice")
	}
	if l.Recipe != "nightly" {
		t.Errorf("recipe = %q, want nightly: admission explains a blocking row from it", l.Recipe)
	}
	if l.ScheduleID != "sched_1" {
		t.Errorf("schedule_id = %q, want sched_1: the outcome has nowhere to be recorded "+
			"without it", l.ScheduleID)
	}
	if !l.Unattended {
		t.Error("the unattended mark went with the pause, so the permission floor would " +
			"leave the next ask waiting for a human who will never arrive")
	}
}

// The four verbs that drive a parked run start a carrier on demand: without it the
// parked population is unreachable the moment a pause drops its bridge. Through their
// PUBLIC entry points, so what is pinned is the behaviour a REST caller gets.
func TestRunVerbs_ReHostARunNothingHolds(t *testing.T) {
	// Parented on nothing this process knows, so the re-host is the verb's only way in.
	seed := func(t *testing.T) (*Runtime, *fakeBridge) {
		t.Helper()
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowList: json.RawMessage(`{"runs":[]}`),
			// SetStepStatus reads the tree first, so the target must resolve to its node.
			methodKiroWorkflowInspect: parkedInspect(
				t, runStatusPaused, needInputPauseReason, "sess_step",
			),
		}
		if h.bridge.mgr.get(runChatID("wf_1")) != nil {
			t.Fatal("the fixture registered a bridge, so this exercises the wrong branch")
		}
		return h, br
	}

	verbs := map[string]struct {
		method string
		issue  func(*Runtime) error
	}{
		"resume": {methodKiroWorkflowResume, func(h *Runtime) error {
			return h.runs.Resume(context.Background(), "wf_1")
		}},
		// KAS's pause REFUSES a run it has forgotten, and the refusal has to come from
		// KAS rather than vibekit, or the reader is shown vibekit's bookkeeping.
		"pause": {methodKiroWorkflowPause, func(h *Runtime) error {
			return h.runs.Pause(context.Background(), "wf_1")
		}},
		"set_step_status": {methodKiroWorkflowUpdate, func(h *Runtime) error {
			return h.runs.SetStepStatus(context.Background(), "wf_1", "review", runStepCompleted)
		}},
	}

	for name, tc := range verbs {
		t.Run(name+" re-hosts and reaches KAS", func(t *testing.T) {
			h, br := seed(t)
			if err := tc.issue(h); err != nil {
				t.Fatalf("%s on an unhosted run = %v, want nil", name, err)
			}
			if !slices.Contains(br.callLog(), tc.method) {
				t.Errorf("%s never reached KAS; calls were %v", tc.method, br.callLog())
			}
			if h.bridge.mgr.get(runChatID("wf_1")) == nil {
				t.Error("no bridge is registered under the run's synthetic chat id, so the " +
					"run's own lifecycle frames have nowhere to route")
			}
		})
	}

	t.Run("answer_input re-hosts and reaches KAS", func(t *testing.T) {
		h, br := seed(t)
		h.runs.asks.Add(&runAsk{
			chatID: "run:wf_1",
			payload: vibekit.RunInputNeededPayload{
				WorkflowID: "wf_1", AskID: "a1", StepSessionID: "sess_step",
			},
		})
		if err := h.runs.AnswerInput(t.Context(), "wf_1", "a1", "the main branch"); err != nil {
			t.Fatalf("AnswerInput on an unhosted run = %v, want nil", err)
		}
		if !slices.Contains(br.callLog(), vibekit.MethodPrompt) {
			t.Errorf("the answer never reached KAS; calls were %v", br.callLog())
		}
		if h.bridge.mgr.get(runChatID("wf_1")) == nil {
			t.Error("no bridge is registered under the run's synthetic chat id")
		}
	})

	// Why `discard` is returned rather than derived: a carrier THIS call started holds
	// a run nothing is executing once the verb fails, so it must go.
	t.Run("a refused verb tears the carrier it started back down", func(t *testing.T) {
		h, br := seed(t)
		br.callRPCErrs = map[string]*vibekit.RPCError{
			methodKiroWorkflowResume: {Code: -32603, Message: "Internal error"},
		}
		if err := h.runs.Resume(t.Context(), "wf_1"); err == nil {
			t.Fatal("a refused resume reported success")
		}
		if h.bridge.mgr.get(runChatID("wf_1")) != nil {
			t.Error("the refused verb left a process hosting a run it could not drive")
		}
	})

	// The DECLINE, the one outcome neither the error path above nor a lifecycle frame
	// covers: KAS resolved the run and WROTE NOTHING, so no run_complete follows and
	// nothing else would ever close the process the re-host started. Driven through the
	// re-host, because every other case pre-inserts a bridge where discard is a no-op.
	t.Run("a DECLINED step status tears the carrier it started back down", func(t *testing.T) {
		h, br := seed(t)
		br.callResults[methodKiroWorkflowUpdate] = json.RawMessage(
			`{"workflowId":"wf_1","updated":false,"queued":false,` +
				`"message":"No current step to update: the workflow has no running or paused step."}`,
		)

		err := h.runs.SetStepStatus(t.Context(), "wf_1", "review", runStepCompleted)
		if !errors.Is(err, errStepStatusRefused) {
			t.Fatalf("a declined update = %v, want errStepStatusRefused", err)
		}
		if h.bridge.mgr.get(runChatID("wf_1")) != nil {
			t.Error("a declined update left the process the re-host started hosting a run " +
				"KAS wrote nothing to, and no lifecycle frame is coming to close it")
		}
	})

	// The other side: for an agent-launched run the bridge the verb did not start is
	// the launching CHAT's, so closing it would tear down the conversation's process.
	t.Run("a refused verb leaves a bridge it did not start alone", func(t *testing.T) {
		h, _, br := newTestHub()
		h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
		br.callRPCErrs = map[string]*vibekit.RPCError{
			methodKiroWorkflowPause: {Code: -32603, Message: "Internal error"},
		}
		if err := h.runs.Pause(t.Context(), "wf_1"); err == nil {
			t.Fatal("a refused pause reported success")
		}
		if h.bridge.mgr.get(runChatID("wf_1")) == nil {
			t.Error("a refused verb closed a bridge it did not start; for an agent-launched " +
				"run that is the launching chat's own process")
		}
	})
}

// The three wrongs discarding `insert`'s refusal produced are in vibekit-runtime.md's
// liveness-split block. The factory is overridden because newTestHub's serves one
// shared bridge, and the question here is which of TWO processes survives.
func TestRehost_ALostRaceStopsItsOwnBridgeAndLeavesTheWinnerAlone(t *testing.T) {
	h, _, _ := newTestHub()
	incumbent := newFakeBridge()
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: incumbent, state: bridgeIdle})

	loser := newFakeBridge()
	h.bridge.mgr.factory = func() ACPBridge { return loser }

	sb, discard, err := h.runs.rehost(t.Context(), "wf_1")
	if err != nil {
		t.Fatalf("rehost against an occupied chat id = %v, want the incumbent", err)
	}
	if sb.bridge != incumbent {
		t.Error("the loser was handed its own bridge, which is in no map — so nothing " +
			"will ever close it and its ~300 MB process tree outlives the run")
	}
	if !loser.isStopped() {
		t.Error("the second process was left running: its frames arrive on a chat id " +
			"another bridge owns, indistinguishable from the winner's")
	}

	// The refusal path, which is where the third wrong lands.
	discard(errors.New("refused"))
	if h.bridge.mgr.get(runChatID("wf_1")) == nil {
		t.Error("the loser's discard closed the WINNER's carrier, so a run KAS may " +
			"already be driving has nowhere to send its frames")
	}
	if incumbent.isStopped() {
		t.Error("the winner's process was stopped by the loser's discard")
	}
}

// A context error says the verb's outcome is UNKNOWN, so the carrier stays — the same
// direction closeStoppedBridge takes for an unrecognised status. Through hostedControl
// rather than the closure, because the verb call is where the caller's context lands.
func TestRehost_ACancelledVerbKeepsTheCarrierItStarted(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: json.RawMessage(`{"runs":[]}`),
	}
	br.callErrs = map[string]error{methodKiroWorkflowResume: context.Canceled}

	if err := h.runs.Resume(t.Context(), "wf_1"); err == nil {
		t.Fatal("a cancelled resume reported success")
	}
	if h.bridge.mgr.get(runChatID("wf_1")) == nil {
		t.Error("the carrier was torn down on a cancellation, so a run KAS may already " +
			"be driving has nowhere to send its frames")
	}
}

// The lease and the carrier follow ONE unknown-outcome rule, and the lease is what
// makes a run BOUNDABLE — armDeadline reads it first and returns when there is none.
// So handing it back on a cancellation leaves a retry KAS did take executing with no
// ceiling, no slot, absent from the executing set, and unbindable afterwards.
func TestRetry_ACancelledRetryKeepsTheLeaseItMinted(t *testing.T) {
	cases := map[string]struct {
		cause     error
		wantLease bool
	}{
		// The unknown outcome: the client walked away mid-verb.
		"a cancelled retry keeps it": {context.Canceled, true},
		"a deadline keeps it":        {context.DeadlineExceeded, true},
		// A KAS refusal is a KNOWN outcome — nothing re-drove — so the minted lease goes
		// back, and a mutation keeping it cannot pass on the rows above alone.
		"a refused retry gives it back": {errors.New("refused"), false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowList: json.RawMessage(`{"runs":[]}`),
			}
			br.callErrs = map[string]error{methodKiroWorkflowRetry: tc.cause}
			if _, held := h.runs.lease("wf_1"); held {
				t.Fatal("the fixture holds a lease, so Retry mints none and this proves nothing")
			}

			if err := h.runs.Retry(t.Context(), "wf_1"); err == nil {
				t.Fatal("a failed retry reported success")
			}
			why := "a run KAS may be driving cannot be bounded without the lease armDeadline reads"
			if !tc.wantLease {
				why = "a lease minted for a run that provably never re-drove was stranded"
			}
			if _, held := h.runs.lease("wf_1"); held != tc.wantLease {
				t.Errorf("lease held = %v, want %v: %s", held, tc.wantLease, why)
			}
		})
	}
}

// The bound's decision, asked SYNCHRONOUSLY rather than through its timer. The spared
// rows are why: through the timer, "the bridge is still there" is satisfied by the
// instant BEFORE the grace elapses, so each would pass against a bound that closes the
// carrier unconditionally a moment later.
func TestCloseKeptCarrier_DecidesOnAFreshRead(t *testing.T) {
	cases := map[string]struct {
		inspect json.RawMessage
		// held marks a verb still holding the carrier when the bound comes due.
		held bool
		want carrierVerdict
	}{
		// KAS never took the verb: the run is where it was, and nothing is coming.
		"a parked run's kept carrier is closed": {
			inspectReply(t, "wf_1", runStatusPaused, ""), false, carrierClosed,
		},
		"a terminal run's kept carrier is closed": {
			inspectReply(t, "wf_1", "failed", ""), false, carrierClosed,
		},
		// KAS DID take it, so the carrier is the connection that run's frames arrive
		// on and closing it would send them nowhere.
		"an executing run's carrier is spared": {
			inspectReply(t, "wf_1", "running", ""), false, carrierSpared,
		},
		// A failed read never destroys work here.
		"an unreadable run's carrier is spared": {json.RawMessage(`{`), false, carrierSpared},
		// The identity guard: a reply naming another run says nothing about this one.
		"a reply naming another run is ignored": {
			inspectReply(t, "wf_other", "failed", ""), false, carrierSpared,
		},
		// The USE guard: a reader whose first resume was cancelled clicks again and
		// reuses the kept carrier, so KAS reports the run parked while that second verb
		// is in flight on the very process the bound is about to stop.
		"a carrier a verb is holding is kept, whatever the run reports": {
			inspectReply(t, "wf_1", runStatusPaused, ""), true, carrierBusy,
		},
		// Both directions of that guard, so neither arm can pass by widening the
		// other: an executing run is spared for its OWN reason, not for this one.
		"a held carrier on an executing run is still busy": {
			inspectReply(t, "wf_1", "running", ""), true, carrierBusy,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowInspect: tc.inspect,
			}
			kept := &sharedBridge{bridge: br, state: bridgeIdle}
			h.bridge.mgr.insert(runChatID("wf_1"), kept)
			if tc.held {
				h.runs.carriers.enter(kept)
				t.Cleanup(func() { h.runs.carriers.leave(kept) })
			}

			got := h.runs.closeKeptCarrier(runChatID("wf_1"), "wf_1", kept)

			if got != tc.want {
				t.Errorf("closeKeptCarrier = %v, want %v", got, tc.want)
			}
			wantPresent := tc.want != carrierClosed
			if present := h.bridge.mgr.get(runChatID("wf_1")) != nil; present != wantPresent {
				t.Errorf("bridge present = %v, want %v", present, wantPresent)
			}
		})
	}

	// The map holds a DIFFERENT carrier, so this one is no longer ours to end. SPARED
	// rather than busy even under a verb, so a stale bound cannot re-arm forever over a
	// carrier nothing will close.
	t.Run("a carrier replaced meanwhile is left alone", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowInspect: inspectReply(t, "wf_1", "failed", ""),
		}
		incumbent := &sharedBridge{bridge: br, state: bridgeIdle}
		h.bridge.mgr.insert(runChatID("wf_1"), incumbent)
		stale := &sharedBridge{bridge: newFakeBridge(), state: bridgeIdle}
		h.runs.carriers.enter(stale)
		t.Cleanup(func() { h.runs.carriers.leave(stale) })

		if got := h.runs.closeKeptCarrier(runChatID("wf_1"), "wf_1", stale); got != carrierSpared {
			t.Errorf("a stale bound = %v, want carrierSpared: it must neither close the "+
				"carrier a later re-host registered nor re-arm over one nothing will close", got)
		}
		if h.bridge.mgr.get(runChatID("wf_1")) != incumbent {
			t.Error("the incumbent was unregistered by another call's bound")
		}
	})
}

// A second verb reuses the kept carrier through hostOrRehost, whose discard is then a
// no-op, so a bound that declines once and stops leaves exactly the leak it exists to
// end. Through the TIMER, because the re-arm is the timer's own decision; the negative
// half runs first and only the positive half polls.
func TestBoundKeptCarrier_ReArmsWhileAVerbIsStillHoldingTheCarrier(t *testing.T) {
	old := keptCarrierGrace
	keptCarrierGrace = time.Millisecond
	t.Cleanup(func() { keptCarrierGrace = old })

	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", runStatusPaused, ""),
	}
	kept := &sharedBridge{bridge: br, state: bridgeIdle}
	h.bridge.mgr.insert(runChatID("wf_1"), kept)
	h.runs.carriers.enter(kept)

	h.runs.boundKeptCarrier(runChatID("wf_1"), "wf_1", kept)

	// Many graces' worth: a bound that closed under the verb, or that gave up
	// silently, both land here — and they are told apart by the poll below.
	time.Sleep(50 * time.Millisecond)
	if h.bridge.mgr.get(runChatID("wf_1")) == nil {
		t.Fatal("the bound closed a carrier a verb was still holding, which is the " +
			"unknown-outcome state keeping it exists to prevent")
	}

	// The verb finishes; the next firing has nothing left to wait for.
	h.runs.carriers.leave(kept)
	if !waitForBridge(t, h, "wf_1", false) {
		t.Error("the bound never came back after the verb released the carrier, so a " +
			"single busy firing retires it and the ~300 MB process tree leaks")
	}
}

// The WIRING, which the decision table above cannot pin: that one drives carrierUse
// from the test, so with a verb's enter/leave gone the guard still answers while
// nothing registers. Each verb is held open ON a real call, so the assertion lands
// strictly inside its window.
func TestCarrierUse_AVerbHoldsItsCarrierForTheWholeSpan(t *testing.T) {
	cases := map[string]struct {
		// blocked is the method held open, and it is what puts the assertion inside
		// the span rather than beside it.
		blocked string
		drive   func(*testing.T, *Runtime) error
	}{
		// A reader whose first resume was cancelled clicks again, reuses the kept carrier,
		// and is still waiting when the grace elapses — where both older guards pass.
		"a resume in flight": {
			methodKiroWorkflowResume,
			func(t *testing.T, h *Runtime) error { return h.runs.Resume(t.Context(), "wf_1") },
		},
		// Why the count is entered at RESOLVE: this verb's address read is a round trip,
		// so a Call-scoped count leaves the carrier closable for the length of it.
		"an answer still resolving its address": {
			methodKiroWorkflowInspect,
			func(t *testing.T, h *Runtime) error {
				h.runs.asks.Add(&runAsk{
					chatID: runChatID("wf_1"),
					payload: vibekit.RunInputNeededPayload{
						WorkflowID: "wf_1", AskID: "a1", NodeID: "review",
						StepSessionID: "sess_step",
					},
				})
				return h.runs.AnswerInput(t.Context(), "wf_1", "a1", "the release branch")
			},
		},
		// The other two verbs, so no site's hold can be deleted silently. Retry's own call
		// is bounded by launchTimeout and counted anyway: a bound armed by an EARLIER verb
		// targets this same carrier.
		"a step-status write in flight": {
			methodKiroWorkflowUpdate,
			func(t *testing.T, h *Runtime) error {
				return h.runs.SetStepStatus(t.Context(), "wf_1", "review", runStepCompleted)
			},
		},
		"a retry in flight": {
			methodKiroWorkflowRetry,
			func(t *testing.T, h *Runtime) error { return h.runs.Retry(t.Context(), "wf_1") },
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowInspect: parkedInspect(
					t, runStatusPaused, needInputPauseReason, "sess_step",
				),
				// Retry reads its recipe off the run list before it re-drives.
				methodKiroWorkflowList: json.RawMessage(
					`{"runs":[{"workflowId":"wf_1","name":"nightly","status":"aborted"}]}`,
				),
			}
			held := make(chan struct{})
			br.blockOn = map[string]chan struct{}{tc.blocked: held}
			kept := &sharedBridge{bridge: br, state: bridgeIdle}
			h.bridge.mgr.insert(runChatID("wf_1"), kept)

			done := make(chan error, 1)
			go func() { done <- tc.drive(t, h) }()

			// The fake's own record, never carriers.busy: waiting on the thing under
			// assertion would make the assertion vacuous.
			stop := time.Now().Add(5 * time.Second)
			for !slices.Contains(br.callLog(), tc.blocked) {
				if time.Now().After(stop) {
					close(held)
					t.Fatalf("the verb never reached %s: %v", tc.blocked, br.callLog())
				}
				time.Sleep(time.Millisecond)
			}

			if got := h.runs.closeKeptCarrier(runChatID("wf_1"), "wf_1", kept); got != carrierBusy {
				t.Errorf("closeKeptCarrier during a verb = %v, want carrierBusy: the verb is "+
					"mid-flight on that process, so closing it is the unknown-outcome state "+
					"the keep exists to prevent", got)
			}
			if h.bridge.mgr.get(runChatID("wf_1")) != kept {
				t.Error("the carrier was closed under a verb still holding it")
			}

			close(held)
			if err := <-done; err != nil {
				t.Fatalf("the verb failed: %s", err)
			}
			// RELEASED on the way out, or the bound re-arms forever and never closes it.
			if h.runs.carriers.busy(kept) {
				t.Error("the carrier is still recorded as held after the verb returned, so " +
					"its bound re-arms forever and the process leaks")
			}
		})
	}
}

// The keeping and the bound are one decision, so a cancelled verb has to ARM it rather
// than leave the carrier for the container's life. What keeps the re-read off a verb in
// flight is carrierUse, not the grace's length.
func TestRehost_ACancelledVerbArmsTheBoundOnTheCarrierItKeeps(t *testing.T) {
	old := keptCarrierGrace
	keptCarrierGrace = time.Millisecond
	t.Cleanup(func() { keptCarrierGrace = old })

	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: json.RawMessage(`{"runs":[]}`),
		// KAS never took the resume, so the run is still parked.
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", runStatusPaused, ""),
	}
	br.callErrs = map[string]error{methodKiroWorkflowResume: context.Canceled}

	if err := h.runs.Resume(t.Context(), "wf_1"); err == nil {
		t.Fatal("a cancelled resume reported success")
	}
	// Deterministic in this direction only: the poll can pass exclusively once the
	// bridge is genuinely gone, which nothing but the bound can do here.
	if !waitForBridge(t, h, "wf_1", false) {
		t.Error("the kept carrier was never bounded, so a browser tab closed at the wrong " +
			"instant leaks a ~300 MB process tree for the container's life")
	}
}

// KAS reroutes a prompt into the run only while the addressed step is parked; past
// that the same prompt runs as an ordinary turn on that session (vibekit-acp.md "A
// step's answer is a plain `session/prompt`"). SETTLED rather than restored, because
// nothing will wait on the question again — the between-steps case below will.
func TestAnswerInput_AMovedOnStepIsSettledRatherThanAnswered(t *testing.T) {
	cases := map[string]json.RawMessage{
		"a different step is parked now": parkedInspect(
			t, runStatusPaused, needInputPauseReason, "sess_other",
		),
		"the run is over": inspectReply(t, "wf_1", "failed", ""),
	}

	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowList:    json.RawMessage(`{"runs":[]}`),
				methodKiroWorkflowInspect: reply,
			}
			h.runs.asks.Add(&runAsk{
				chatID: runChatID("wf_1"),
				payload: vibekit.RunInputNeededPayload{
					// A node the reply does NOT report as the parked leaf.
					WorkflowID: "wf_1", AskID: "a1", NodeID: "plan",
					StepSessionID: "sess_stale",
				},
			})

			err := h.runs.AnswerInput(t.Context(), "wf_1", "a1", "the main branch")
			if !errors.Is(err, errAskAlreadySettled) {
				t.Fatalf("AnswerInput for a moved-on step = %v, want errAskAlreadySettled", err)
			}
			if slices.Contains(br.callLog(), vibekit.MethodPrompt) {
				t.Error("the answer was sent anyway; KAS runs it as an ordinary turn on a " +
					"step nobody asked to steer, and no run frame closes the carrier")
			}
			if h.runs.asks.HasRun("wf_1") {
				t.Error("the ask was re-offered, so a reader is asked to answer a question " +
					"the run has stopped waiting on")
			}
			if !hasEventType(bufferedEvents(h), string(vibekit.EventRunInputSettled)) {
				t.Error("no run_input_settled event, so the card stays live on every surface")
			}
			if h.bridge.mgr.get(runChatID("wf_1")) != nil {
				t.Error("the carrier started for an ask that had moved on was left running")
			}
		})
	}
}

// The third verdict, and the one a reader reaches WITHOUT a race: the run view offers
// Resume and the ask card at once, and between the resume and the re-park nothing is
// parked. Reading that as GONE discards the words just typed and reports the step as
// moved on, false in both clauses, so the card goes BACK.
func TestAnswerInput_ARunBetweenStepsHoldsTheAnswerRatherThanDiscardingIt(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: json.RawMessage(`{"runs":[]}`),
		// Non-terminal, and no node parked: the resume landed, the re-park has not.
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "running", ""),
	}
	h.runs.asks.Add(&runAsk{
		chatID: runChatID("wf_1"),
		payload: vibekit.RunInputNeededPayload{
			WorkflowID: "wf_1", AskID: "a1", NodeID: "review", StepSessionID: "sess_step",
		},
	})

	err := h.runs.AnswerInput(t.Context(), "wf_1", "a1", "the main branch")
	if !errors.Is(err, errRunNotParked) {
		t.Fatalf("AnswerInput between steps = %v, want errRunNotParked", err)
	}
	if slices.Contains(br.callLog(), vibekit.MethodPrompt) {
		t.Error("the answer was sent into a run with no parked step, which KAS runs as an " +
			"ordinary turn on that session")
	}
	if !h.runs.asks.HasRun("wf_1") {
		t.Error("the ask was consumed, so the reader's words are gone and the card is off " +
			"every surface with the question still open")
	}
	if hasEventType(bufferedEvents(h), string(vibekit.EventRunInputSettled)) {
		t.Error("the ask was settled, so every surface retires a card the run is still " +
			"about to wait on")
	}
	if !hasEventType(bufferedEvents(h), string(vibekit.EventRunInputNeeded)) {
		t.Error("no run_input_needed re-offer, so the card is gone until the next SSE " +
			"connect refills it from the replay")
	}
	if h.bridge.mgr.get(runChatID("wf_1")) != nil {
		t.Error("the carrier started for an answer that was held back was left running")
	}
}

// The check asks whether the step THAT ASKED is parked, addressed by the ask's own node
// id rather than by pausedLeaf's first depth-first match. The two agree for a run parked
// at one step and diverge exactly here: with two branches parked, first-match names the
// wrong one and the other branch's answer is destroyed.
func TestAnswerInput_AParkedBranchIsAnsweredEvenWhenItIsNotTheFirstMatch(t *testing.T) {
	tree, err := json.Marshal(map[string]any{
		"state": map[string]any{
			"status": runStatusPaused,
			"root": map[string]any{
				"nodeId": "fanout", "status": "paused",
				"children": []any{
					map[string]any{"nodeId": "branch_a", "status": "paused", "sessionId": "sess_a"},
					map[string]any{"nodeId": "branch_b", "status": "paused", "sessionId": "sess_b"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Setup: marshalling the inspect reply: %s", err)
	}

	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList:    json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowInspect: tree,
	}
	h.runs.asks.Add(&runAsk{
		chatID: runChatID("wf_1"),
		payload: vibekit.RunInputNeededPayload{
			// The SECOND parked branch, so a first-match check answers "a different
			// step is parked" and moots it.
			WorkflowID: "wf_1", AskID: "a1", NodeID: "branch_b", StepSessionID: "sess_stale",
		},
	})

	if err := h.runs.AnswerInput(t.Context(), "wf_1", "a1", "the main branch"); err != nil {
		t.Fatalf("AnswerInput for the second parked branch = %v, want nil", err)
	}
	params := br.paramsFor(vibekit.MethodPrompt)
	if params == nil {
		t.Fatalf("no %s call, calls were %v", vibekit.MethodPrompt, br.callLog())
	}
	if params["sessionId"] != "sess_b" {
		t.Errorf("sessionId = %v, want sess_b: the answer went to the branch that did not "+
			"ask, or was discarded as moot", params["sessionId"])
	}
}

// The read has to LEAD for the check above to mean anything: preferring the ask's own
// address makes a stale ask unnoticeable. The payload is only the unreadable-run fallback.
func TestAnswerInput_TheFreshAddressBeatsTheOneTheAskCarries(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowInspect: parkedInspect(
			t, runStatusPaused, needInputPauseReason, "sess_current",
		),
	}
	h.runs.asks.Add(&runAsk{
		chatID: runChatID("wf_1"),
		payload: vibekit.RunInputNeededPayload{
			// `review` is the leaf parkedInspect reports, so the step has NOT moved —
			// only the session recorded on the ask is out of date.
			WorkflowID: "wf_1", AskID: "a1", NodeID: "review",
			StepSessionID: "sess_stale",
		},
	})

	if err := h.runs.AnswerInput(t.Context(), "wf_1", "a1", "the main branch"); err != nil {
		t.Fatalf("AnswerInput = %v, want nil", err)
	}
	params := br.paramsFor(vibekit.MethodPrompt)
	if params == nil {
		t.Fatalf("no %s call, calls were %v", vibekit.MethodPrompt, br.callLog())
	}
	if params["sessionId"] != "sess_current" {
		t.Errorf("sessionId = %v, want sess_current: the ask's recorded address won, so a "+
			"step whose session changed is answered at the wrong one", params["sessionId"])
	}
}

// A failed read never destroys work, and here the work is the answer itself: refusing
// every answer whenever the utility session is briefly unreachable would be a new way
// to lose a run parked on a person.
func TestAnswerInput_AnUnreadableRunFallsBackToTheAddressTheAskCarries(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: json.RawMessage(`{"runs":[]}`),
	}
	br.callErrs = map[string]error{methodKiroWorkflowInspect: errors.New("bridge died")}
	h.runs.asks.Add(&runAsk{
		chatID: runChatID("wf_1"),
		payload: vibekit.RunInputNeededPayload{
			WorkflowID: "wf_1", AskID: "a1", NodeID: "review", StepSessionID: "sess_step",
		},
	})

	if err := h.runs.AnswerInput(t.Context(), "wf_1", "a1", "the main branch"); err != nil {
		t.Fatalf("AnswerInput with an unreadable run = %v, want nil", err)
	}
	params := br.paramsFor(vibekit.MethodPrompt)
	if params == nil {
		t.Fatalf("no %s call, calls were %v", vibekit.MethodPrompt, br.callLog())
	}
	if params["sessionId"] != "sess_step" {
		t.Errorf("sessionId = %v, want sess_step", params["sessionId"])
	}
}

// The ORDERING, not the outcome: a claimed ask is off every surface, so the other order
// leaves a window with neither a card nor a process. The discriminator is an ask already
// GONE — host-then-claim spends one spawn, claim-then-host none — so the spawn DELTA
// answers which ran. The utility session is warmed first because one factory serves both.
func TestAnswerInput_HostsBeforeItClaimsTheAsk(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: json.RawMessage(`{"runs":[]}`),
	}
	if _, err := h.runs.listRaw(t.Context()); err != nil {
		t.Fatalf("warm the utility session: %v", err)
	}

	before := br.startCount()
	err := h.runs.AnswerInput(t.Context(), "wf_1", "a1", "the main branch")
	if !errors.Is(err, errAskAlreadySettled) {
		t.Fatalf("AnswerInput for an ask nobody holds = %v, want errAskAlreadySettled", err)
	}
	if got := br.startCount() - before; got != 1 {
		t.Errorf("spawns during the call = %d, want 1; the answer path claimed the ask "+
			"before it had a carrier, so a reader could be left with no card and no process",
			got)
	}
	if h.bridge.mgr.get(runChatID("wf_1")) != nil {
		t.Error("the carrier started for an ask that turned out to be gone was left running")
	}
}

// Cancel is the one verb that may run on the utility session — it rehydrates from disk
// and only WRITES state — and it is the tab-close gesture, so it must never be the verb
// that fails. A re-host would spend a ~300 MB process tree to tell a run to stop.
func TestCancel_IsUnchangedByTheReHostAndStartsNoProcess(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList:    json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "aborted", ""),
	}
	if _, err := h.runs.listRaw(t.Context()); err != nil {
		t.Fatalf("warm the utility session: %v", err)
	}
	h.runs.grantLease(t.Context(), "wf_1", "nightly", manualLaunch())

	before := br.startCount()
	if err := h.runs.Cancel(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Cancel on an unhosted run = %v, want nil", err)
	}
	if got := br.startCount() - before; got != 0 {
		t.Errorf("spawns during a cancel = %d, want 0; cancel goes out on the utility "+
			"session and must not start a process to stop a run", got)
	}
	if h.bridge.mgr.get(runChatID("wf_1")) != nil {
		t.Error("cancel registered a run bridge")
	}
	if !slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Errorf("the cancel verb never went out; calls were %v", br.callLog())
	}
}

// The ruling's other half: a failed run stays openable in a subtab though nothing is
// running. The endpoint reads no lease and needs no run bridge — it goes out on the
// shared utility session and passes `state` and `nodePlan` through verbatim.
func TestHandleRun_AParkedRunsPageRendersWithNoLeaseAndNoBridge(t *testing.T) {
	h, _, br := newTestHub()
	tree, err := json.Marshal(map[string]any{
		"workflowId": "wf_1",
		"state": map[string]any{
			"status":      runStatusPaused,
			"pauseReason": "Step 'review' is waiting for user input.",
			"root": map[string]any{
				"nodeId": "review", "type": "step", "status": runStatusPaused,
			},
		},
		"nodePlan": map[string]any{"type": "sequence"},
	})
	if err != nil {
		t.Fatalf("marshal inspect: %v", err)
	}
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowInspect: tree}

	if _, held := h.runs.lease("wf_1"); held {
		t.Fatal("the fixture holds a lease, so this exercises the wrong state")
	}
	if h.bridge.mgr.get(runChatID("wf_1")) != nil {
		t.Fatal("the fixture registered a bridge, so this exercises the wrong state")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/runs/wf_1", nil)
	req.SetPathValue("id", "wf_1")
	h.runRoutes.handleRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/runs/wf_1 with no lease and no bridge = %d, want 200: %s",
			rec.Code, rec.Body.String())
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode the reply: %v", err)
	}
	for _, key := range []string{"state", "nodePlan"} {
		if _, ok := got[key]; !ok {
			t.Errorf("the reply carries no %q, so the tab has no tree to render", key)
		}
	}
}

// The mechanism that lets a lifecycle frame ask the kept-carrier bound's question with
// no timer. Asked directly, because the wait is bounded by the verb's own span and that
// is a property of this type rather than of the frame.
func TestCarrierUse_WhenIdleDefersACloseUnderALiveVerb(t *testing.T) {
	t.Run("an idle carrier closes immediately", func(t *testing.T) {
		var c carrierUse
		sb := &sharedBridge{bridge: newFakeBridge(), state: bridgeIdle}
		ran := 0
		c.whenIdle(sb, func() { ran++ })
		// SYNCHRONOUSLY, so a caller that must run the close on its own goroutine
		// (closeStoppedBridge runs from the forward loop) still controls when.
		if ran != 1 {
			t.Errorf("the close ran %d times on an idle carrier, want 1", ran)
		}
	})

	t.Run("a held carrier defers until the last verb leaves", func(t *testing.T) {
		var c carrierUse
		sb := &sharedBridge{bridge: newFakeBridge(), state: bridgeIdle}
		ran := 0
		c.enter(sb)
		c.enter(sb)
		c.whenIdle(sb, func() { ran++ })
		if ran != 0 {
			t.Fatalf("the close ran under a live verb: Stop unblocks every pending waiter "+
				"with the bridge-exited sentinel, so the verb's outcome becomes unknown (ran=%d)", ran)
		}
		c.leave(sb)
		if ran != 0 {
			t.Errorf("the close ran with one verb still holding the carrier (ran=%d)", ran)
		}
		c.leave(sb)
		if ran != 1 {
			t.Errorf("the close ran %d times after the last verb left, want 1: a deferred "+
				"close that never fires is the leak the ask was supposed to avoid", ran)
		}
	})

	// A re-registration is cheap to reach (a duplicate frame, a paused frame then a
	// terminal one) and closing twice tears down whatever a later re-host put under that
	// chat id. WHICH closer survives is not asserted: both close the same carrier.
	t.Run("two registrations still close once", func(t *testing.T) {
		var c carrierUse
		sb := &sharedBridge{bridge: newFakeBridge(), state: bridgeIdle}
		ran := 0
		c.enter(sb)
		c.whenIdle(sb, func() { ran++ })
		c.whenIdle(sb, func() { ran++ })
		c.leave(sb)
		if ran != 1 {
			t.Errorf("the close ran %d times, want 1", ran)
		}
	})

	// The entry has to go with the count, or a carrier that was closed once holds a
	// stale closer that the NEXT verb's leave would fire against a different process.
	t.Run("a fired closer is forgotten", func(t *testing.T) {
		var c carrierUse
		sb := &sharedBridge{bridge: newFakeBridge(), state: bridgeIdle}
		ran := 0
		c.enter(sb)
		c.whenIdle(sb, func() { ran++ })
		c.leave(sb)
		c.enter(sb)
		c.leave(sb)
		if ran != 1 {
			t.Errorf("the close ran %d times across two spans, want 1", ran)
		}
	})
}

// This closer and closeKeptCarrier are siblings and both must ask. Reachability is
// narrow — the response and the notification travel one stdio stream but land in two
// places, measured margin 15 ms — but Stop unblocks every pending waiter with the
// bridge-exited sentinel, so a frame inside a verb's span makes its outcome unknown.
func TestCloseStoppedBridge_AsksAboutAVerbInFlight(t *testing.T) {
	h, _, br := newTestHub()
	br.setCallResult(methodKiroWorkflowInspect, parkedInspect(
		t, runStatusPaused, needInputPauseReason, "sess_step",
	))
	held := make(chan struct{})
	br.blockOn = map[string]chan struct{}{methodKiroWorkflowUpdate: held}
	kept := &sharedBridge{bridge: br, state: bridgeIdle}
	h.bridge.mgr.insert(runChatID("wf_1"), kept)

	done := make(chan error, 1)
	go func() { done <- h.runs.SetStepStatus(t.Context(), "wf_1", "review", runStepCompleted) }()

	// The fake's own call log, never carriers.busy: waiting on the thing under
	// assertion would make the assertion vacuous.
	stop := time.Now().Add(5 * time.Second)
	for !slices.Contains(br.callLog(), methodKiroWorkflowUpdate) {
		if time.Now().After(stop) {
			close(held)
			t.Fatalf("the verb never reached %s: %v", methodKiroWorkflowUpdate, br.callLog())
		}
		time.Sleep(time.Millisecond)
	}

	h.dispatch(t.Context(), runChatID("wf_1"), runCompleteFrame(t, "wf_1", "completed"))

	// A BOUNDED negative: the close the guard suppresses is a goroutine, so absence has
	// to be observed over a window rather than at one instant.
	windowEnd := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(windowEnd) {
		if h.bridge.mgr.get(runChatID("wf_1")) == nil {
			close(held)
			t.Fatal("the carrier was closed under a verb still holding it, so that verb's " +
				"Call unblocks with the bridge-exited sentinel and its outcome is unknown")
		}
		time.Sleep(time.Millisecond)
	}

	close(held)
	if err := <-done; err != nil {
		t.Fatalf("the verb failed: %s", err)
	}
	// The other half: a deferred close that never fires is the leak the ask was
	// supposed to avoid, so the frame's decision has to survive the wait.
	if !waitForBridge(t, h, "wf_1", false) {
		t.Error("the stopped run kept its process after the verb released the carrier")
	}
}

// The identity re-check the deferral needs: a discard on a non-context error closes the
// carrier itself, so a later verb can re-host and put a DIFFERENT process under the same
// chat id while this close is still pending.
func TestCloseStoppedBridge_DoesNotCloseALaterReHostsCarrier(t *testing.T) {
	h, _, br := newTestHub()
	kept := &sharedBridge{bridge: br, state: bridgeIdle}
	h.bridge.mgr.insert(runChatID("wf_1"), kept)
	// A verb holding it, so the frame's close is deferred rather than taken.
	h.runs.carriers.enter(kept)

	h.dispatch(t.Context(), runChatID("wf_1"), runCompleteFrame(t, "wf_1", "completed"))

	// The window: the first carrier goes and a re-host registers a second one.
	h.bridge.mgr.close(runChatID("wf_1"))
	incumbent := &sharedBridge{bridge: newFakeBridge(), state: bridgeIdle}
	h.bridge.mgr.insert(runChatID("wf_1"), incumbent)

	h.runs.carriers.leave(kept)

	// BOUNDED for the same reason: without the check `leave` launches the close, and an
	// assertion read at one instant usually wins that race.
	windowEnd := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(windowEnd) {
		if got := h.bridge.mgr.get(runChatID("wf_1")); got != incumbent {
			t.Fatalf("the chat id holds %p, want the incumbent %p: a pending close must name "+
				"the carrier it was armed for, not whatever occupies its key later", got, incumbent)
		}
		time.Sleep(time.Millisecond)
	}
}
