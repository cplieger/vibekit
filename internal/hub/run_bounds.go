package hub

// Run bounds: the wall clock EVERY run gets, the per-step turn cap, and the one
// field that lets a run's row say which of those stopped it.
//
// The hole this closes: `armRunDeadline` (run_host.go) is called only from
// LaunchScheduledRun, so a scheduled run was bounded by its own repeat interval
// and nothing else was bounded at all. A manual launch, a retry, a resume and an
// agent's `run_workflow` tool call could each run forever, and under the
// single-run rule one wedged run blocks every later run of the same recipe.
//
// TWO BOUNDS, NOT THREE. The run clock below and the per-step turn cap; there is
// deliberately no per-step wall clock (a step is already bounded by its turn cap
// and by the run clock above it, so a third bound catches no failure the first
// two miss) and no token budget (vibekit's per-step meter reads zero — Usage
// carries turn_count per CHAT, and a step is not a chat).
//
// NEITHER IS A SETTING, and that is the point rather than an omission: a backstop
// the user can raise stops being a backstop. There is no per-run override either,
// because launchRun is shared between the Workflows tab's Run button and the
// scheduler, so a per-run ceiling would let the bounded thing choose its own
// bound.

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/translate"
)

// runCeiling is how long any single run may execute before it is cancelled.
//
// KiroCrew's number (its `_RUN_TIMEOUT_SECS` default is 3600s) taken as the
// starting point, because it is the one comparable figure measured against real
// unattended workflow runs. A CONSTANT, deliberately: Crew clamps a configured
// value to [60s, 6h] and there is nothing to clamp here — no caller supplies one
// and no caller may.
const runCeiling = time.Hour

// The per-step cap is Crew's 200 and it lives in translate.StepTurnCap, beside
// the counter that enforces it. Tool calls rather than model turns, because a turn
// is not observable per step: KAS's nine workflow notifications carry no per-step
// turn-end frame, so a step's boundary is node_start/node_complete and everything
// between is ordinary `session/update` traffic. The runaway this catches IS a tool
// loop, so counting the loop's tool calls measures the thing rather than a proxy.

// The abnormal terminations a run's row can report, and the vocabulary the
// client's verdict branches on. A user cancel records NOTHING — its absence is
// what makes these two distinguishable from it, which is the whole point of the
// field (see api.WorkflowRun.EndReason).
const (
	runEndOverran = "overran"
	runEndStepCap = "step_cap"
)

// logMsgRunCeiling / logMsgStepCap are the messages the two bounds log under.
// ERROR for the same reason logMsgRunOverran is: a run stopped by a backstop is
// an unattended failure, and a homelab Loki rule can key on the message.
const (
	logMsgRunCeiling = "run exceeded its wall-clock ceiling; cancelling"
	logMsgStepCap    = "workflow step exceeded its turn cap; cancelling the run"
)

// maxRunEndReasons bounds the recorded-termination map.
//
// The record has to OUTLIVE the run — the History row reads it after the run
// finished, which is the only moment it is useful — so it cannot be cleared on
// the terminal frame the way the ceiling arm is. Only an abnormal termination
// writes an entry, so this is a large multiple of what a healthy container
// produces; the oldest is dropped first, which loses the reason for a run nobody
// is still looking at.
const maxRunEndReasons = 256

