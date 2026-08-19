package hub

// Run bounds: ONE wall clock every run gets, the per-step turn cap, and the one
// field that lets a run's row say which of those stopped it.
//
// ONE CLOCK, TWO INPUTS. The universal ceiling and a scheduled run's own next
// slot used to be two independent mechanisms — the slot armed only by the
// scheduler's launch path, the ceiling armed by everything — and neither knew the
// other existed. So a manual run of a scheduled recipe held that recipe for the
// whole ceiling and refused every slot underneath it, and a slot that fired late
// bounded its run by whatever remained of an interval that had nearly elapsed.
// Both are now INPUTS to the single deadline the run's lease carries
// (runlease.NextDeadline): the tighter of the two, floored so no run is handed a
// budget it cannot finish inside.
//
// The deadline is MUTABLE and lives on the lease, which is what keeps the bound
// on EXECUTING time: every start re-stamps it, every pause parks it, so a run
// deliberately held for a week is not cancelled for having been held. A zero
// deadline means vibekit is not bounding the run, and it is the successor of the
// arm map's membership.
//
// TWO BOUNDS, NOT THREE. The run clock and the per-step turn cap; there is
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
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// runCeiling is how long any single run may execute before it is cancelled.
//
// KiroCrew's number (its `_RUN_TIMEOUT_SECS` default is 3600s) taken as the
// starting point, because it is the one comparable figure measured against real
// unattended workflow runs. A CONSTANT, deliberately: Crew clamps a configured
// value to [60s, 6h] and there is nothing to clamp here — no caller supplies one
// and no caller may.
const runCeiling = time.Hour

// minRunBudget is the smallest EXECUTING budget any run may be given, whatever
// the other input says.
//
// DERIVED, not chosen: it is internal/schedule's own minimum interval
// (minMinuteInterval, 5 minutes), which is the shortest repeat the schedule form
// will accept. So the floor says exactly "a run gets at least as long as the
// tightest schedule this app allows", and it closes two ways the slot input can
// produce a nonsense number: a slot that fires at the very end of its miss-grace
// window, and an interval edited below the schedule floor by something other than
// the validated store path. Below it, a bound is not a backstop — it is a
// guaranteed failure written into the row on every slot while nothing is wrong.
//
// Change it with internal/schedule/spec.go's minMinuteInterval, whose own comment
// names this bound as one of the four mechanisms its floor exists for.
const minRunBudget = 5 * time.Minute

// The per-step cap is Crew's 200 and it lives in translate.StepTurnCap, beside
// the counter that enforces it. Tool calls rather than model turns, because a turn
// is not observable per step: KAS's nine workflow notifications carry no per-step
// turn-end frame, so a step's boundary is node_start/node_complete and everything
// between is ordinary `session/update` traffic. The runaway this catches IS a tool
// loop, so counting the loop's tool calls measures the thing rather than a proxy.

// The abnormal terminations a run's row can report, and the vocabulary the
// client's verdict branches on. A user cancel records NOTHING — its absence is
// what makes these three distinguishable from it, which is the whole point of the
// field (see vibekit.WorkflowRun.EndReason).
//
// FOUR facts, four values, because a user cancel, a blown deadline, a step-cap
// trip and a restart orphan are four different things that happened and the row
// should name which. Every value here needs a matching sentence in
// static-src/history.ts END_REASON_TEXT: an unrecognised reason renders safely
// (the row falls back to the live status) but says NOTHING about what vibekit did,
// which is the invisibility the field exists to remove. Ship both halves together.
const (
	runEndOverran  = "overran"
	runEndStepCap  = "step_cap"
	runEndOrphaned = "orphaned"
)

// logMsgRunCeiling / logMsgStepCap / logMsgRunOrphaned are the messages the bounds
// and the orphan sweep log under. ERROR for the same reason logMsgRunOverran is: a
// run stopped by a backstop is an unattended failure, and a homelab Loki rule can
// key on the message.
const (
	logMsgRunCeiling  = "run exceeded its wall-clock ceiling; cancelling"
	logMsgStepCap     = "workflow step exceeded its turn cap; cancelling the run"
	logMsgRunOrphaned = "run was orphaned by a restart; cancelling so its recipe is idle again"
)

