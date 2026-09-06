package agent

// Restart orphans: the runs vibekit launched whose owning process died. TWO clearing
// paths, answering different questions — the BOOT sweep "is this system idle", the
// ADMISSION backstop "may this run start". No automatic relaunch (user decision).

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/workflow"
)

// runStatusPaused is KAS's one non-terminal status a stopped run reports.
const runStatusPaused = "paused"

// runStatusRunning is KAS's executing status, read on NODES rather than on runs.
const runStatusRunning = "running"

// orphanSweepBudget bounds the whole boot sweep: one sequential `inspect` per candidate
// lease on a utility bridge whose own timeout is 45s could hold a boot goroutine for minutes.
const orphanSweepBudget = 2 * time.Minute

// SweepOrphaned clears every lease whose run a dead process left paused, so nothing reads
// as live after boot unless it genuinely is. Best-effort: skipping an orphan costs one
// stale row, cancelling a live run destroys work. Reports whether it REACHED KAS.
func (rs *Runs) SweepOrphaned(ctx context.Context) (reached bool) {
	held := rs.leaseStore().List()
	if len(held) == 0 {
		// No leases is not a failure to reach KAS: there was nothing to ask about.
		return true
	}
	cctx, cancel := context.WithTimeout(ctx, orphanSweepBudget)
	defer cancel()

	runs, err := rs.listRaw(cctx)
	if err != nil {
		// Leave every lease alone; the admission backstop is the second chance.
		slog.Warn("boot: run list unavailable, skipping the orphan sweep", "error", err)
		return false
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
			// Bookkeeping only — no run to cancel, so no reason recorded. A lease
			// outliving its run would make the recipe look busy to the backstop.
			slog.Info("boot: releasing the lease of a run that is over",
				"workflow_id", l.WorkflowID, "recipe", l.Recipe, "status", st)
			rs.releaseLease(cctx, l.WorkflowID)
		case l.Origin == runlease.OriginAgent:
			// Chat-parented by construction: KAS parents an agent's run on the calling
			// chat's session, so it heals WITH that chat when its bridge rehydrates.
		case st == runStatusPaused && rs.restartPaused(cctx, l.WorkflowID):
			rs.clearOrphaned(cctx, &l)
		}
	}
	return true
}

// releaseIfOver releases a run's lease when KAS says the run is over, for the one caller
// that asked a run to stop and got no confirmation. TWO conditions, no third:
// unknown-to-KAS needs none, because a LANDED cancel proved KAS resolved the id. A run it
// cannot READ is left alone, and this is a NO-OP for a running run.
func (rs *Runs) releaseIfOver(ctx context.Context, workflowID string) {
	if workflowID == "" {
		return
	}
	if _, held := rs.lease(workflowID); !held {
		// Already released; skipping the inspect is why this check is out here.
		return
	}
	res, ok := rs.inspect(ctx, workflowID)
	if !ok || res.WorkflowID != workflowID || !terminalRunStatus(res.State.Status) {
		return
	}
	slog.Info("releasing the lease of a run that stopped without a terminal frame",
		"workflow_id", workflowID, "status", res.State.Status)
	rs.releaseLease(ctx, workflowID)
}

// clearBlockingOrphan is the admission backstop: is a row blocking a launch an orphan
// vibekit itself owns, and clear it if so. Admission reads KAS's run LIST rather than the
// leases, because that list is the only thing that sees the runs vibekit did not launch.
func (rs *Runs) clearBlockingOrphan(ctx context.Context, workflowID, status string) bool {
	if status != runStatusPaused {
		// A running row is not an orphan, whatever else is true of it.
		return false
	}
	l, held := rs.lease(workflowID)
	if !held || l.Origin == runlease.OriginAgent {
		// Not vibekit's own to clear: a run with no lease came from the TUI, and an
		// agent's run belongs to its chat.
		return false
	}
	if !rs.restartPaused(ctx, workflowID) {
		return false
	}
	return rs.clearOrphaned(ctx, &l)
}

// clearOrphaned is the release both paths share: claim, re-confirm, cancel, and only then
// record the reason and give up the lease. That order AGREES with finishTermination's —
// neither records an outcome a refused cancel has not reached. THE CHECK-TO-CANCEL WINDOW
// is accepted, not closed: do not widen it or drop the auto-cancel without a
// compare-and-cancel.
func (rs *Runs) clearOrphaned(ctx context.Context, l *runlease.Lease) bool {
	if !rs.claimTermination(l.WorkflowID) {
		// Something is already ending it, and the winner will release the lease.
		return false
	}
	if !rs.restartPaused(ctx, l.WorkflowID) {
		// It stopped being an orphan between the caller's read and now. Hand the claim
		// back: whatever is true of the run, this is not the path that gets to end it.
		rs.releaseTermination(l.WorkflowID)
		slog.Info("a run stopped reading as restart-orphaned before its cancel; leaving it alone",
			"workflow_id", l.WorkflowID, "recipe", l.Recipe)
		return false
	}
	rs.disarmDeadline(ctx, l.WorkflowID)
	if err := rs.cancelRPC(ctx, l.WorkflowID, nil); err != nil {
		rs.releaseTermination(l.WorkflowID)
		slog.Error("could not cancel a restart-orphaned run; its recipe stays busy until the next try",
			"workflow_id", l.WorkflowID, "error", err)
		return false
	}
	rs.recordEnd(l.WorkflowID, runEndOrphaned)
	// ERROR because a homelab Loki rule keys on logMsgRunOrphaned.
	slog.Error(logMsgRunOrphaned, "workflow_id", l.WorkflowID, "recipe", l.Recipe,
		"origin", string(l.Origin), "schedule_id", l.ScheduleID)
	rs.releaseLease(ctx, l.WorkflowID)
	return true
}

// restartPaused reports whether KAS says a run's owning PROCESS died, as opposed to the
// other reasons that share `paused`. THREE conditions on one reply: the REASON is
// BYTE-EQUAL and never reads `pauseDetail` (the asymmetry with involuntarilyPaused, which
// resumes where this CANCELS), STATUS and IDENTITY come off that same reply, and any RPC
// failure is FALSE rather than "assume dead".
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

// involuntarilyPaused reports whether a paused run stopped for a cause nobody chose, and
// is therefore vibekit's to resume unasked. restartPaused's sibling with a wider PAUSE
// predicate, argued at resumablePause; FALSE on any RPC failure.
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
		resumablePause(res.State.PauseReason, res.State.PauseDetail)
}

// inspectRunState is the part of `_kiro/workflow/inspect`'s reply the pause predicates
// read. The DETAIL is read here because this is the re-read the heal decides on: a field
// pauseFrame's gate can see and this guard cannot would pass a run and then decline it.
type inspectRunState struct {
	State struct {
		PauseDetail *pauseDetail `json:"pauseDetail"`
		Status      string       `json:"status"`
		PauseReason string       `json:"pauseReason"`
	} `json:"state"`
	WorkflowID string `json:"workflowId"`
}

// inspect reads one run's inspect reply, false when it cannot be read or decoded at all.
func (rs *Runs) inspect(ctx context.Context, workflowID string) (inspectRunState, bool) {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		slog.Warn("could not read a paused run's state, so it is left alone",
			"workflow_id", workflowID, "error", err)
		return inspectRunState{}, false
	}
	var res inspectRunState
	if !unmarshalKeepingReadable(raw, &res) {
		return inspectRunState{}, false
	}
	return res, true
}

// rawInspect issues `_kiro/workflow/inspect` and TYPES its failure at the boundary, so
// callers ask errors.Is rather than re-reading KAS's error text.
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
