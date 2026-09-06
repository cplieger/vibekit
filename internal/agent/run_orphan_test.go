package agent

// Tests for the restart-orphan clearing. The unacceptable failure here is
// cancelling a LIVE run, so most of these assert a refusal rather than an action,
// and each one names the population it protects.

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/runlease"
)

// kasRuns builds a workflow/list reply.
func kasRuns(t *testing.T, rows ...map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"runs": rows})
	if err != nil {
		t.Fatalf("marshal runs: %v", err)
	}
	return raw
}

// inspectReply builds a workflow/inspect reply in KAS's own shape:
// `{workflowId, state, nodePlan}`, with the run status and the pause reason on
// `state`.
//
// It takes the WORKFLOW ID because the predicate refuses a reply that does not
// echo the run it asked about. A fixture that omitted the id — as this one used to
// — let every positive case pass against exactly the unsafe response shape that
// check exists to reject: a reply carrying an orphan's pause state while naming
// some other, live run. Every predicate test was therefore permitting the failure
// it was written to prevent.
func inspectReply(t *testing.T, workflowID, status, reason string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"workflowId": workflowID,
		"state":      map[string]any{"status": status, "pauseReason": reason},
	})
	if err != nil {
		t.Fatalf("marshal inspect: %v", err)
	}
	return raw
}

// inspectPaused is the ordinary positive shape: this run, paused, for this reason.
func inspectPaused(t *testing.T, workflowID, reason string) json.RawMessage {
	t.Helper()
	return inspectReply(t, workflowID, runStatusPaused, reason)
}

// inspectPausedWithDetail is the shape a pause KAS CLASSIFIED comes back as: the
// same reply plus `state.pauseDetail`.
//
// It exists because the reason and the detail can DISAGREE about how resumable a
// pause looks, and only that combination reproduces the run this whole mechanism
// came from: a step inside a parallel branch parks with a matching reason, that
// reason is written to a shallow copy of the run state and discarded, and what
// reaches the run is a wrapper sentence composed from the detail. Passing them
// separately is what lets a test drive the wrapper sentence past the reason arm.
// `occurredAt` is written even though `pauseDetail` no longer declares it: the
// fixture describes the WIRE, and KAS sends the field. An unknown key decoding
// harmlessly is exactly the property that let it come off the predicate path.
func inspectPausedWithDetail(t *testing.T, workflowID, reason string, d pauseDetail) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"workflowId": workflowID,
		"state": map[string]any{
			"status":      runStatusPaused,
			"pauseReason": reason,
			"pauseDetail": map[string]any{
				"class": d.Class, "code": d.Code,
				"occurredAt": "2026-09-04T12:03:43.000Z",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal inspect: %v", err)
	}
	return raw
}

// The pause a step inside a PARALLEL BRANCH produces, both halves, verbatim from
// the run this mechanism was built for (wf_b724fd55e6cea1e7, 2026-09-04) and
// re-read off the stock KAS 2.21.0 bundle.
//
// The sentence matches NONE of the reason arms — that is the point of it — and the
// detail is byte-identical to what a plain step's pause carries, because
// `executeParallel` reads the branch's detail and composes the sentence FROM it.
const branchWrapperReason = "Parallel 'phase1' is waiting on branch 'live-verify' " +
	"(branch paused on transient error EAI_AGAIN)."

// branchWaitReason is the SAME wrapper with no cause in it: the sentence KAS
// composes for a parallel branch that has stopped, when the branch's own pause
// carried no detail to compose one from.
//
// It is named for what it holds rather than for any one of the states it covers.
// An interruption, a permanent failure and a need-input park all reach the run as
// exactly this sentence, so a name claiming one of the three would be wrong for
// the other two — and the reason the fixture exists is that the sentence names
// NONE of them, which is what makes it prove the detail arm rather than the
// reason arm.
const branchWaitReason = "Parallel 'phase1' is waiting on branch 'live-verify'."

func transientDetail() pauseDetail {
	return pauseDetail{
		Class: transientErrorClass,
		Code:  "EAI_AGAIN",
	}
}