// logMsgRunYieldedToSlot is what a MANUAL run cut short by its own recipe's next
// scheduled slot logs under.
//
// INFO, and its own message rather than a share of logMsgRunOverran, because it is
// the bound WORKING rather than a failure: the manual run stood aside so the
// schedule could keep producing, which is exactly what feeding the slot into a
// manual run's deadline is for. Reusing logMsgRunOverran would have paged the
// operator through the Loki rule that reads that string as "a schedule stopped
// producing", for the one case where it did not.
const logMsgRunYieldedToSlot = "manual run reached its recipe's next scheduled slot; " +
	"cancelling so the schedule can run"

// maxRunEndReasons bounds the recorded-termination map.
//
// The record has to OUTLIVE the run — the History row reads it after the run
// finished, which is the only moment it is useful — so it cannot be cleared on
// the terminal frame the way the ceiling arm is. Only an abnormal termination
// writes an entry, so this is a large multiple of what a healthy container
// produces; the oldest is dropped first, which loses the reason for a run nobody
// is still looking at.
const maxRunEndReasons = 256

// runBoundsState is the hub-side half of the bounds: the live timers, the claim
// that arbitrates every ending path, and the recorded reasons.
//
// IN-MEMORY, and the consequence is stated rather than hidden. The DEADLINE
// itself is not here — it lives on the run's durable lease, which is what makes a
// run's bound survive a restart. What a restart still loses is a claim (the first
// path to ask afterwards wins) and a recorded reason (an already-finished run's
// row falls back to plain "aborted"). Both are facts about an ending in progress
// in THIS process, which is why neither is worth persisting.
type runBoundsState struct {
	// timers holds the live deadline timer per run, keyed by workflow id.
	//
	// A handle rather than a set, because `AfterFunc` cannot be un-fired: a
	// timer has to be STOPPABLE or a run paused and resumed a hundred times over
	// a week carries a hundred pending callbacks, each waking up to ask about a
	// run its own deadline no longer describes.
	//
	// It carries NO generation, and does not need one. Identity used to be
	// implicit in a closure's captured variable, so the callback could not ask
	// "am I still current" without a token to compare. It asks the LEASE now: a
	// fired callback re-reads the stored deadline and does nothing unless it is
	// still the one the callback was armed for, which a pause or a resume has
	// already changed.
	timers map[string]*time.Timer
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
}

// armRunDeadline gives a run its deadline and the timer that enforces it.
//
// IDEMPOTENT on an already-bounded run, which is the property `run_start` forces:
// that frame re-fires on every resume (probe 6 saw three for one run) and the
// launch verbs arm too, so a run is armed more than once by design and the
// EARLIEST arm wins. A resumed run is a different case and gets a fresh budget,
// because the pause parked its lease — so "already bounded" is false by the time
// the resume arrives.
//
// A run with no lease is not armed at all. That is not a gap: a lease exists for
// every run vibekit put on the wire, and observeRunStart mints one for the
// agent's launch path, so the only runs reaching here without one are the TUI's —
// which vibekit does not host, cannot cancel through a bridge of its own, and has
// no business bounding.
//
// AfterFunc rather than a goroutine: it parks nothing while waiting, and a run
// that ends first makes the callback a no-op rather than something that has to be
// cancelled.
//
// ONE TRANSACTION, and the concurrency it exists for is by design rather than
// exotic: the launch verb arms after `invoke` while that run's own `run_start`
// frame is already arriving on its bridge, so two arms race on every launch. Read
// as three separately-locked steps, both callers could see an unbounded lease,
// compute two deadlines, store them in one order and install their timers in the
// other — leaving the lease carrying deadline B while only timer A survived, and
// timer A's own liveness check then refuses to act because A is not B. The run
// would read as bounded with no live callback anywhere. So the idempotence check,
// the store and the timer swap happen under one hold of unattendedMu.
//
// Lock order is the one documented on claimExpiredDeadline and taken nowhere else
// in reverse: unattendedMu, then the lease store's. leaseStore() takes
// unattendedMu itself, so it is resolved BEFORE the hold.
func (h *Hub) armRunDeadline(ctx context.Context, workflowID string) {
	if workflowID == "" {
		return
	}
	store := h.leaseStore()
	h.unattendedMu.Lock()
	defer h.unattendedMu.Unlock()
	l, ok := store.Get(workflowID)
	if !ok || l.Bounded() {
		return
	}
	deadline := runlease.NextDeadline(time.Now(), runCeiling, minRunBudget, l.SlotAt)
	err := store.SetDeadline(ctx, workflowID, deadline)
	if errors.Is(err, runlease.ErrNotFound) {
		// The lease went away between the read and the write. There is nothing to
		// bound, and arming a timer would schedule a cancel against a run whose
		// envelope this process no longer holds.
		return
	}
	if err != nil {
		// DURABILITY ONLY. SetDeadline sets the in-memory deadline whenever the
		// lease exists and reports just the persist, so the store DOES carry this
		// deadline — and returning here would leave the lease bounded with no
		// timer, which the idempotence check above makes permanent: every later
		// `run_start` sees Bounded() and returns, so the missing timer is never
		// installed and the run executes with no wall clock at all. Degrading
		// durability while keeping the in-process envelope is the same choice
		// grantLease makes for the same reason.
		slog.Error("a run's deadline is not durable, so it will not survive a restart; "+
			"this process still bounds the run",
			"workflow_id", workflowID, "deadline", deadline, "error", err)
	}
	h.setRunTimerLocked(workflowID, deadline)
}