// runBoundsState is the hub-side half of the two bounds, plus the claim that
// arbitrates between them and the user.
//
// IN-MEMORY, and the consequence is stated rather than hidden: a restart loses
// all of it. A lost arm means a run that outlives the restart is unbounded again
// (it also loses its unattended mark, for the same reason), a lost claim means the
// first path to ask after the restart wins, and a lost reason means an
// already-finished run's row falls back to plain "aborted". A durable store for
// bounded facts about KAS's own entity would be the second state store invariant 5
// forbids.
type runBoundsState struct {
	// arms holds the live ceiling arm per run, keyed by workflow id.
	//
	// A *runCeilingArm rather than a set, and the pointer is what makes a
	// pause/resume cycle safe. `AfterFunc` cannot be un-fired, so the arm has to
	// be STOPPABLE (a set could only forget it, leaving one live timer per arm
	// until each deadline) and it has to be IDENTIFIABLE (a run paused just
	// before its ceiling and then resumed gets fresh membership, which a set
	// would hand to the old arm's callback — cancelling the resumed run after
	// only the old arm's remainder). The generation answers the second half.
	arms map[string]*runCeilingArm
	// terminating names the runs whose termination has been CLAIMED, and it is
	// the arbitration every ending path shares: user cancel, schedule deadline,
	// universal ceiling and step cap. Exactly one of them wins a run, and only
	// the winner records a reason and issues the cancel — so a genuine user
	// cancel racing a bound cannot be rewritten into a timeout in History, and
	// the deadline and the step cap cannot both record over each other.
	//
	// Dropped when the run reports terminal (forgetRunBounds) and when a retry
	// re-drives it (clearRunEnd), which is what bounds it: membership is the set
	// of runs currently terminating, not a log of runs that did.
	terminating map[string]struct{}
	// reasons maps a workflow id to why it was stopped; order is the FIFO
	// eviction queue for it.
	reasons map[string]string
	order   []string
	// gen issues arm generations. Monotonic across the whole hub rather than
	// per-run: a single counter cannot repeat a value, so no callback can ever
	// match an arm it did not create.
	gen uint64
}

// runCeilingArm is one run's live wall clock: the timer to stop, and the
// generation that says which arm a fired callback belongs to.
type runCeilingArm struct {
	timer *time.Timer
	gen   uint64
}

// armRunCeiling starts the wall clock for a run, once.
//
// AfterFunc rather than a goroutine, for armRunDeadline's reason: it parks
// nothing while waiting, and a run that ends first makes the callback a no-op
// instead of something that has to be cancelled.
//
// The timer is created UNDER the lock so the arm is never observable without it
// — a disarm that read a nil timer would forget the arm and leave the callback
// live, which is the bug the pointer exists to prevent. AfterFunc only schedules
// here; the callback runs on its own goroutine and takes this same lock, so it
// waits rather than deadlocking.
func (h *Hub) armRunCeiling(workflowID string) {
	if workflowID == "" {
		return
	}
	h.unattendedMu.Lock()
	defer h.unattendedMu.Unlock()
	if _, already := h.runBounds.arms[workflowID]; already {
		return
	}
	if h.runBounds.arms == nil {
		h.runBounds.arms = map[string]*runCeilingArm{}
	}
	h.runBounds.gen++
	gen := h.runBounds.gen
	h.runBounds.arms[workflowID] = &runCeilingArm{
		gen:   gen,
		timer: time.AfterFunc(runCeiling, func() { h.cancelCeilingRun(workflowID, gen) }),
	}
}

// disarmRunCeiling stops a run's wall clock and reports whether it held one.
//
// Stopping the timer is the load-bearing half: without it a run parked for a week
// keeps one live timer per arm, and each of those callbacks wakes up to ask about
// a run it no longer describes.
func (h *Hub) disarmRunCeiling(workflowID string) bool {
	if workflowID == "" {
		return false
	}
	h.unattendedMu.Lock()
	defer h.unattendedMu.Unlock()
	arm, ok := h.runBounds.arms[workflowID]
	if !ok {
		return false
	}
	arm.timer.Stop()
	delete(h.runBounds.arms, workflowID)
	return true
}

// takeCeilingArm consumes the arm a given generation created, and reports
// whether that arm was still the run's live one.
//
// This is the ceiling callback's whole liveness test, and it is exact rather
// than approximate. `Timer.Stop` does not halt an already-running func, so a
// callback can be in flight while its arm is dropped at a pause, at a terminal
// run_complete, or replaced by a resume's fresh arm — and the generation is what
// separates "my arm is still current" from "some LATER arm is". A run that
// completes microseconds later does not make the record wrong; a run whose arm
// was replaced is not this callback's to cancel.
func (h *Hub) takeCeilingArm(workflowID string, gen uint64) bool {
	h.unattendedMu.Lock()
	defer h.unattendedMu.Unlock()
	arm, ok := h.runBounds.arms[workflowID]
	if !ok || arm.gen != gen {
		return false
	}
	arm.timer.Stop()
	delete(h.runBounds.arms, workflowID)
	return true
}

