package agent

// Restart orphans: the runs vibekit launched, whose owning process died, and
// which nothing else would ever clear.
//
// The failure this closes is permanent: KAS reconciles a dead owner's run to
// `paused`, the resume sweep only reaches runs inside a chat's session chain
// (and a scheduled run is parentless), and `paused` is not terminal — so the
// single-run rule refused every later slot of that recipe forever.
//
// TWO clearing paths for one condition, because they answer different
// questions. The BOOT sweep answers "is this system idle"; the ADMISSION
// backstop answers "may this run start" — a run can be orphaned without a
// restart, because its own bridge can die mid-session and nothing else would
// notice. They share one release function.
//
// NO AUTOMATIC RELAUNCH (user decision). The stated recovery is a manual run
// before the next slot; auto-relaunching would carry work forward across a
// restart, which the closed-loop ruling rejects.

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/workflow"
)

// runStatusPaused is KAS's one non-terminal status a stopped run reports, and the
// first half of the orphan predicate.
const runStatusPaused = "paused"

// orphanSweepBudget bounds the whole boot sweep.
//
// The per-call timeout is not enough on its own: the sweep issues one
// `inspect` per candidate lease, sequentially, on one utility bridge whose
// own timeout is 45s — so a wedged bridge could hold a boot goroutine for
// minutes.
const orphanSweepBudget = 2 * time.Minute

// SweepOrphaned clears every lease whose run a dead process left paused, so
// that after boot nothing reads as live unless it genuinely is.
//
// Runs in the background on the runtime's own shutdown context. The whole
// sweep is best-effort: skipping an orphan costs one stale row until the next
// launch attempt releases it, while cancelling a live run destroys work.
// Every branch resolves in that direction.
func (rs *Runs) SweepOrphaned(ctx context.Context) {
	held := rs.leaseStore().List()
	if len(held) == 0 {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, orphanSweepBudget)
	defer cancel()

	runs, err := rs.listRaw(cctx)
	if err != nil {
		// Leave every lease alone. The admission backstop is the second chance,
		// and it runs with a bridge that answered.
		slog.Warn("boot: run list unavailable, skipping the orphan sweep", "error", err)
		return
	}
	status := make(map[string]string, len(runs))
	for i := range runs {
		status[runs[i].WorkflowID] = runs[i].Status
	}
	for i := range held {
		l := held[i]
		st, known := status[l.WorkflowID]
		switch {
		case !known || terminalRunStatus(st):
			// Bookkeeping only: there is no run to cancel, so this records no
			// reason. A lease outliving its run would otherwise make the
			// recipe look busy to the backstop and hold that chat exempt from
			// client eviction for the rest of the process.
			slog.Info("boot: releasing the lease of a run that is over",
				"workflow_id", l.WorkflowID, "recipe", l.Recipe, "status", st)
			rs.releaseLease(cctx, l.WorkflowID)
		case l.Origin == runlease.OriginAgent:
			// Chat-parented by construction: KAS parents an agent's run on the
			// calling chat's session, so it heals WITH that chat when the
			// user's next message rehydrates its bridge.
		case st == runStatusPaused && rs.restartPaused(cctx, l.WorkflowID):
			rs.clearOrphaned(cctx, &l)
		}
	}
}

// clearBlockingOrphan is the admission backstop: it answers whether a row
// blocking a launch is an orphan vibekit itself owns, and clears it if so.
//
// This is the whole reason admission still reads KAS's run list rather than
// the leases: that list is the only thing that sees the populations vibekit
// does not launch — an agent's run and the TUI's. A lease-only admission
// would make both invisible to the single-run rule. What the lease adds is
// the ability to EXPLAIN a row admission would otherwise have to refuse
// blindly.
func (rs *Runs) clearBlockingOrphan(ctx context.Context, workflowID, status string) bool {
	if status != runStatusPaused {
		// A running row is not an orphan, whatever else is true of it.
		return false
	}
	l, held := rs.lease(workflowID)
	if !held || l.Origin == runlease.OriginAgent {
		// Not vibekit's own to clear. A run with no lease was launched by the TUI
		// or by a build that did not keep leases, and an agent's run belongs to
		// its chat.
		return false
	}
	if !rs.restartPaused(ctx, workflowID) {
		return false
	}
	return rs.clearOrphaned(ctx, &l)
}

