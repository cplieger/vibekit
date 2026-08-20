package agent

// Tests for the restart-orphan clearing. The unacceptable failure here is
// cancelling a LIVE run, so most of these assert a refusal rather than an action,
// and each one names the population it protects.

import (
	"encoding/json"
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