// runCeilingArmed reports whether vibekit currently believes the run to be
// EXECUTING — the step cap's gate, which must not CONSUME the arm the way the
// ceiling's own callback does: a breach that loses the termination claim leaves
// the wall clock to whoever won it.
func (h *Hub) runCeilingArmed(workflowID string) bool {
	if workflowID == "" {
		return false
	}
	h.unattendedMu.Lock()
	defer h.unattendedMu.Unlock()
	_, ok := h.runBounds.arms[workflowID]
	return ok
}

// claimRunTermination takes a run's single termination claim, reporting true for
// the ONE caller that may end it.
//
// Four callers race for this and they are not variations of one thing: a user
// pressing Cancel, a schedule's own repeat interval, the universal wall clock,
// and a step's turn cap. Before the claim each had its own gate — the arm for two
// of them, the unattended mark for the third, nothing at all for the user — so
// two could pass simultaneously, and the second `recordRunEnd` overwrote the
// first. The user's cancel was the one that lost worst: it records nothing, so a
// bound that claimed alongside it turned a deliberate stop into a timeout on the
// History row.
func (h *Hub) claimRunTermination(workflowID string) bool {
	if workflowID == "" {
		return false
	}
	h.unattendedMu.Lock()
	defer h.unattendedMu.Unlock()
	if _, taken := h.runBounds.terminating[workflowID]; taken {
		return false
	}
	if h.runBounds.terminating == nil {
		h.runBounds.terminating = map[string]struct{}{}
	}
	h.runBounds.terminating[workflowID] = struct{}{}
	return true
}

// releaseRunTermination hands a claim back, for the one case where the winner did
// not actually terminate anything: the cancel RPC failed, so nothing is in
// flight and the next caller must be able to try. Holding a claim on a run still
// executing would make the Cancel button silently do nothing.
func (h *Hub) releaseRunTermination(workflowID string) {
	h.unattendedMu.Lock()
	defer h.unattendedMu.Unlock()
	delete(h.runBounds.terminating, workflowID)
}

// finishRunTermination is what the claim WINNER does, and the only place a run's
// reason is recorded alongside its cancel.
//
// reason empty means a user cancel: recordRunEnd ignores it, and that absence is
// what makes the two bounds distinguishable from a person (see
// api.WorkflowRun.EndReason).
func (h *Hub) finishRunTermination(ctx context.Context, workflowID, reason string) error {
	h.disarmRunCeiling(workflowID)
	h.recordRunEnd(workflowID, reason)
	err := h.cancelRunRPC(ctx, workflowID)
	if err != nil {
		h.releaseRunTermination(workflowID)
	}
	return err
}

// forgetRunBounds drops what a run that stopped executing for good no longer
// needs: its wall clock and its termination claim.
//
// The recorded REASON deliberately survives — the History row reads it after the
// run finished, which is the only moment it is useful. Nothing can act on the run
// after this: every bound's gate (a current arm for the ceiling, any arm for the
// step cap, the unattended mark for the schedule deadline) is already false.
func (h *Hub) forgetRunBounds(workflowID string) {
	h.disarmRunCeiling(workflowID)
	h.releaseRunTermination(workflowID)
}