// TestRestartPaused_AcceptsOnlyKASsOwnRestartLiteral is the orphan predicate's
// first half, and the narrowest thing in this change.
//
// At least five KAS sites set a pause reason and only ONE means the owning process
// died. A deliberate pause, a policy stop, a step waiting for input and a torn
// plan all report the same `paused` status, and cancelling any of them destroys
// work somebody is coming back to. So the comparison is against the literal, and
// anything else — including an empty reason, a prefix of the literal, and a failed
// RPC — leaves the run alone.
func TestRestartPaused_AcceptsOnlyKASsOwnRestartLiteral(t *testing.T) {
	for name, tc := range map[string]struct {
		reason string
		want   bool
	}{
		"KAS's restart literal":         {stalePauseReason, true},
		"a deliberate pause":            {"Paused by user request", false},
		"a policy stop":                 {"Maximum iterations reached", false},
		"a step waiting for input":      {"Waiting for user input", false},
		"a torn plan":                   {"Plan was modified during execution", false},
		"no reason at all":              {"", false},
		"a prefix of the literal":       {"Interrupted by agent restart;", false},
		"the literal with a suffix":     {stalePauseReason + " (retry 2)", false},
		"the literal in different case": {"interrupted by agent restart; the previously running step was paused for resume.", false},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowInspect: inspectPaused(t, "wf_1", tc.reason),
			}
			if got := h.runs.restartPaused(t.Context(), "wf_1"); got != tc.want {
				t.Errorf("restartPaused(%q) = %v, want %v", tc.reason, got, tc.want)
			}
		})
	}

	t.Run("a failed inspect leaves the run alone", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callErrs = map[string]error{methodKiroWorkflowInspect: errRecipeBusy}
		if h.runs.restartPaused(t.Context(), "wf_1") {
			t.Error("an unreadable pause reason was treated as a dead process; at boot the " +
				"likeliest cause is that kiro-cli is still installing")
		}
	})

	t.Run("an undecodable inspect reply leaves the run alone", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowInspect: json.RawMessage(`{"state":`),
		}
		if h.runs.restartPaused(t.Context(), "wf_1") {
			t.Error("a malformed inspect reply was treated as a dead process")
		}
	})

	// The reply must be ABOUT the run that was asked about, and it must still say
	// paused. Both halves guard the same unacceptable failure from different sides:
	// the caller cancels the workflow id from the LEASE, so a reply that names a
	// different run, or that names this one in a state it has already left, would
	// authorise the cancel of a live run on the strength of somebody else's — or an
	// expired — pause state.
	t.Run("the reply must be about this run and still say paused", func(t *testing.T) {
		for name, reply := range map[string]json.RawMessage{
			"a reply naming a DIFFERENT run": inspectPaused(t, "wf_other", stalePauseReason),
			"a reply naming an empty run":    inspectPaused(t, "", stalePauseReason),
			// The exact shape the old fixture produced: no workflowId key at all.
			"a reply naming no run at all": json.RawMessage(
				`{"state":{"status":"paused","pauseReason":"` + stalePauseReason + `"}}`,
			),
			"a run KAS says is running":   inspectReply(t, "wf_1", "running", stalePauseReason),
			"a run KAS says completed":    inspectReply(t, "wf_1", "completed", stalePauseReason),
			"a reply carrying no status":  inspectReply(t, "wf_1", "", stalePauseReason),
			"a status in different case":  inspectReply(t, "wf_1", "Paused", stalePauseReason),
			"nothing but the pause state": json.RawMessage(`{"state":{"pauseReason":"x"}}`),
		} {
			t.Run(name, func(t *testing.T) {
				h, _, br := newTestHub()
				br.callResults = map[string]json.RawMessage{methodKiroWorkflowInspect: reply}
				if h.runs.restartPaused(t.Context(), "wf_1") {
					t.Errorf("reply %s was accepted as proof that wf_1's process died", reply)
				}
			})
		}
	})

	// And the request side of the same identity check: an empty id would otherwise
	// match a reply that carries no workflowId, which is the one shape that decodes
	// to an empty string.
	t.Run("an empty workflow id is never asked about", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowInspect: json.RawMessage(
				`{"state":{"status":"paused","pauseReason":"` + stalePauseReason + `"}}`,
			),
		}
		if h.runs.restartPaused(t.Context(), "") {
			t.Error("the empty workflow id read as a dead process")
		}
		if len(br.callLog()) != 0 {
			t.Errorf("the empty workflow id was put on the wire: %v", br.callLog())
		}
	})
}

// TestSweepOrphanedRuns_NeverTouchesARunItDoesNotOwn is the UNACCEPTABLE-FAILURE
// test, and the reason the predicate is a conjunction of two narrow conditions
// rather than one broad one.
//
// Each case below is a run that is genuinely LIVE, or genuinely somebody else's,
// and each one would be cancelled at boot by a predicate widened in a plausible
// direction:
//
//   - a run with no lease is the TUI's. Sweeping on the KAS list alone reaches it.
//   - an agent-launched run IS leased (it needs the ceiling) but is chat-parented,
//     so it belongs to the chat rehydrate's resume sweep. Dropping the origin
//     exclusion reaches it.
//   - a paused-by-policy run satisfies every condition except the literal.
//     Sweeping on `status == paused` reaches it.
//   - a RUNNING leased run satisfies the lease condition alone.
//   - and the signal this deliberately does NOT use is bridge presence: after a
//     restart NO run has a bridge, so a missing-bridge predicate cancels every
//     one of these four at once.
func TestSweepOrphanedRuns_NeverTouchesARunItDoesNotOwn(t *testing.T) {
	for name, tc := range map[string]struct {
		lease  *runlease.Lease
		status string
		reason string
	}{
		"a TUI-launched run, which has no lease": {
			lease: nil, status: runStatusPaused, reason: stalePauseReason,
		},
		"an agent-launched run, which its chat resumes": {
			lease:  &runlease.Lease{WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginAgent},
			status: runStatusPaused, reason: stalePauseReason,
		},
		"a run paused by a policy stop": {
			lease:  &runlease.Lease{WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginManual},
			status: runStatusPaused, reason: "Maximum iterations reached",
		},
		"a run paused by a person on purpose": {
			lease:  &runlease.Lease{WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginScheduled},
			status: runStatusPaused, reason: "Paused by user request",
		},
		"a run that is still running": {
			lease:  &runlease.Lease{WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginScheduled},
			status: "running", reason: "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowList: kasRuns(t, map[string]any{
					"workflowId": "wf_1", "name": "publish", "status": tc.status,
				}),
				methodKiroWorkflowInspect: inspectPaused(t, "wf_1", tc.reason),
				methodKiroWorkflowCancel:  json.RawMessage(`{}`),
			}
			if tc.lease != nil {
				if err := h.runs.leaseStore().Put(t.Context(), tc.lease); err != nil {
					t.Fatalf("Put: %v", err)
				}
			}
			// A live bridge is deliberately absent for every case: a restart leaves
			// none, so a fixture that supplied one would let a bridge-presence
			// predicate pass and this test would stop guarding against it.
			if h.bridge.mgr.get(runChatID("wf_1")) != nil {
				t.Fatal("the fixture registered a bridge; the sweep must be wrong-by-default without one")
			}

			h.runs.SweepOrphaned(t.Context())

			if got := h.runs.endReason("wf_1"); got != "" {
				t.Errorf("the sweep recorded %q against a run it does not own", got)
			}
			for _, m := range br.callLog() {
				if m == methodKiroWorkflowCancel {
					t.Fatal("the sweep CANCELLED a live run; this is the failure the predicate " +
						"is narrow to prevent")
				}
			}
			if tc.lease != nil {
				if _, held := h.runs.lease("wf_1"); !held {
					t.Error("the sweep released the lease of a run it must leave alone")
				}
			}
		})
	}
}

