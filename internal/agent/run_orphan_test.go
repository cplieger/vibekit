package agent

// Tests for the restart-orphan clearing. The unacceptable failure is cancelling a
// LIVE run, so most of these assert a refusal and name the population it protects.

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/runlease"
)

func kasRuns(t *testing.T, rows ...map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"runs": rows})
	if err != nil {
		t.Fatalf("marshal runs: %v", err)
	}
	return raw
}

// inspectReply builds a workflow/inspect reply in KAS's own shape, with the run status
// and the pause reason on `state`. It takes the WORKFLOW ID because the predicate
// refuses a reply that does not echo the run it asked about: a fixture omitting the id
// lets every positive case pass against the exact unsafe shape that check rejects — an
// orphan's pause state while naming some other, live run.
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

// inspectPausedWithDetail is the shape a pause KAS CLASSIFIED comes back as: the same
// reply plus `state.pauseDetail`. Reason and detail are passed separately because they
// can DISAGREE about how resumable a pause looks, which is what lets a test drive the
// wrapper sentence past the reason arm. `occurredAt` is written even though
// `pauseDetail` no longer declares it: the fixture describes the WIRE, and KAS sends it.
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

// The pause a step inside a PARALLEL BRANCH produces, both halves, verbatim from run
// wf_b724fd55e6cea1e7 (2026-09-04) and re-read off the stock KAS 2.21.0 bundle. The
// sentence matches NONE of the reason arms and the detail is byte-identical to a plain
// step's, because `executeParallel` composes the sentence FROM the branch's detail.
const branchWrapperReason = "Parallel 'phase1' is waiting on branch 'live-verify' " +
	"(branch paused on transient error EAI_AGAIN)."

// branchWaitReason is the SAME wrapper with no cause in it. An interruption, a permanent
// failure and a need-input park all reach the run as exactly this sentence, so it names
// NONE of them — which is what makes it prove the detail arm rather than the reason arm.
const branchWaitReason = "Parallel 'phase1' is waiting on branch 'live-verify'."

func transientDetail() pauseDetail {
	return pauseDetail{
		Class: transientErrorClass,
		Code:  "EAI_AGAIN",
	}
}