// setRunTimerLocked installs the one timer a run's current deadline gets,
// replacing any timer left from an earlier one. The caller holds unattendedMu,
// because installing the timer is the last step of the arm's single transaction.
//
// The replaced timer is STOPPED rather than forgotten: an un-stopped AfterFunc
// stays live until its own deadline, and a run cycling through pause and resume
// would accumulate one per cycle.
func (h *Hub) setRunTimerLocked(workflowID string, deadline time.Time) {
	if old := h.runBounds.timers[workflowID]; old != nil {
		old.Stop()
	}
	if h.runBounds.timers == nil {
		h.runBounds.timers = map[string]*time.Timer{}
	}
	// The deadline travels INTO the callback so the callback can compare it with
	// what the lease says at fire time. That comparison is the whole liveness
	// test, and it is why no generation token is needed.
	h.runBounds.timers[workflowID] = time.AfterFunc(time.Until(deadline),
		func() { h.cancelExpiredRun(workflowID, deadline) })
}

// stopRunTimer stops and forgets a run's timer.
func (h *Hub) stopRunTimer(workflowID string) {
	h.unattendedMu.Lock()
	defer h.unattendedMu.Unlock()
	t, ok := h.runBounds.timers[workflowID]
	if !ok {
		return
	}
	t.Stop()
	delete(h.runBounds.timers, workflowID)
}

// disarmRunDeadline parks a run vibekit is no longer bounding: the lease's
// deadline is cleared and its timer stopped. Reports whether the run held a
// deadline at all.
//
// Clearing the LEASE is the load-bearing half, not stopping the timer. A stopped
// timer that leaves a stale deadline behind would make the step cap believe the
// run is still executing, and would hand the next re-arm an "already bounded" run
// to skip — so the run would never be bounded again.
func (h *Hub) disarmRunDeadline(ctx context.Context, workflowID string) bool {
	if workflowID == "" {
		return false
	}
	l, ok := h.lease(workflowID)
	if !ok || !l.Bounded() {
		// Stop any timer anyway: a lease already released by a terminal frame can
		// still have a timer pending against it.
		h.stopRunTimer(workflowID)
		return false
	}
	if err := h.leaseStore().SetDeadline(ctx, workflowID, time.Time{}); err != nil {
		slog.Warn("could not park a run's deadline", "workflow_id", workflowID, "error", err)
	}
	h.stopRunTimer(workflowID)
	return true
}