// TestSweepOrphanedRuns_ClearsTheRunARestartOrphaned is the positive case: both
// conditions hold, so the run is cancelled, the row is told what happened, and the
// recipe goes idle.
//
// Without this the schedule wedged permanently — KAS reports the run `paused`, the
// resume sweep only reaches runs inside a chat's session chain, `paused` is not
// terminal, so the single-run rule refused every later slot forever.
func TestSweepOrphanedRuns_ClearsTheRunARestartOrphaned(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "name": "nightly", "status": runStatusPaused,
		}),
		methodKiroWorkflowInspect: inspectPaused(t, "wf_1", stalePauseReason),
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
	}
	if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_1", Recipe: "nightly", Origin: runlease.OriginScheduled,
		ScheduleID: "sched-1", Unattended: true,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	h.runs.SweepOrphaned(t.Context())

	var cancelled bool
	for _, m := range br.callLog() {
		if m == methodKiroWorkflowCancel {
			cancelled = true
		}
	}
	if !cancelled {
		t.Errorf("no cancel went out for the orphan: %v", br.callLog())
	}
	// Its own reason, not `cancelled`: a user cancel, a blown deadline, a step-cap
	// trip and a restart orphan are four different facts and the row names which.
	if got := h.runs.endReason("wf_1"); got != runEndOrphaned {
		t.Errorf("the orphan recorded %q, want %q", got, runEndOrphaned)
	}
	if _, held := h.runs.lease("wf_1"); held {
		t.Error("the cleared orphan kept its lease, so the recipe still reads as vibekit's own")
	}
}

// TestSweepOrphanedRuns_ReleasesTheLeaseOfARunThatIsOver is bookkeeping rather
// than the orphan path: there is nothing to cancel, so nothing is recorded.
//
// It matters because a lease outliving its run makes the recipe look like
// vibekit's own business to the admission backstop, which would then spend an
// inspect on every launch to learn the run is finished.
func TestSweepOrphanedRuns_ReleasesTheLeaseOfARunThatIsOver(t *testing.T) {
	for name, rows := range map[string][]map[string]any{
		"KAS reports it completed": {{"workflowId": "wf_1", "name": "publish", "status": "completed"}},
		"KAS does not know it":     {},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowList:   kasRuns(t, rows...),
				methodKiroWorkflowCancel: json.RawMessage(`{}`),
			}
			if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
				WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginManual,
			}); err != nil {
				t.Fatalf("Put: %v", err)
			}

			h.runs.SweepOrphaned(t.Context())

			if _, held := h.runs.lease("wf_1"); held {
				t.Error("the lease of a finished run survived the sweep")
			}
			if got := h.runs.endReason("wf_1"); got != "" {
				t.Errorf("a finished run was recorded as %q; nothing was cancelled", got)
			}
		})
	}
}

// TestSweepOrphanedRuns_ReleasesAnAgentOriginLeaseWhoseRunIsOver is the
// bookkeeping arm applied to the origin it used to skip, and the mechanism
// behind "terminal runs are absent from GET /api/runs/live".
//
// The agent exclusion exists to avoid destroying an agent's work, and the
// bookkeeping arm cancels nothing — it releases the lease of a run KAS reports
// terminal or unknown. When the exclusion sat above BOTH arms, an agent-origin
// lease whose terminal frame was missed (the launching chat's bridge was torn
// down by a close, delete or model-switch fallback before the frame arrived)
// was permanent: presence stopped meaning "non-terminal", and with the lease
// carrying the launching chat it would hold that chat exempt from client
// eviction forever.
func TestSweepOrphanedRuns_ReleasesAnAgentOriginLeaseWhoseRunIsOver(t *testing.T) {
	for name, rows := range map[string][]map[string]any{
		"KAS reports it completed": {{"workflowId": "wf_1", "name": "publish", "status": "completed"}},
		"KAS does not know it":     {},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowList:   kasRuns(t, rows...),
				methodKiroWorkflowCancel: json.RawMessage(`{}`),
			}
			if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
				WorkflowID: "wf_1", Recipe: "publish", ChatID: "c-live", Origin: runlease.OriginAgent,
			}); err != nil {
				t.Fatalf("Put: %v", err)
			}

			h.runs.SweepOrphaned(t.Context())

			if _, held := h.runs.lease("wf_1"); held {
				t.Error("an agent-origin lease outlived its run; the recipe reads busy and the " +
					"launching chat stays exempt from eviction until the next boot")
			}
			// The exclusion still guards the CANCEL: bookkeeping must not have
			// turned into an ending.
			if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
				t.Fatal("the sweep CANCELLED an agent's run while releasing its lease")
			}
			if got := h.runs.endReason("wf_1"); got != "" {
				t.Errorf("a finished run was recorded as %q; nothing was cancelled", got)
			}
		})
	}
}