// TestRestartPaused_AcceptsOnlyKASsOwnRestartLiteral: at least five KAS sites set a
// pause reason and only ONE means the owning process died. A deliberate pause, a policy
// stop, a step waiting for input and a torn plan all report the same `paused` status,
// and cancelling any of them destroys work somebody is coming back to. So the comparison
// is against the literal, and anything else leaves the run alone.
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

	// The reply must be ABOUT the run asked about and must still say paused: the caller
	// cancels the workflow id from the LEASE, so either half missing would authorise
	// cancelling a live run on somebody else's — or an expired — pause state.
	t.Run("the reply must be about this run and still say paused", func(t *testing.T) {
		for name, reply := range map[string]json.RawMessage{
			"a reply naming a DIFFERENT run": inspectPaused(t, "wf_other", stalePauseReason),
			"a reply naming an empty run":    inspectPaused(t, "", stalePauseReason),
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

	// The request side of the same identity check: an empty id would match a reply that
	// carries no workflowId, the one shape that decodes to an empty string.
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

// TestSweepOrphanedRuns_NeverTouchesARunItDoesNotOwn is the UNACCEPTABLE-FAILURE test,
// and why the predicate is a conjunction of two narrow conditions: each case is a run
// that is genuinely LIVE or genuinely somebody else's, and each would be cancelled by a
// predicate widened in one plausible direction — the KAS list alone, dropping the origin
// exclusion, `status == paused`, or the lease condition alone. Bridge presence is
// deliberately unused: after a restart NO run has one, so it would cancel all four.
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
			// none, so supplying one would let a bridge-presence predicate pass.
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

// TestSweepOrphanedRuns_ClearsTheRunARestartOrphaned is the positive case. Without it
// the schedule wedged permanently: KAS reports the run `paused`, the resume sweep only
// reaches runs inside a chat's session chain, and `paused` is not terminal, so the
// single-run rule refused every later slot forever.
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

// TestSweepOrphanedRuns_ReleasesTheLeaseOfARunThatIsOver is bookkeeping rather than the
// orphan path: there is nothing to cancel, so nothing is recorded. It matters because a
// lease outliving its run makes the recipe look like vibekit's own business to the
// admission backstop, which would then spend an inspect on every launch.
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

// TestSweepOrphanedRuns_ReleasesAnAgentOriginLeaseWhoseRunIsOver is the bookkeeping arm
// applied to the agent origin, and the mechanism behind "terminal runs are absent from
// GET /api/runs/live". The agent exclusion exists to avoid destroying an agent's work and
// this arm cancels nothing, so with the exclusion above BOTH arms an agent-origin lease
// whose terminal frame was missed was permanent — and the lease carries the launching
// chat, so it held that chat exempt from client eviction forever.
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

// TestSweepOrphanedRuns_LeavesEveryLeaseAloneWhenTheListFails: at boot the likeliest
// cause is kiro-cli still installing, and the admission backstop is the second chance.
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

// TestSweepOrphanedRuns_KeepsTheLeaseWhenTheCancelFails: freeing the lease would leave
// the KAS row paused with nothing left to explain it, so admission would refuse forever,
// where keeping it means the next launch retries the clear. It also pins what the ROW
// says — recording the reason before the cancel and never taking it back made History
// render the run as ended (a recognised end reason outranks live status in history.ts)
// for a run still paused in KAS. An ending that did not happen must not be announced.
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

// TestClearOrphanedRun_RefusesWhenTheRunNoLongerReadsAsAnOrphan is the check-to-cancel
// window, narrowed. Both callers establish "orphan" from an earlier read and then cancel;
// re-asking immediately before the cancel shrinks the gap to one RPC round trip, which is
// as far as it goes — KAS exposes no compare-and-cancel and no state token `cancel` will
// honour. What makes the remainder safe is that nothing vibekit owns can resume a run this
// function reaches: Resume needs the run's own `run:<id>` bridge, which a restart destroys.
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

// TestRecipeIdle_ClearsABlockingOrphanAndProceeds is the admission backstop, which exists
// because a run can be orphaned without a restart: its own bridge can die mid-session.
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

// TestRecipeIdle_StillRefusesEveryBlockingRowItCannotExplain is why admission keeps
// reading KAS's list rather than the leases: that list is the only thing that sees the two
// populations vibekit does not launch, so a lease-only admission would make an
// agent-launched and a TUI-launched run invisible to the single-run rule.
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
// candidate lease sequentially, so a budget below the per-call timeout would cancel the
// first call rather than bounding the sweep.
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

// TestResumablePause_CoversEveryInvoluntaryPauseAndNothingElse pins the boundary the
// resume sweep may act on. KAS records a pause for about thirteen causes in three groups —
// involuntary, waiting on a human, stopped by policy — and only the first may be resumed
// without asking, so a reason drifting into the wrong group either strands a run forever
// or restarts one somebody parked on purpose. The network reason is matched by PREFIX, so
// the negative cases include the shapes a loose prefix would swallow.
func TestResumablePause_CoversEveryInvoluntaryPauseAndNothingElse(t *testing.T) {
	transient := transientDetail()

	for name, tc := range map[string]struct {
		detail *pauseDetail
		reason string
		want   bool
	}{
		// --- The REASON arm, every case with NO detail ---
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

		// The shapes a careless prefix match would swallow. They carry no detail so the
		// second arm cannot answer for them and the prefix stays tested.
		"no reason at all":                           {nil, "", false},
		"the network phrase mid-sentence":            {nil, "Step failed: Transient connection error (EAI_AGAIN)", false},
		"the network phrase without its parenthesis": {nil, "Transient connection error EAI_AGAIN", false},
		"a permanent connection failure":             {nil, "Permanent connection error (ENOTFOUND); the run failed.", false},
		"the interruption literal truncated":         {nil, "Step interrupted (agent shutdown or connection reset)", false},
		"the restart literal in different case":      {nil, "interrupted by agent restart; the previously running step was paused for resume.", false},

		// --- The DETAIL arm, every case with a reason NO arm accepts ---
		// KAS's executeParallel composes the sentence from the branch's own detail, so
		// it matches nothing above and the detail is a plain step's byte for byte.
		"a transient fault inside a parallel branch": {&transient, branchWrapperReason, true},
		// The same detail under a sentence nobody has seen: a third KAS code path may
		// word it a third way and the class still decides.
		"a classified fault under prose no arm knows": {&transient, "Something upstream re-worded this.", true},
		// A classified fault with NO prose at all. The frame carries the class, so
		// the absence of a sentence is not the absence of a verdict.
		"a classified fault with no reason at all": {&transient, "", true},

		// The detail arm is a CLASS match, not a presence check: a pause KAS classified
		// as anything else is somebody's decision.
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
		// The needInput-inside-a-parallel-branch shape: the wrapper sentence with NO
		// detail. It must stay false, and it proves the detail arm did not widen the
		// predicate to the wrapper SENTENCE. See run_ask.go for the signal that closes it.
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

// TestResumablePause_IsStrictlyWiderThanTheCancelPredicate is the asymmetry, and the whole
// reason these are two functions. The orphan sweep CANCELS on its predicate so it may only
// fire when the owning process died; the resume sweep RESUMES so it may fire for any
// involuntary stop. Widening the cancel side destroys work; narrowing the resume side
// stranded six live runs.
func TestResumablePause_IsStrictlyWiderThanTheCancelPredicate(t *testing.T) {
	// Both predicates are driven over the SAME inspect fixture, so this asserts the
	// relationship between the two rules rather than restating either.
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

	// The DETAIL half of the asymmetry: the detail arm licenses a RESUME and must never
	// license a CANCEL. `restartPaused` takes no detail at all, so this is by
	// construction — and the way it would be lost is threading the detail in for symmetry.
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

	// The same fixture with the detail REMOVED, the wrapper a need-input park (and an
	// interruption, and a permanent failure) produces. Neither predicate may touch it: it
	// is not the restart literal, and the sentence carries no verdict at all.
	t.Run("the branch wrapper with no detail is neither resumable nor cancellable", func(t *testing.T) {
		cancel, resume := both(t, inspectPaused(t, "wf_1", branchWaitReason))
		if cancel || resume {
			t.Errorf("restartPaused = %v, involuntarilyPaused = %v for a detail-less parallel "+
				"wrapper; want both false, because that one sentence covers a need-input park, "+
				"an interruption and a permanent failure alike", cancel, resume)
		}
	})
}

// TestInvoluntarilyPaused_KeepsItsSiblingsThreeConditions: the reason predicate is wider
// than restartPaused's and nothing else about the check is. The status is re-read off THIS
// reply because a pause reason outlives its pause, so acting on the reason alone would
// resume a run already executing; the identity check and the failed-RPC refusal stand.
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

// TestReleaseIfOver_ReleasesTheLeaseOfARunThatStoppedWithoutAFrame: a lease is released on
// the live path by exactly one event, a terminal `run_complete` on a bridge this process
// still reads, and a cancel is a node-boundary verb — so a run with no in-flight node has
// no boundary to reach and no such frame follows. wf_5fa90abea7328028 was cancelled at
// 2026-09-03T16:36:21Z, reached `aborted`, and 27 hours later was still on /api/runs/live
// holding its chat exempt from the client's eviction sweep.
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

// TestReleaseIfOver_RefusesEveryReplyThatDoesNotSayTheRunIsOver: each case is a lease that
// must survive. The caller asks about the workflow id from the LEASE, so a reply naming a
// different run must not decide this one's fate; the rest are runs that have not finished,
// and releasing one unbounds it (the deadline lives on the lease), silences the unattended
// permission floor, and strands a blocking row clearBlockingOrphan can no longer explain.
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

// TestReleaseIfOver_LeavesARunItCouldNotReadAlone is the recorded LIMIT, and the one case
// SweepOrphaned's first branch covers that this does not: that branch reads the run LIST,
// where an absent id positively states KAS has no such run, while `inspect` reports the
// same condition as an ERROR indistinguishable in KIND from a bridge that did not answer.
// So it takes the conservative direction — a lease left behind costs memory and one chat's
// eviction exemption, a lease released under a live run unbounds it.
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

// TestReleaseIfOver_AsksNothingWhenThereIsNoLeaseToRelease: the early return is what makes
// the reconcile a NO-OP for a run whose terminal frame won the race, and why the check sits
// before the RPC rather than inside the release.
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

// TestSweepOrphaned_ReportsWhetherItReachedKAS: the verdict exists so the composition root
// can retry the ONE failure a caller can act on. Before it travelled, `run list
// unavailable` was terminal for the process — seven boots in ten days met a kiro-cli still
// installing and kept every stale lease for the whole process life. An empty lease store
// reports REACHED deliberately: a retry would find the same emptiness.
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
