package agent

// Restart orphans: the runs vibekit launched, whose owning process died, and
// which nothing else would ever clear.
//
// The failure this closes is a permanent one. KAS reconciles a dead owner's run
// to `paused`, the resume sweep only reaches runs inside a chat's session chain
// (and a scheduled run is parentless), and `paused` is not terminal — so the
// single-run rule refused every later slot of that recipe forever. The mechanism
// whose stated job was to break exactly this was a `time.AfterFunc` that died
// with the process.
//
// TWO clearing paths for one condition, deliberately, because they answer
// different questions. The BOOT sweep answers "is this system idle": after boot,
// nothing reads as live that is not genuinely running, which is the promise a
// restart is supposed to keep. The ADMISSION backstop answers "may this run
// start": a run can be orphaned without a restart, because its own bridge can die
// mid-session and nothing else would notice. They share one release function,
// which is what keeps them from disagreeing — the same shape claimTermination
// gives the ending paths.
//
// NO AUTOMATIC RELAUNCH (user decision). The stated recovery is a manual run
// before the next slot, and that affordance already exists on the recipe row.
// Auto-relaunching a job whose previous attempt was cut mid-step would carry work
// forward across a restart, which is what the closed-loop ruling rejects.

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
// The per-call timeout is not enough on its own: the sweep issues one `inspect`
// per candidate lease, sequentially, on one utility bridge whose own timeout is
// 45s — so a wedged bridge could hold a boot goroutine for minutes. Bounding the
// sweep rather than raising the per-call limit keeps a slow first call from
// deciding how long the rest may take.
const orphanSweepBudget = 2 * time.Minute

// SweepOrphaned clears every lease whose run a dead process left paused, so
// that after boot nothing reads as live unless it genuinely is.
//
// Runs in the background on the runtime's own shutdown context. At boot the most
// likely reason an RPC fails is that kiro-cli is still installing, and the whole
// sweep is best-effort: skipping an orphan costs one stale row until the next
// launch attempt releases it, while cancelling a live run destroys work. Every
// branch resolves in that direction.
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
		if l.Origin == runlease.OriginAgent {
			// Chat-parented by construction: KAS parents an agent's run on the
			// calling chat's session, so it heals WITH that chat when the user's
			// next message rehydrates its bridge (resumeRestartPaused). This
			// exclusion is why the sweep needs no session-chain walk.
			continue
		}
		st, known := status[l.WorkflowID]
		switch {
		case !known || terminalRunStatus(st):
			// Bookkeeping only: there is no run to cancel, so this is not the
			// orphan path and records no reason. A lease outliving its run would
			// otherwise make the recipe look busy to the backstop.
			slog.Info("boot: releasing the lease of a run that is over",
				"workflow_id", l.WorkflowID, "recipe", l.Recipe, "status", st)
			rs.releaseLease(cctx, l.WorkflowID)
		case st == runStatusPaused && rs.restartPaused(cctx, l.WorkflowID):
			rs.clearOrphaned(cctx, &l)
		}
	}
}