// TestSweepOrphanedRuns_LeavesEveryLeaseAloneWhenTheListFails: at boot the
// likeliest cause is that kiro-cli is still installing. The admission backstop is
// the second chance, and it runs with a bridge that answered.
func TestSweepOrphanedRuns_LeavesEveryLeaseAloneWhenTheListFails(t *testing.T) {
	h, _, br := newTestHub()
	br.callErrs = map[string]error{methodKiroWorkflowList: errRecipeBusy}
	if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginScheduled,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	h.runs.SweepOrphaned(t.Context())

	if _, held := h.runs.lease("wf_1"); !held {
		t.Error("an unreadable run list released a lease, so a live run's envelope is gone")
	}
	for _, m := range br.callLog() {
		if m == methodKiroWorkflowCancel {
			t.Fatal("the sweep cancelled a run it could not see the status of")
		}
	}
}

// TestSweepOrphanedRuns_KeepsTheLeaseWhenTheCancelFails is the failure direction
// that would otherwise recreate the bug this closes: freeing the lease would leave
// the KAS row paused with nothing left to explain it, so admission would refuse
// forever. Keeping it means the next launch attempt retries the clear.
//
// It also pins what the ROW says, which the lease assertion alone missed. The
// reason used to be recorded before the cancel was issued and was never taken back
// when it failed, so History rendered the run as "the server restarted while it was
// running" — a recognised end reason outranks live status in history.ts — for a run
// that is still paused in KAS and which this code is about to try again. An ending
// that did not happen must not be announced.
func TestSweepOrphanedRuns_KeepsTheLeaseWhenTheCancelFails(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "name": "publish", "status": runStatusPaused,
		}),
		methodKiroWorkflowInspect: inspectPaused(t, "wf_1", stalePauseReason),
	}
	br.callErrs = map[string]error{methodKiroWorkflowCancel: errRecipeBusy}
	if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginManual,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	h.runs.SweepOrphaned(t.Context())

	if _, held := h.runs.lease("wf_1"); !held {
		t.Error("a failed cancel released the lease, stranding a paused KAS row with nothing " +
			"left to explain it")
	}
	if got := h.runs.endReason("wf_1"); got != "" {
		t.Errorf("the row reads %q after a cancel that never landed; the run is still paused "+
			"in KAS and the next admission attempt retries the clear, so History must not "+
			"already say it was stopped", got)
	}
	// And the claim is back, or the retry this lease was kept for cannot happen.
	if !h.runs.claimTermination("wf_1") {
		t.Error("the failed cancel kept the termination claim, so nothing can clear the orphan")
	}
}

// TestClearOrphanedRun_RefusesWhenTheRunNoLongerReadsAsAnOrphan is the
// check-to-cancel window, narrowed.
//
// Both callers establish "orphan" from an earlier read — the boot sweep from a
// `workflow/list` row taken before its whole sequential pass, the admission backstop
// from the list it was refusing a launch over — and then cancel. Re-asking
// immediately before the cancel shrinks the gap to one RPC round trip, which is as
// far as it can be shrunk: KAS exposes no compare-and-cancel and no state token
// `cancel` will honour, so the test and the cancel cannot be made atomic.
//
// What makes the remainder safe is that nothing vibekit owns can resume a run this
// function can reach — Resume needs the run's own `run:<id>` bridge, which a
// restart is precisely what destroys, and the chat-parented resume sweep is scoped
// to a chat's session chain that a parentless run is not in. This test pins the
// second read itself: without it, a run that stopped reading as an orphan is
// cancelled anyway.
func TestClearOrphanedRun_RefusesWhenTheRunNoLongerReadsAsAnOrphan(t *testing.T) {
	for name, reply := range map[string]json.RawMessage{
		"it is executing again":       inspectReply(t, "wf_1", "running", stalePauseReason),
		"it was paused on purpose":    inspectPaused(t, "wf_1", "Paused by user request"),
		"the reply names another run": inspectPaused(t, "wf_other", stalePauseReason),
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowInspect: reply,
				methodKiroWorkflowCancel:  json.RawMessage(`{}`),
			}
			l := runlease.Lease{WorkflowID: "wf_1", Recipe: "publish", Origin: runlease.OriginManual}
			if err := h.runs.leaseStore().Put(t.Context(), &l); err != nil {
				t.Fatalf("Put: %v", err)
			}

			if h.runs.clearOrphaned(t.Context(), &l) {
				t.Error("a run that no longer reads as an orphan was cleared")
			}
			if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
				t.Errorf("a cancel went out for a run that is not an orphan: %v", br.callLog())
			}
			if got := h.runs.endReason("wf_1"); got != "" {
				t.Errorf("the row reads %q for a run nothing ended", got)
			}
			if _, held := h.runs.lease("wf_1"); !held {
				t.Error("the lease of a run that was left alone was released")
			}
			// The claim goes back, or the run can never be ended by anything again.
			if !h.runs.claimTermination("wf_1") {
				t.Error("the refusal kept the termination claim, so no bound and no Cancel " +
					"button can ever act on this run")
			}
		})
	}
}