// runBounded reports whether vibekit currently believes the run to be EXECUTING
// under a deadline it set — the step cap's gate, which must not CLEAR that
// deadline the way the timer's own callback path does: a breach that loses the
// termination claim leaves the wall clock to whoever won it.
func (h *Hub) runBounded(workflowID string) bool {
	l, ok := h.lease(workflowID)
	return ok && l.Bounded()
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
	return h.claimLocked(workflowID)
}

// claimExpiredDeadline is the deadline callback's claim, and the deadline check
// is INSIDE it deliberately.
//
// `Timer.Stop` does not halt an already-running func, so a callback that fired
// microseconds before a pause is in flight while the pause parks the lease and the
// resume re-stamps a fresh deadline. Checking the deadline and taking the claim in
// two steps would leave exactly the window the retired generation token existed to
// close: the stale callback reads a deadline that still matches, loses the race to
// the resume, and then cancels a run that has just been given a fresh budget.
//
// Two locks are held, outer first, and only in this order anywhere: the hub's
// bounds mutex, then the lease store's. The store never calls back into the hub,
// so there is no path that could take them the other way.
func (h *Hub) claimExpiredDeadline(workflowID string, armedFor time.Time) bool {
	store := h.leaseStore()
	h.unattendedMu.Lock()
	defer h.unattendedMu.Unlock()
	l, ok := store.Get(workflowID)
	if !ok || !l.Deadline.Equal(armedFor) {
		return false
	}
	return h.claimLocked(workflowID)
}