// clearBlockingOrphan is the admission backstop: it answers whether a row
// blocking a launch is an orphan vibekit itself owns, and clears it if so.
//
// This is the whole reason admission still reads KAS's run list rather than the
// leases. That list is the truth about what is running, and it is the ONLY thing
// that sees the two populations vibekit does not launch — an agent's run, which
// KAS parents on the calling chat's session, and the TUI's. A lease-only
// admission would make both invisible to the single-run rule, so a second live run
// of one recipe could start. What the lease adds is not a second source of truth
// but the ability to EXPLAIN a row admission would otherwise have to refuse
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
// re-confirm the run is still an orphan, cancel, and only then say so and give up
// the lease.
//
// The lease is released only when the cancel LANDED. A failed cancel leaves the
// KAS row paused, and freeing the lease then would strand that row with nothing
// left to explain it — the permanent wedge, back again. Keeping it means the next
// admission attempt retries this same clear.
//
// The REASON is recorded only after the cancel landed, which is the opposite
// order from finishTermination and the reason this composes the primitives
// instead of calling it. The other ending paths record first on purpose: a run
// that blew its ceiling breached its bound whether or not the cancel went
// through, so the row should say so. An orphan is not that — a failed cancel
// means the run did NOT end, the lease is deliberately kept, and the clear will
// be retried — so recording `orphaned` there would make History claim an ending
// that never happened (a recognised end reason outranks live status in
// history.ts) for a run this code is about to try again.
//
// THE CHECK-TO-CANCEL WINDOW, and why it is accepted rather than closed. The
// second restartPaused below is the LAST thing before the cancel, so the gap
// between "KAS says this run's process is dead" and "cancel" is one RPC round
// trip rather than a whole sequential sweep. It cannot be closed: KAS exposes no
// compare-and-cancel and no state token `cancel` will honour, so no amount of
// re-reading makes the test and the cancel atomic. What makes the residual window
// safe is that the population which could resume inside it is EMPTY in practice,
// not merely unlikely:
//
//   - the sweep only ever touches a lease vibekit minted, so a TUI-launched run
//     (no lease) and an agent-launched run (excluded by origin) are out of reach;
//   - vibekit owns no verb that can resume what is left. Resume needs the
//     run's OWN `run:<id>` bridge (hostedControl), and a restart is precisely
//     what destroys that bridge — so it answers errRunNotHosted for every run
//     this function can reach;
//   - and the chat-parented resume sweep (resumeRestartPaused) is scoped to a
//     chat's session chain, which a parentless run is not in.
//
// So the resumer would have to be another KAS client acting on a parentless run
// inside one round trip. Do not "fix" this by widening the predicate or dropping
// the auto-cancel — the ratified behaviour is to cancel and say so in History —
// and do not re-open it as a defect without first checking whether KAS has gained
// a conditional cancel.
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

// restartPaused reports whether KAS says a run's owning PROCESS died, as opposed
// to the run having been paused for any of the other reasons that share the
// `paused` status.
//
// THREE conditions on one reply, and every one of them is load-bearing.
//
// The pause REASON is where the process-died distinction lives: `workflow/list`
// carries no such field, and at least five KAS sites set one while only this
// literal means the process died. A deliberate pause, a policy stop, a step
// waiting for input and a torn plan all read `paused` too, and every one of them
// must be left alone. This is the same gate resumeIfRestartPaused reads, inverted
// in action — that one RESUMES what this one cancels — which is why it is one
// function rather than two copies of a literal comparison.
//
// The STATUS is read off the same reply rather than inherited from the caller's
// older `workflow/list` row, because a pause reason outlives the pause: a run
// resumed after that row was taken still carries the reason that parked it, and
// the reason alone would then read as "dead" for a run that is executing.
//
// The IDENTITY check is not ceremony. `inspect` answers with the run it
// inspected, and the caller cancels the workflow id from the LEASE — so a reply
// that names a different run (a mis-routed response, a stale one, a
// utility-bridge or KAS defect) would otherwise let an orphan's pause state
// authorise the cancel of a live run. That is the one unacceptable failure in
// this whole mechanism, and a field comparison is the entire cost of refusing it.
//
// FALSE on any RPC failure, never "assume dead". At boot the likeliest cause is
// that kiro-cli is still installing, and the asymmetry is total: a skipped orphan
// costs one stale row, a wrongly cancelled run costs the work.
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
// The resume-side sibling of restartPaused above, and it carries the same three
// conditions for the same three reasons — the status is re-read off THIS reply
// because a pause reason outlives its pause, and the identity check refuses a
// reply naming a different run. Only the reason predicate is wider, and
// resumablePause (run_host.go) is where that asymmetry is argued.
//
// FALSE on any RPC failure, same as its sibling. A run left paused keeps every
// door it had; the cost of guessing is a run resumed on evidence nobody read.
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
// predicate reads: `{workflowId, state, nodePlan}` with the run status, inputs,
// artifacts and node tree on `state` (probed contract, see vibekit-acp.md).
// Deliberately its own minimal decode — `nodePlan` and the node tree pass through
// GET /api/runs/{id} verbatim and re-modelling them for a predicate would be a
// second representation of a structure vibekit does not own.
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
// workflow.ErrUnknownMethod, so callers ask errors.Is instead of re-reading KAS's
// error text. One helper rather than a copy per caller because the classification
// has to happen where the RPC error still carries its `error.data` — a caller
// that wrapped first and asked second would be sniffing its own message.
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