// TestRecipeIdle_ClearsABlockingOrphanAndProceeds is the admission backstop, which
// exists because a run can be orphaned without a restart: its own bridge can die
// mid-session, and nothing else would notice.
func TestRecipeIdle_ClearsABlockingOrphanAndProceeds(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_old", "name": "publish", "status": runStatusPaused,
		}),
		methodKiroWorkflowInspect: inspectPaused(t, "wf_old", stalePauseReason),
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
	}
	if err := h.runs.leaseStore().Put(t.Context(), &runlease.Lease{
		WorkflowID: "wf_old", Recipe: "publish", Origin: runlease.OriginScheduled,
		ScheduleID: "sched-1", Unattended: true,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := h.runs.recipeIdle(t.Context(), "publish"); err != nil {
		t.Fatalf("admission refused a launch over an orphan it owns: %v", err)
	}
	if got := h.runs.endReason("wf_old"); got != runEndOrphaned {
		t.Errorf("the cleared orphan recorded %q, want %q", got, runEndOrphaned)
	}
	if _, held := h.runs.lease("wf_old"); held {
		t.Error("the cleared orphan kept its lease")
	}
}

// TestRecipeIdle_StillRefusesEveryBlockingRowItCannotExplain is the other half,
// and the reason admission keeps reading KAS's list rather than the leases.
//
// That list is the only thing that sees the two populations vibekit does not
// launch. A lease-only admission would make an agent-launched and a TUI-launched
// run invisible to the single-run rule, so a second live run of one recipe could
// start.
func TestRecipeIdle_StillRefusesEveryBlockingRowItCannotExplain(t *testing.T) {
	for name, tc := range map[string]struct {
		lease  *runlease.Lease
		status string
		reason string
	}{
		"a TUI-launched run, unleased": {
			lease: nil, status: runStatusPaused, reason: stalePauseReason,
		},
		"an agent-launched run": {
			lease:  &runlease.Lease{WorkflowID: "wf_old", Recipe: "publish", Origin: runlease.OriginAgent},
			status: runStatusPaused, reason: stalePauseReason,
		},
		"a leased run that is still running": {
			lease:  &runlease.Lease{WorkflowID: "wf_old", Recipe: "publish", Origin: runlease.OriginManual},
			status: "running", reason: "",
		},
		"a leased run paused on purpose": {
			lease:  &runlease.Lease{WorkflowID: "wf_old", Recipe: "publish", Origin: runlease.OriginManual},
			status: runStatusPaused, reason: "Paused by user request",
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowList: kasRuns(t, map[string]any{
					"workflowId": "wf_old", "name": "publish", "status": tc.status,
				}),
				methodKiroWorkflowInspect: inspectPaused(t, "wf_old", tc.reason),
				methodKiroWorkflowCancel:  json.RawMessage(`{}`),
			}
			if tc.lease != nil {
				if err := h.runs.leaseStore().Put(t.Context(), tc.lease); err != nil {
					t.Fatalf("Put: %v", err)
				}
			}

			if err := h.runs.recipeIdle(t.Context(), "publish"); err == nil {
				t.Fatal("admission allowed a second live run of one recipe")
			}
			for _, m := range br.callLog() {
				if m == methodKiroWorkflowCancel {
					t.Fatal("admission cancelled a run it cannot explain")
				}
			}
		})
	}
}

// TestOrphanSweepBudget_ExceedsThePerCallTimeout: the sweep issues one inspect per
// candidate lease, sequentially, on one utility bridge. A budget below the per-call
// timeout would cancel the first call rather than bounding the sweep.
func TestOrphanSweepBudget_ExceedsThePerCallTimeout(t *testing.T) {
	t.Parallel()
	if orphanSweepBudget <= sessionListTimeout {
		t.Errorf("orphanSweepBudget = %v, not longer than one call's %v, so the sweep would "+
			"time out before its first inspect could answer", orphanSweepBudget, sessionListTimeout)
	}
	if orphanSweepBudget > 5*time.Minute {
		t.Errorf("orphanSweepBudget = %v; a boot goroutine holding a utility bridge that long "+
			"is a stall, not a sweep", orphanSweepBudget)
	}
}