// clearOrphaned is the release both paths share: claim the termination,
// re-confirm the run is still an orphan, cancel, and only then say so and
// give up the lease.
//
// The lease is released only when the cancel LANDED. A failed cancel leaves
// the KAS row paused, and freeing the lease then would strand that row with
// nothing left to explain it.
//
// The REASON is recorded only after the cancel landed, opposite the order
// finishTermination uses: an orphan's failed cancel means the run did NOT
// end, and recording `orphaned` there would make History claim an ending
// that never happened.
//
// THE CHECK-TO-CANCEL WINDOW is accepted rather than closed. KAS exposes no
// compare-and-cancel, so no amount of re-reading makes the test and the
// cancel atomic. What makes the residual window safe is that the population
// which could resume inside it is EMPTY in practice: the sweep only ever
// touches a lease vibekit minted (excluding TUI and agent-launched runs),
// vibekit owns no verb that can resume a bridge-less run, and the
// chat-parented resume sweep is scoped to a chat's session chain, which a
// parentless run is not in. Do not "fix" this by widening the predicate or
// dropping the auto-cancel without first checking whether KAS has gained a
// conditional cancel.
func (rs *Runs) clearOrphaned(ctx context.Context, l *runlease.Lease) bool {
	if !rs.claimTermination(l.WorkflowID) {
		// Something is already ending it. Not this path's run to clear, and not a
		// reason to refuse the launch either — the winner will release the lease.
		return false
	}
	if !rs.restartPaused(ctx, l.WorkflowID) {
		// It stopped being an orphan between the caller's read and now. Hand the
		// claim back: whatever is true of the run now, this is not the path that
		// gets to end it.
		rs.releaseTermination(l.WorkflowID)
		slog.Info("a run stopped reading as restart-orphaned before its cancel; leaving it alone",
			"workflow_id", l.WorkflowID, "recipe", l.Recipe)
		return false
	}
	rs.disarmDeadline(ctx, l.WorkflowID)
	if err := rs.cancelRPC(ctx, l.WorkflowID); err != nil {
		rs.releaseTermination(l.WorkflowID)
		slog.Error("could not cancel a restart-orphaned run; its recipe stays busy until the next try",
			"workflow_id", l.WorkflowID, "error", err)
		return false
	}
	rs.recordEnd(l.WorkflowID, runEndOrphaned)
	// ERROR for the same reason the other two bounds log at ERROR: an unattended
	// run cut off by a restart is a failure a homelab Loki rule should be able to
	// key on, and the schedule row only tells the user once they look.
	slog.Error(logMsgRunOrphaned, "workflow_id", l.WorkflowID, "recipe", l.Recipe,
		"origin", string(l.Origin), "schedule_id", l.ScheduleID)
	rs.releaseLease(ctx, l.WorkflowID)
	return true
}

// restartPaused reports whether KAS says a run's owning PROCESS died, as
// opposed to the run having been paused for any of the other reasons that
// share the `paused` status.
//
// THREE conditions on one reply, and every one of them is load-bearing.
//
// The pause REASON is where the process-died distinction lives — a
// deliberate pause, a policy stop, a step waiting for input and a torn plan
// all read `paused` too, and every one of them must be left alone. This is
// the same gate involuntarilyPaused reads, inverted in action.
//
// The STATUS is read off the same reply rather than inherited from the
// caller's older `workflow/list` row, because a pause reason outlives the
// pause.
//
// The IDENTITY check refuses a reply naming a different run — the one
// unacceptable failure in this mechanism — since the caller cancels the
// workflow id from the LEASE.
//
// FALSE on any RPC failure, never "assume dead": a skipped orphan costs one
// stale row, a wrongly cancelled run costs the work.
func (rs *Runs) restartPaused(ctx context.Context, workflowID string) bool {
	if workflowID == "" {
		return false
	}
	res, ok := rs.inspect(ctx, workflowID)
	if !ok {
		return false
	}
	return res.WorkflowID == workflowID &&
		res.State.Status == runStatusPaused &&
		res.State.PauseReason == stalePauseReason
}

// involuntarilyPaused reports whether a paused run stopped for a cause nobody
// chose, and is therefore vibekit's to resume without being asked.
//
// The resume-side sibling of restartPaused: only the reason predicate is
// wider, and resumablePause (run_host.go) is where that asymmetry is argued.
//
// FALSE on any RPC failure, same as its sibling.
func (rs *Runs) involuntarilyPaused(ctx context.Context, workflowID string) bool {
	if workflowID == "" {
		return false
	}
	res, ok := rs.inspect(ctx, workflowID)
	if !ok {
		return false
	}
	return res.WorkflowID == workflowID &&
		res.State.Status == runStatusPaused &&
		resumablePause(res.State.PauseReason)
}

// inspectRunState is the part of `_kiro/workflow/inspect`'s reply the orphan
// predicate reads: `{workflowId, state, nodePlan}` with the run status,
// inputs, artifacts and node tree on `state`. Deliberately its own minimal
// decode — the full structure passes through GET /api/runs/{id} verbatim.
type inspectRunState struct {
	State struct {
		Status      string `json:"status"`
		PauseReason string `json:"pauseReason"`
	} `json:"state"`
	WorkflowID string `json:"workflowId"`
}

// inspect reads one run's inspect reply, reporting false when it cannot be
// read or decoded at all.
func (rs *Runs) inspect(ctx context.Context, workflowID string) (inspectRunState, bool) {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		slog.Warn("could not read a paused run's state, so it is left alone",
			"workflow_id", workflowID, "error", err)
		return inspectRunState{}, false
	}
	var res inspectRunState
	if json.Unmarshal(raw, &res) != nil {
		return inspectRunState{}, false
	}
	return res, true
}

// rawInspect issues `_kiro/workflow/inspect` for one run and TYPES its failure
// at the boundary: an unregistered verb comes back wrapping
// workflow.ErrUnknownMethod, so callers ask errors.Is instead of re-reading
// KAS's error text.
func (rs *Runs) rawInspect(ctx context.Context, workflowID string) (json.RawMessage, error) {
	u := rs.utility()
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()
	raw, err := u.session.rawCall(cctx, "workflow inspect call", methodKiroWorkflowInspect,
		callerParams(map[string]any{keyWorkflowID: workflowID}))
	if err != nil {
		return nil, workflow.Classify(err)
	}
	return raw, nil
}
