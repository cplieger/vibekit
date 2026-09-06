package agent

// Restart orphans: the runs vibekit launched, whose owning process died, and
// which nothing else would ever clear.
//
// TWO clearing paths for one condition, because they answer different questions.
// The BOOT sweep answers "is this system idle"; the ADMISSION backstop answers
// "may this run start", for a bridge that died without a restart. They share one
// release function, and there is NO automatic relaunch (user decision).
// Reasoning and the five-conjunct predicate: vibekit-runtime.md.

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

// runStatusRunning is KAS's executing status. Read on NODES rather than on runs, by
// statusUpdateTarget (run_host.go), whose running-first precedence is the resolver's.
const runStatusRunning = "running"

// orphanSweepBudget bounds the whole boot sweep. The per-call timeout is not
// enough on its own: one sequential `inspect` per candidate lease on one utility
// bridge whose own timeout is 45s could hold a boot goroutine for minutes.
const orphanSweepBudget = 2 * time.Minute

// SweepOrphaned clears every lease whose run a dead process left paused, so that
// after boot nothing reads as live unless it genuinely is. Best-effort throughout:
// skipping an orphan costs one stale row, cancelling a live run destroys work, and
// every branch resolves that way. It reports whether it REACHED KAS, the one
// failure its caller can act on.
func (rs *Runs) SweepOrphaned(ctx context.Context) (reached bool) {
	held := rs.leaseStore().List()
	if len(held) == 0 {
		// No leases is not a failure to reach KAS — there was nothing to ask
		// about, and a retry would find the same emptiness.
		return true
	}
	cctx, cancel := context.WithTimeout(ctx, orphanSweepBudget)
	defer cancel()

	runs, err := rs.listRaw(cctx)
	if err != nil {
		// Leave every lease alone. The admission backstop is the second chance,
		// and it runs with a bridge that answered.
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
	return true
}

// releaseIfOver releases a run's lease when KAS says the run is over, for the one
// caller that asked a run to stop and got no confirmation that it did.
//
// TWO CONDITIONS, no third: restartPaused's identity guard, then terminalRunStatus.
// Unknown-to-KAS needs none, because a LANDED cancel proved KAS resolved the id —
// that argument's residual `kind: "gone"` arm and the defect this closes are in
// vibekit-acp.md and vibekit-runtime.md. A run that cannot be READ is left alone,
// and this is a NO-OP for a running run.
func (rs *Runs) releaseIfOver(ctx context.Context, workflowID string) {
	if workflowID == "" {
		return
	}
	if _, held := rs.lease(workflowID); !held {
		// Already released — the terminal frame won the race, or another stop
		// path got there first. Skipping the inspect is the whole reason this
		// check is here rather than inside the release.
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

// clearBlockingOrphan is the admission backstop: is a row blocking a launch an
// orphan vibekit itself owns, and clear it if so.
//
// Admission reads KAS's run LIST rather than the leases, because that list is the
// only thing that sees the runs vibekit did not launch; the lease is what lets a
// blocking row be EXPLAINED rather than refused blindly.
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

// clearOrphaned is the release both paths share: claim, re-confirm, cancel, and only
// then record the reason and give up the lease.
//
// That order AGREES with finishTermination's: neither records an outcome a refused
// cancel has not reached. What still differs is narrower — the disarm runs BEFORE the
// cancel here (inert: runlease.NewStore parks every disk-loaded lease) and a refusal
// schedules no ladder. THE CHECK-TO-CANCEL WINDOW is accepted, not closed — do not
// widen it or drop the auto-cancel without a compare-and-cancel (vibekit-runtime.md).
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
	if err := rs.cancelRPC(ctx, l.WorkflowID, nil); err != nil {
		rs.releaseTermination(l.WorkflowID)
		slog.Error("could not cancel a restart-orphaned run; its recipe stays busy until the next try",
			"workflow_id", l.WorkflowID, "error", err)
		return false
	}
	rs.recordEnd(l.WorkflowID, runEndOrphaned)
	// ERROR for the reason the other two bounds are: a homelab Loki rule keys on
	// logMsgRunOrphaned, and the schedule row only tells the user once they look.
	slog.Error(logMsgRunOrphaned, "workflow_id", l.WorkflowID, "recipe", l.Recipe,
		"origin", string(l.Origin), "schedule_id", l.ScheduleID)
	rs.releaseLease(ctx, l.WorkflowID)
	return true
}

// restartPaused reports whether KAS says a run's owning PROCESS died, as opposed to
// any of the other reasons that share the `paused` status.
//
// THREE conditions on one reply, every one load-bearing. The REASON is BYTE-EQUAL and
// never reads `pauseDetail` — the asymmetry with involuntarilyPaused, which resumes
// where this CANCELS. The STATUS comes off the same reply, because a pause reason
// outlives the pause. The IDENTITY check refuses a reply naming another run, and any
// RPC failure is FALSE rather than "assume dead".
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
// restartPaused's resume-side sibling: only the PAUSE predicate is wider, and
// resumablePause is where that asymmetry is argued. FALSE on any RPC failure.
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

// inspectRunState is the part of `_kiro/workflow/inspect`'s reply the pause
// predicates read — its own minimal decode, since the full structure passes through
// GET /api/runs/{id} verbatim. The DETAIL is read here because this is the re-read
// the heal decides on: a field pauseFrame's gate can see and this guard cannot would
// pass a run and then decline it. Pointer first for govet's fieldalignment.
type inspectRunState struct {
	State struct {
		PauseDetail *pauseDetail `json:"pauseDetail"`
		Status      string       `json:"status"`
		PauseReason string       `json:"pauseReason"`
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
	if !unmarshalKeepingReadable(raw, &res) {
		return inspectRunState{}, false
	}
	return res, true
}

// rawInspect issues `_kiro/workflow/inspect` for one run and TYPES its failure at
// the boundary, so callers ask errors.Is rather than re-reading KAS's error text.
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