// TestResumablePause_CoversEveryInvoluntaryReasonAndNothingElse pins the boundary
// the resume sweep is allowed to act on.
//
// KAS records a pause for about thirteen different causes and they fall into three
// groups. Involuntary: the reconcile's restart literal, an interruption, a
// transient network code, a transient model 5xx. Waiting on a human: a step that
// asked for input, a step waiting for the next message. Stopped by policy: a
// repeat at maxIterations, a recorded failure. Only the first group may be resumed
// without asking, so a reason that drifts into the wrong group here either strands
// a run forever or restarts one somebody parked on purpose.
//
// The network reason is matched by PREFIX, which is the one place this could go
// wrong quietly, so the negative cases include the shapes a loose prefix would
// swallow.
//
// TWO ARMS since 2026-09, and the table drives both from the same fixture. Every
// reason case carries a nil detail, which is the shape a pause KAS did not
// classify arrives as — so those rows still pin the reason arm on its own and a
// detail arm that swallowed everything would not rescue them. The detail rows
// then carry a reason NONE of the arms accept, which is what makes them prove the
// detail arm rather than the reason arm.
func TestResumablePause_CoversEveryInvoluntaryPauseAndNothingElse(t *testing.T) {
	transient := transientDetail()

	for name, tc := range map[string]struct {
		detail *pauseDetail
		reason string
		want   bool
	}{
		// --- The REASON arm, every case with NO detail --------------------------
		//
		// Involuntary: nobody chose this, and KAS's own text says so.
		"the reconcile's restart literal": {nil, stalePauseReason, true},
		"an interrupted step":             {nil, interruptedPauseReason, true},
		"a transient model 5xx":           {nil, modelServicePauseReason, true},
		"a transient network code":        {nil, "Transient connection error (EAI_AGAIN); the run is paused and can be resumed.", true},
		"a different network code":        {nil, "Transient connection error (ECONNRESET); the run is paused and can be resumed.", true},

		// Waiting on a human. Resuming these would answer a question nobody asked.
		"a step that asked for input":   {nil, "Step requested user input via send_message.", false},
		"a step awaiting the next turn": {nil, "Step 'review' is waiting for the next user message.", false},
		"a step awaiting user input":    {nil, "Step 'design' is waiting for user input.", false},

		// Stopped by policy or already over. Resuming these overrides a decision.
		"a repeat at maxIterations":   {nil, "Repeat 'implement' reached maxIterations.", false},
		"a repeat aborted at the cap": {nil, "Repeat 'implement' aborted at maxIterations.", false},
		"a recorded failure":          {nil, "Run failed: the reviewer never approved", false},
		"a deliberate pause":          {nil, "Paused by user request", false},

		// The shapes a careless prefix match would swallow. These must stay false
		// on the REASON arm specifically, which is why every one carries no detail:
		// a detail here would let the second arm answer for them and the prefix
		// would stop being tested at all.
		"no reason at all":                           {nil, "", false},
		"the network phrase mid-sentence":            {nil, "Step failed: Transient connection error (EAI_AGAIN)", false},
		"the network phrase without its parenthesis": {nil, "Transient connection error EAI_AGAIN", false},
		"a permanent connection failure":             {nil, "Permanent connection error (ENOTFOUND); the run failed.", false},
		"the interruption literal truncated":         {nil, "Step interrupted (agent shutdown or connection reset)", false},
		"the restart literal in different case":      {nil, "interrupted by agent restart; the previously running step was paused for resume.", false},

		// --- The DETAIL arm, every case with a reason NO arm accepts ------------
		//
		// The run this whole mechanism came from. The sentence is composed by
		// KAS's executeParallel from the branch's own detail, so it matches
		// nothing above — and the detail beside it is byte-identical to what a
		// plain step's pause carries.
		"a transient fault inside a parallel branch": {&transient, branchWrapperReason, true},
		// The same detail under a sentence nobody has seen, which is the point of
		// reading the structured field: a third KAS code path may word it a third
		// way and the class still decides.
		"a classified fault under prose no arm knows": {&transient, "Something upstream re-worded this.", true},
		// A classified fault with NO prose at all. The frame carries the class, so
		// the absence of a sentence is not the absence of a verdict.
		"a classified fault with no reason at all": {&transient, "", true},

		// The detail arm is a CLASS match, not a presence check. A pause KAS
		// classified as anything else is somebody's decision, and the detail must
		// not be read as "this pause is involuntary" just because it exists.
		"a permanent fault carrying a detail": {
			&pauseDetail{Class: "permanent", Code: "ENOTFOUND"},
			branchWaitReason, false,
		},
		"a detail with an empty class": {
			&pauseDetail{Code: "EAI_AGAIN"},
			branchWaitReason, false,
		},
		"a detail whose class is a prefix of the transient one": {
			&pauseDetail{Class: "transient", Code: "EAI_AGAIN"},
			branchWaitReason, false,
		},
		// The needInput-inside-a-parallel-branch shape: the wrapper sentence with
		// NO detail, because a need-input park sets none. It must stay false —
		// resuming it would answer a question nobody asked — and it is the case
		// that proves the detail arm did not widen the predicate to the wrapper
		// SENTENCE. See run_ask.go for why closing that hole needs its own signal.
		"a need-input park inside a parallel branch": {nil, branchWaitReason, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := resumablePause(tc.reason, tc.detail); got != tc.want {
				t.Errorf("resumablePause(%q, %+v) = %v, want %v",
					tc.reason, tc.detail, got, tc.want)
			}
		})
	}
}