// clearRunEnd forgets a run's recorded termination, so a RE-DRIVEN run is bounded
// again and its row stops reading as the failure it used to be.
//
// Both halves are required and for different reasons. The reason has to go
// because the client deliberately lets a recognised end_reason outrank live
// status (history.ts), so a retry of an `overran` run would render as aborted
// while it was running and after it succeeded. The claim has to go because a
// terminated run holds one, and a run holding a claim cannot be bounded or
// cancelled again — which would leave the retry with no wall clock at all.
//
// The claim is dropped BEFORE the reason lookup returns, because a user-cancelled
// run holds a claim and records no reason: keying the whole clear on a recorded
// reason would leave exactly that run unbounded on retry.
func (h *Hub) clearRunEnd(workflowID string) {
	if workflowID == "" {
		return
	}
	h.unattendedMu.Lock()
	defer h.unattendedMu.Unlock()
	delete(h.runBounds.terminating, workflowID)
	if _, ok := h.runBounds.reasons[workflowID]; !ok {
		return
	}
	delete(h.runBounds.reasons, workflowID)
	// The order slice is the eviction QUEUE for the map, so leaving a dangling
	// entry would make eviction delete a key that is already gone and let the map
	// grow past its cap.
	h.runBounds.order = slices.DeleteFunc(h.runBounds.order,
		func(id string) bool { return id == workflowID })
}

// rearmRetriedRun gives a re-driven run a clean row and a FRESH wall clock.
//
// The disarm is not redundant with the terminal state that made retry legal:
// RetryRun's already-hosted branch exists for a run aborted WITHOUT a terminal
// frame, which can still hold the arm it was launched with — and armRunCeiling is
// idempotent, so without the disarm that run would be retried under the remainder
// of its previous clock.
func (h *Hub) rearmRetriedRun(workflowID string) {
	h.clearRunEnd(workflowID)
	h.disarmRunCeiling(workflowID)
	h.armRunCeiling(workflowID)
}

// recordRunEnd notes why a run was stopped, so its row can say so.
func (h *Hub) recordRunEnd(workflowID, reason string) {
	if workflowID == "" || reason == "" {
		return
	}
	h.unattendedMu.Lock()
	defer h.unattendedMu.Unlock()
	if h.runBounds.reasons == nil {
		h.runBounds.reasons = map[string]string{}
	}
	if _, dup := h.runBounds.reasons[workflowID]; !dup {
		h.runBounds.order = append(h.runBounds.order, workflowID)
	}
	h.runBounds.reasons[workflowID] = reason
	for len(h.runBounds.order) > maxRunEndReasons {
		delete(h.runBounds.reasons, h.runBounds.order[0])
		h.runBounds.order = h.runBounds.order[1:]
	}
}

// runEndReason reports why a run was stopped, or "" for a run that ended on its
// own terms — including a user cancel, which records nothing.
func (h *Hub) runEndReason(workflowID string) string {
	h.unattendedMu.Lock()
	defer h.unattendedMu.Unlock()
	return h.runBounds.reasons[workflowID]
}

// cancelCeilingRun is the ceiling's callback: stop the run and say why.
//
// Two gates, in this order, because they answer two different questions. The arm
// generation asks whether THIS arm is still the run's — a resumed run carries a
// later one, and cancelling it here would end it after the old arm's remainder
// rather than a fresh hour. The claim then asks whether anything else is already
// ending the run.
func (h *Hub) cancelCeilingRun(workflowID string, gen uint64) {
	if !h.takeCeilingArm(workflowID, gen) {
		return
	}
	if !h.claimRunTermination(workflowID) {
		return
	}
	slog.Error(logMsgRunCeiling, "workflow_id", workflowID, "ceiling", runCeiling.String())
	h.cancelBoundedRun(workflowID, runEndOverran)
}