// claimLocked is the claim itself, for a caller already holding unattendedMu.
func (h *Hub) claimLocked(workflowID string) bool {
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
// vibekit.WorkflowRun.EndReason).
func (h *Hub) finishRunTermination(ctx context.Context, workflowID, reason string) error {
	h.disarmRunDeadline(ctx, workflowID)
	h.recordRunEnd(workflowID, reason)
	err := h.cancelRunRPC(ctx, workflowID)
	if err != nil {
		h.releaseRunTermination(workflowID)
	}
	return err
}

// forgetRunBounds drops what a run that stopped executing for good no longer
// needs: its wall clock, its termination claim and its lease.
//
// The lease goes HERE rather than beside the bridge teardown because this is the
// one site every origin reaches. A parentless run's terminal frame also closes
// its bridge, but an agent-parented run has no bridge of its own to close, and
// its lease must be released all the same.
//
// The recorded REASON deliberately survives — the History row reads it after the
// run finished, which is the only moment it is useful. Nothing can act on the run
// after this: every bound's gate (a current arm for the ceiling, any arm for the
// step cap, the unattended mark for the schedule deadline) is already false.
func (h *Hub) forgetRunBounds(ctx context.Context, workflowID string) {
	h.stopRunTimer(workflowID)
	h.releaseRunTermination(workflowID)
	h.releaseLease(ctx, workflowID)
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
// frame, which can still carry the deadline it was launched with — and the arm is
// idempotent on an already-bounded run, so without the disarm that run would be
// retried under the remainder of its previous clock.
//
// A retry after a TERMINAL frame has no lease left (forgetRunBounds released it),
// so it needs a fresh one — and it gets the recipe NAME the caller read off KAS's
// own run list, because that list carries it (`kasWorkflowRun.Name`) for a run
// this process never launched. An empty name used to be minted here on the
// reasoning that a re-hosted run's recipe is unknowable; it is not, and the cost
// of the guess was real: a nameless lease is invisible to the single-run rule's
// comparison, so the admission backstop could not recognise this run as the thing
// holding its own recipe.
//
// The SLOT is still zero, and that narrowing is genuine: a schedule is matched by
// launch SOURCE (manualSlot) and the run list reports only the name, so a retried
// run of a scheduled recipe is bounded by the ceiling alone rather than by the
// schedule's next slot.
func (h *Hub) rearmRetriedRun(ctx context.Context, workflowID, recipe string) {
	h.clearRunEnd(workflowID)
	h.disarmRunDeadline(ctx, workflowID)
	if _, held := h.lease(workflowID); !held {
		h.grantLease(ctx, workflowID, recipe, manualLaunch())
	}
	h.armRunDeadline(ctx, workflowID)
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

// cancelExpiredRun is the deadline's callback: stop the run, say why, and tell the
// schedule row when the slot is what ran out.
//
// armedFor is the deadline this callback was armed for, and claimExpiredDeadline
// checks it against the lease before taking the termination claim — a resumed or
// re-stamped run carries a different one, and cancelling it here would end it
// after the old deadline's remainder rather than a fresh budget.
//
// WHICH input ran out decides the log line and whether the schedule row is
// written, and it is read off the lease rather than remembered.
//
// The test is "the slot is set and is not AFTER the deadline", not equality with
// it, because NextDeadline has THREE possible answers and not two: the ceiling,
// the slot, or the FLOOR — and the floor outranks the slot deliberately, so a slot
// already gone or closer than minRunBudget produces a deadline LATER than SlotAt.
// Equality classified that as a ceiling breach, which logged the wrong message
// (`ceiling=1h` on a callback that fired after five minutes) and skipped the
// schedule row entirely, leaving the row reading `started` for a schedule vibekit
// had just cut a run off for. An earlier slot means the slot is still what
// limited the run; only a slot beyond the deadline means the ceiling won.
//
// Three outcomes, because the ceiling message and the schedule message mean
// genuinely different things and a MANUAL run yielding to a slot means a third:
// the two ERROR-level constants are matched by homelab Loki rules as "a schedule
// stopped producing" and "a run blew the universal ceiling", and a manual run
// standing aside for its recipe's next slot is neither — it is the bound working,
// so it says so at INFO instead of paging somebody.
func (h *Hub) cancelExpiredRun(workflowID string, armedFor time.Time) {
	l, held := h.lease(workflowID)
	if !h.claimExpiredDeadline(workflowID, armedFor) {
		return
	}
	slotRanOut := held && !l.SlotAt.IsZero() && !l.SlotAt.After(armedFor)
	switch {
	case slotRanOut && l.ScheduleID != "":
		slog.Error(logMsgRunOverran, "workflow_id", workflowID, "schedule_id", l.ScheduleID,
			"slot_at", l.SlotAt, "armed_for", armedFor)
	case slotRanOut:
		slog.Info(logMsgRunYieldedToSlot, "workflow_id", workflowID, "recipe", l.Recipe,
			"slot_at", l.SlotAt)
	default:
		slog.Error(logMsgRunCeiling, "workflow_id", workflowID, "ceiling", runCeiling.String())
	}
	h.cancelBoundedRun(workflowID, runEndOverran)
	if !slotRanOut || h.schedules == nil || l.ScheduleID == "" {
		return
	}
	// Surface it on the row. Without this the schedule still reads "started"
	// while it silently stops producing, which is the one failure the launch
	// alert cannot distinguish from a merely long run.
	ctx, cancel := h.hubContext()
	defer cancel()
	if err := h.schedules.RecordOutcome(ctx, l.ScheduleID, outcomeOverran); err != nil {
		slog.Warn("could not record the schedule's outcome",
			"schedule_id", l.ScheduleID, "error", err)
	}
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
	if !h.runBounded(workflowID) {
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

// runStartOrigin classifies a lease minted from a `run_start` frame by the
// CARRIER the frame arrived on, which is the only thing that actually says who
// launched the run.
//
// A real chat id means the frame came up a CHAT's bridge, and KAS puts a run on a
// chat's session only when that chat's agent asked for it — so that is an
// agent-launched run, chat-parented by construction and excluded from the orphan
// sweep because the chat rehydrate's resume sweep owns it.
//
// Anything else is PARENTLESS: run-bridge frames are dispatched with an empty chat
// id (runDispatch), and the bridge itself is registered under the synthetic
// `run:<id>`, so both spellings mean "this run has no chat". Such a run is
// vibekit's own manual work and MUST be sweepable.
//
// Inferring the origin from lease ABSENCE, as this used to, was wrong in exactly
// the case that matters. A retry re-hosts a parentless run and grants its lease
// after the retry call returns, while the code itself notes the first lifecycle
// frame can arrive immediately — so `run_start` landing first saw no lease and
// stamped OriginAgent on a run no chat owns. rearmRetriedRun then kept that lease,
// and the agent exclusion made the run permanently unsweepable: if its bridge died
// or vibekit restarted, its restart-paused row was never cleared and blocked every
// later launch of the recipe. The carrier is knowable at the frame, so nothing is
// left to infer.
func runStartOrigin(chatID vibekit.ChatID) runlease.Origin {
	if chatID == "" || isRunChat(chatID) {
		return runlease.OriginManual
	}
	return runlease.OriginAgent
}

// observeRunStart arms the run's deadline, then hands the frame to the
// translator.
//
// `run_start` is the arming point that covers the launch path vibekit does not
// own: KAS's RunWorkflowTool creates and invokes an agent-launched run internally
// and parents it on the calling chat's session, so this frame is the FIRST thing
// vibekit sees of it — which is why the lease for that population is minted HERE
// rather than at a launch verb it never passes through. That lease carries the
// ceiling and nothing else: an agent's run has no slot, is attended by the chat
// that asked for it, and is deliberately excluded from the restart-orphan sweep
// because KAS parents it on that chat's session.
//
// The frame's own WorkflowName is the recipe, and it is the same string KAS's run
// list reports — so a lease minted here is keyed consistently with every other
// one and the single-run rule can compare it.
//
// The launch verbs arm too, so a run this process started is bounded from the
// instant it started rather than from whenever its frame arrives; the arm is
// idempotent on an already-bounded run, so the earlier one wins.
//
// It also re-arms a RESUMED run, which is why the deadline is parked on a pause:
// each arm is a fresh budget of EXECUTING time, and a run deliberately parked for
// a week must not be cancelled for having been parked.
func (h *Hub) observeRunStart(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	if f := decodeLifecycleFrame(msg); f.WorkflowID != "" {
		if _, held := h.lease(f.WorkflowID); !held {
			h.grantLease(ctx, f.WorkflowID, f.WorkflowName,
				launchOrigin{origin: runStartOrigin(chatID)})
		}
		h.armRunDeadline(ctx, f.WorkflowID)
	}
	h.translator.HandleRunStart(ctx, chatID, msg)
}

// observeRunComplete drops the bounds of a TERMINAL run, then translates.
//
// Non-terminal run_complete frames keep the arm: KAS reports an `onMaxIterations`
// policy pause through this same frame, and that run is still this process's to
// resume.
func (h *Hub) observeRunComplete(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	if f := decodeLifecycleFrame(msg); f.WorkflowID != "" && terminalRunStatus(f.Status) {
		h.forgetRunBounds(ctx, f.WorkflowID)
	}
	h.translator.HandleRunComplete(ctx, chatID, msg)
}

// observeRunPaused parks the deadline of a run that stopped executing, then
// translates. The run-level `paused` kind only: a node-level pause is a step
// waiting inside a run that is still going.
func (h *Hub) observeRunPaused(next func(context.Context, vibekit.ChatID, *vibekit.RPCResponse)) func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {
	return func(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
		h.disarmRunDeadline(ctx, workflowIDOfFrame(msg))
		next(ctx, chatID, msg)
	}
}

// lifecycleFrame is the three fields the bounds read off a workflow lifecycle
// frame. Deliberately its own minimal decode rather than a share of translate's
// wire structs: those are the translator's contract with KAS, and a bound
// reaching into them would couple two unrelated readers of one frame.
//
// WorkflowName is on `run_start` and is the recipe name KAS's own run list
// reports, which is the string the single-run rule compares against — so an
// agent-launched run's lease is keyed consistently with every other one.
type lifecycleFrame struct {
	WorkflowID   string `json:"workflowId"`
	WorkflowName string `json:"workflowName"`
	Status       string `json:"status"`
}

func decodeLifecycleFrame(msg *vibekit.RPCResponse) lifecycleFrame {
	var f lifecycleFrame
	if msg == nil || len(msg.Params) == 0 {
		return f
	}
	if json.Unmarshal(msg.Params, &f) != nil {
		return lifecycleFrame{}
	}
	return f
}

func workflowIDOfFrame(msg *vibekit.RPCResponse) string {
	return decodeLifecycleFrame(msg).WorkflowID
}