// TestResumablePause_IsStrictlyWiderThanTheCancelPredicate is the asymmetry, and
// it is the whole reason these are two functions instead of one.
//
// The orphan sweep CANCELS on its predicate, so it may only fire when the owning
// process died; the resume sweep RESUMES, so it may fire for any involuntary stop.
// Widening the cancel side would destroy work — `clearOrphaned` carries a standing
// instruction against exactly that — and narrowing the resume side is what
// stranded six live runs. So every reason the narrow predicate accepts must also
// pass the wide one, and the wide one must accept strictly more.
func TestResumablePause_IsStrictlyWiderThanTheCancelPredicate(t *testing.T) {
	// Both predicates are driven over the SAME inspect fixture, so this asserts the
	// relationship between the two live rules rather than restating either of them.
	both := func(t *testing.T, reply json.RawMessage) (cancel, resume bool) {
		t.Helper()
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{methodKiroWorkflowInspect: reply}
		return h.runs.restartPaused(t.Context(), "wf_1"), h.runs.involuntarilyPaused(t.Context(), "wf_1")
	}

	t.Run("everything the cancel side accepts, the resume side accepts too", func(t *testing.T) {
		// Otherwise a restart-paused run would be cancellable by the orphan sweep and
		// never resumable by its own chat, which is the worst of both rules.
		cancel, resume := both(t, inspectPaused(t, "wf_1", stalePauseReason))
		if !cancel || !resume {
			t.Errorf("restartPaused = %v, involuntarilyPaused = %v for KAS's restart literal; want both true",
				cancel, resume)
		}
	})

	for name, reason := range map[string]string{
		"an interrupted step":      interruptedPauseReason,
		"a transient model 5xx":    modelServicePauseReason,
		"a transient network code": "Transient connection error (EAI_AGAIN); the run is paused and can be resumed.",
	} {
		t.Run(name+" is resumable but never cancellable", func(t *testing.T) {
			cancel, resume := both(t, inspectPaused(t, "wf_1", reason))
			if cancel {
				t.Errorf("restartPaused = true for %q; widening the CANCEL side destroys work "+
					"a resume would have saved (see clearOrphaned)", reason)
			}
			if !resume {
				t.Errorf("involuntarilyPaused = false for %q; this is the class that stranded "+
					"six live runs", reason)
			}
		})
	}

	// The DETAIL half of the asymmetry, and the guard against the worst regression
	// available here: the detail arm licenses a RESUME and must never license a
	// CANCEL. `restartPaused` does not take a detail at all, so this is a
	// by-construction property — which is exactly why it is worth an assertion,
	// because the way it would be lost is somebody threading the detail into the
	// second predicate for symmetry.
	t.Run("a classified transient fault is resumable and never cancellable", func(t *testing.T) {
		cancel, resume := both(t, inspectPausedWithDetail(t, "wf_1", branchWrapperReason, transientDetail()))
		if cancel {
			t.Error("restartPaused = true for a pause carrying only a transient-error DETAIL; " +
				"the cancel side reads KAS's restart literal and nothing else, and widening it " +
				"cancels work a resume would have saved")
		}
		if !resume {
			t.Error("involuntarilyPaused = false for a transient fault inside a parallel branch; " +
				"this is the run the whole detail arm exists for")
		}
	})

	// The same fixture with the detail REMOVED, which is the wrapper sentence a
	// need-input park (and an interruption, and a permanent failure) produces.
	// Neither predicate may touch it: the cancel side because it is not the restart
	// literal, the resume side because the sentence carries no verdict at all.
	t.Run("the branch wrapper with no detail is neither resumable nor cancellable", func(t *testing.T) {
		cancel, resume := both(t, inspectPaused(t, "wf_1", branchWaitReason))
		if cancel || resume {
			t.Errorf("restartPaused = %v, involuntarilyPaused = %v for a detail-less parallel "+
				"wrapper; want both false, because that one sentence covers a need-input park, "+
				"an interruption and a permanent failure alike", cancel, resume)
		}
	})
}

// TestInvoluntarilyPaused_KeepsItsSiblingsThreeConditions.
//
// The reason predicate is wider than restartPaused's, and nothing else about the
// check is. The status is still re-read off THIS reply, because a pause reason
// outlives its pause: a run resumed after the caller's inventory row was taken
// still carries the reason that parked it, and acting on the reason alone would
// resume a run that is already executing. The identity check still refuses a reply
// naming another run, and a failed RPC still means no.
func TestInvoluntarilyPaused_KeepsItsSiblingsThreeConditions(t *testing.T) {
	transient := "Transient connection error (EAI_AGAIN); the run is paused and can be resumed."

	t.Run("an involuntary pause on this paused run is resumable", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowInspect: inspectPaused(t, "wf_1", transient),
		}
		if !h.runs.involuntarilyPaused(t.Context(), "wf_1") {
			t.Error("involuntarilyPaused = false for a transient-network pause on this very run")
		}
	})

	for name, reply := range map[string]json.RawMessage{
		"a reply naming a DIFFERENT run": inspectPaused(t, "wf_other", transient),
		"a run KAS says is running":      inspectReply(t, "wf_1", "running", transient),
		"a run KAS says completed":       inspectReply(t, "wf_1", "completed", transient),
		"a reply carrying no status":     inspectReply(t, "wf_1", "", transient),
	} {
		t.Run(name+" is not resumable", func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{methodKiroWorkflowInspect: reply}
			if h.runs.involuntarilyPaused(t.Context(), "wf_1") {
				t.Errorf("involuntarilyPaused = true for %s", name)
			}
		})
	}

	t.Run("a failed inspect leaves the run paused", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callErrs = map[string]error{methodKiroWorkflowInspect: errRecipeBusy}
		if h.runs.involuntarilyPaused(t.Context(), "wf_1") {
			t.Error("an unreadable pause reason was treated as resumable")
		}
	})

	t.Run("an empty workflow id is not resumable", func(t *testing.T) {
		h, _, _ := newTestHub()
		if h.runs.involuntarilyPaused(t.Context(), "") {
			t.Error("involuntarilyPaused(\"\") = true")
		}
	})
}

// TestReleaseIfOver_ReleasesTheLeaseOfARunThatStoppedWithoutAFrame is the
// MEASURED defect, and the cancel of a PAUSED run is the shape that produced it.
//
// A lease is released on the live path by exactly one event — a terminal
// `run_complete` delivered on a bridge this process still reads — and a cancel is
// a node-boundary verb, so a run with no in-flight node has no boundary to reach
// and no such frame follows. wf_5fa90abea7328028 was cancelled through its chat's
// close at 2026-09-03T16:36:21Z, reached `aborted`, and 27 hours later was still
// on /api/runs/live holding its chat exempt from the client's eviction sweep.
func TestReleaseIfOver_ReleasesTheLeaseOfARunThatStoppedWithoutAFrame(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "aborted", ""),
	}
	leased(t, h.runs, "wf_1")

	h.runs.releaseIfOver(t.Context(), "wf_1")

	if _, held := h.runs.lease("wf_1"); held {
		t.Error("the lease outlived a run KAS reports as aborted, so /api/runs/live " +
			"keeps advertising it and its chat can never be evicted")
	}
}