// StepTurnCapExceeded stops the run a runaway step belongs to. Satisfies
// translate.RunBoundsAccess.
//
// Cancelling the whole RUN is the only enforcement available, and that is a
// property of the wire rather than a choice: every C→A workflow verb vibekit can
// issue is run-scoped (`cancel` takes a workflowId), so there is no way to stop
// one step and let its run continue. The step is named on the log line, which is
// where a reader finds out WHICH step did it — the row's one field says a step
// cap did, and a second field for the node id would be a second representation of
// state vibekit does not own.
func (h *Hub) StepTurnCapExceeded(workflowID, nodeID string, turns int) {
	if !h.runCeilingArmed(workflowID) {
		// A run vibekit is not bounding is not one it may cancel: either it never
		// was armed, or it already reached a terminal state or a pause.
		return
	}
	if !h.claimRunTermination(workflowID) {
		// Already terminating — a sibling step breached first, the ceiling fired,
		// or the user cancelled. Either way this is not the cancel.
		return
	}
	slog.Error(logMsgStepCap,
		"workflow_id", workflowID, "node_id", nodeID, "turns", turns, "cap", translate.StepTurnCap)
	h.cancelBoundedRun(workflowID, runEndStepCap)
}

// cancelBoundedRun issues the cancel both bounds end in, for a caller that has
// already WON the termination claim.
//
// Not the public CancelRun, deliberately: that one claims, so a bound calling it
// would race itself and refuse its own cancel. Reported on failure rather than
// retried: the run breached its bound whether or not the cancel landed, and a row
// left reading "running" is exactly the silence these bounds exist to end.
func (h *Hub) cancelBoundedRun(workflowID, reason string) {
	ctx, cancel := h.hubContext()
	defer cancel()
	if err := h.finishRunTermination(ctx, workflowID, reason); err != nil {
		slog.Error("could not cancel a run that breached its bound",
			"workflow_id", workflowID, "error", err)
	}
}

// observeRunStart arms the ceiling, then hands the frame to the translator.
//
// `run_start` is the arming point that covers the launch path vibekit does not
// own: KAS's RunWorkflowTool creates and invokes an agent-launched run internally
// and parents it on the calling chat's session, so this frame is the FIRST thing
// vibekit sees of it. The launch verbs arm too, so a run this process started is
// bounded from the instant it started rather than from whenever its frame
// arrives; the arm is idempotent, so the earlier one wins.
//
// It also re-arms a resumed run, which is why the arm is dropped on a pause: each
// arm is a fresh ceiling of EXECUTING time, and a run deliberately parked for a
// week must not be cancelled for having been parked.
func (h *Hub) observeRunStart(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.armRunCeiling(workflowIDOfFrame(msg))
	h.translator.HandleRunStart(ctx, chatID, msg)
}

// observeRunComplete drops the bounds of a TERMINAL run, then translates.
//
// Non-terminal run_complete frames keep the arm: KAS reports an `onMaxIterations`
// policy pause through this same frame, and that run is still this process's to
// resume.
func (h *Hub) observeRunComplete(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	if f := decodeLifecycleFrame(msg); f.WorkflowID != "" && terminalRunStatus(f.Status) {
		h.forgetRunBounds(f.WorkflowID)
	}
	h.translator.HandleRunComplete(ctx, chatID, msg)
}

// observeRunPaused drops the ceiling of a run that stopped executing, then
// translates. The run-level `paused` kind only: a node-level pause is a step
// waiting inside a run that is still going.
func (h *Hub) observeRunPaused(next func(context.Context, api.ChatID, *api.RPCResponse)) func(context.Context, api.ChatID, *api.RPCResponse) {
	return func(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
		h.disarmRunCeiling(workflowIDOfFrame(msg))
		next(ctx, chatID, msg)
	}
}

// lifecycleFrame is the two fields the bounds read off a workflow lifecycle
// frame. Deliberately its own minimal decode rather than a share of translate's
// wire structs: those are the translator's contract with KAS, and a bound
// reaching into them would couple two unrelated readers of one frame.
type lifecycleFrame struct {
	WorkflowID string `json:"workflowId"`
	Status     string `json:"status"`
}

func decodeLifecycleFrame(msg *api.RPCResponse) lifecycleFrame {
	var f lifecycleFrame
	if msg == nil || len(msg.Params) == 0 {
		return f
	}
	if json.Unmarshal(msg.Params, &f) != nil {
		return lifecycleFrame{}
	}
	return f
}

func workflowIDOfFrame(msg *api.RPCResponse) string {
	return decodeLifecycleFrame(msg).WorkflowID
}