// TestReleaseIfOver_RefusesEveryReplyThatDoesNotSayTheRunIsOver is the refusal
// half, and each case is a lease that must survive.
//
// The identity case is the one the whole guard exists for: the caller asks about
// the workflow id from the LEASE, so a reply naming a different run must not
// decide this one's fate. The rest are runs that have not finished — and
// releasing one of those unbounds it (the deadline lives on the lease), silences
// the unattended permission floor, and strands a blocking row that
// clearBlockingOrphan can no longer explain.
func TestReleaseIfOver_RefusesEveryReplyThatDoesNotSayTheRunIsOver(t *testing.T) {
	for name, reply := range map[string]json.RawMessage{
		"a reply naming a DIFFERENT run": inspectReply(t, "wf_other", "aborted", ""),
		"a run KAS says is running":      inspectReply(t, "wf_1", "running", ""),
		"a run KAS says is paused":       inspectPaused(t, "wf_1", stalePauseReason),
		"a reply carrying no status":     inspectReply(t, "wf_1", "", ""),
	} {
		t.Run(name+" keeps the lease", func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{methodKiroWorkflowInspect: reply}
			leased(t, h.runs, "wf_1")

			h.runs.releaseIfOver(t.Context(), "wf_1")

			if _, held := h.runs.lease("wf_1"); !held {
				t.Errorf("the lease was released for %s", name)
			}
		})
	}
}

// TestReleaseIfOver_LeavesARunItCouldNotReadAlone is the recorded LIMIT, not an
// oversight, and it is the one case SweepOrphaned's first branch covers that this
// does not.
//
// That branch reads the run LIST, where an absent id is a positive statement that
// KAS has no such run. `inspect` reports the same condition as an ERROR —
// `Workflow '<id>' is not registered and has no persisted state in any known
// workspace.`, read off the stock 2.21.0 bundle — which is indistinguishable in
// KIND from a bridge that did not answer, and the only thing separating them is
// KAS's own error prose. So the conservative direction is the one every predicate
// in this file takes: a lease left behind costs memory and one chat's eviction
// exemption, while a lease released under a live run unbounds it and silences the
// unattended floor. The boot sweep still reaches it.
func TestReleaseIfOver_LeavesARunItCouldNotReadAlone(t *testing.T) {
	h, _, br := newTestHub()
	br.callErrs = map[string]error{methodKiroWorkflowInspect: errRecipeBusy}
	leased(t, h.runs, "wf_1")

	h.runs.releaseIfOver(t.Context(), "wf_1")

	if _, held := h.runs.lease("wf_1"); !held {
		t.Error("an unreadable inspect released the lease; a failed RPC must never " +
			"be read as a terminal run")
	}
}

// TestReleaseIfOver_AsksNothingWhenThereIsNoLeaseToRelease: the early return is
// what makes the reconcile a NO-OP for a run whose terminal frame won the race,
// and it is why the check sits before the RPC rather than inside the release.
//
// The running-run case reaches it too: forgetBounds released the lease on the
// terminal frame, so there is nothing left to inspect.
func TestReleaseIfOver_AsksNothingWhenThereIsNoLeaseToRelease(t *testing.T) {
	for name, id := range map[string]string{
		"a run whose lease is already gone": "wf_1",
		"an empty workflow id":              "",
	} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowInspect: inspectReply(t, id, "aborted", ""),
			}

			h.runs.releaseIfOver(t.Context(), id)

			if slices.Contains(br.callLog(), methodKiroWorkflowInspect) {
				t.Errorf("%s put an inspect on the wire; the reconcile costs one RPC per "+
					"DELIBERATE stop and none on any other path", name)
			}
		})
	}
}

// TestSweepOrphaned_ReportsWhetherItReachedKAS is Fix B's half of the contract.
//
// The verdict exists so the composition root can retry the ONE failure a caller
// can do something about. Before it travelled, `run list unavailable` was terminal
// for the process: seven boots in ten days met a kiro-cli still installing, skipped
// the sweep, and kept every stale lease for the whole process life.
//
// An empty lease store reports REACHED, deliberately: there was nothing to ask
// about, and a retry would find the same emptiness.
func TestSweepOrphaned_ReportsWhetherItReachedKAS(t *testing.T) {
	t.Run("a run list that answered", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowList: kasRuns(t, map[string]any{
				"workflowId": "wf_1", "name": "nightly", "status": "running",
			}),
		}
		leased(t, h.runs, "wf_1")

		if !h.runs.SweepOrphaned(t.Context()) {
			t.Error("a sweep that read the run list reported unreached, so the caller " +
				"pays for a retry it does not need")
		}
	})

	t.Run("a run list the utility bridge could not serve", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callErrs = map[string]error{
			methodKiroWorkflowList: errors.New("utility bridge start: kiro-cli is not available yet"),
		}
		leased(t, h.runs, "wf_1")

		if h.runs.SweepOrphaned(t.Context()) {
			t.Error("a sweep that never read the run list reported reached; this is the " +
				"one failure the caller retries, and it would be silently dropped")
		}
		if _, held := h.runs.lease("wf_1"); !held {
			t.Error("the sweep touched a lease without reading the run list")
		}
	})

	t.Run("no leases at all", func(t *testing.T) {
		h, _, _ := newTestHub()
		if !h.runs.SweepOrphaned(t.Context()) {
			t.Error("an empty lease store reported unreached, so every boot with no runs " +
				"parks a goroutine waiting for an install it has no use for")
		}
	})
}
