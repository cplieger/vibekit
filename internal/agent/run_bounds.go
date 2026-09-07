package agent

// Run bounds: ONE deadline every run gets, the per-step turn cap, and the field
// that lets a run's row say which stopped it.
//
// The idle window, the absolute backstop and a scheduled run's next slot are all
// INPUTS to the single deadline the lease carries (runlease.NextDeadline). That
// deadline is MUTABLE: every start re-stamps it, every pause parks it, and every
// piece of observable progress rolls it forward, so it bounds EXECUTING time and
// time spent making no progress rather than wall time. Zero means unbounded.

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

// runIdleWindow is how long a run may execute without producing observable progress
// before it is cancelled — the PRIMARY bound, and a stall bound rather than a
// duration one: every node completing and every tool call starting rolls it forward.
// Strictly longer than KAS's own 300s stream idle timeout, which retries a stalled
// stream silently, so anything shorter would cancel runs KAS is recovering.
const runIdleWindow = 15 * time.Minute

// runBackstop is the absolute EXECUTING-time budget a run gets whatever its progress
// says, for the one population the idle window cannot bound: a runaway loop that
// looks productive, emitting a `node_complete` per pass and refilling the window
// forever. 36 hours is OBSERVED rather than derived — this app's own recipes have run
// for eight — and set far enough above that the next longer run does not move it.
// A CONSTANT: a backstop the user can raise stops being a backstop.
const runBackstop = 36 * time.Hour

// refillGranularity is the smallest movement a refill will spend a write on. A refill
// is one whole-file fsynced rewrite of runs.json and a busy step emits a tool call
// every few seconds, so without it the watchdog costs more disk traffic than the run
// it watches. The cost is stated: effective stall tolerance becomes [runIdleWindow -
// refillGranularity, runIdleWindow]. It is policy, so it lives here rather than in
// Store.SetDeadline, whose contract is exact re-stamping.
const refillGranularity = time.Minute

// minRunBudget is the smallest EXECUTING budget any run may be given, whatever the
// idle window or the SLOT says. DERIVED: internal/schedule's own minMinuteInterval,
// the tightest repeat the schedule form accepts — change them together. It answers
// for the SLOT only and must not lift the BACKSTOP: applied there it would hand out a
// fresh five minutes per progress frame and the absolute bound never comes due.
const minRunBudget = 5 * time.Minute

// The per-step cap is Crew's 200 and lives in translate.StepTurnCap, beside the
// counter that enforces it. Tool calls rather than model turns, because a turn is not
// observable per step.

// The abnormal terminations a run's row can report, and the vocabulary the client's
// verdict branches on. A user cancel records NOTHING, which is what makes these three
// distinguishable from it. Every value here needs a matching sentence in
// static-src/history.ts END_REASON_TEXT.
const (
	runEndOverran  = "overran"
	runEndStepCap  = "step_cap"
	runEndOrphaned = "orphaned"
)

// logMsgStepCap / logMsgRunOrphaned are CONSTANTS because a homelab Loki rule
// keys on the message.
const (
	logMsgStepCap     = "workflow step exceeded its turn cap; cancelling the run"
	logMsgRunOrphaned = "run was orphaned by a restart; cancelling so its recipe is idle again"
)

// logMsgRunStalled and logMsgRunBackstop are the two ways the deadline's own bound
// comes due, split because an operator acts on them differently: a stalled run
// stopped producing, while a run that spent its whole absolute budget was working the
// entire time and needs its workflow shortened. CONSTANTS for greppability rather
// than because a Loki rule keys on them.
const (
	logMsgRunStalled  = "run made no progress inside its idle window; cancelling"
	logMsgRunBackstop = "run spent its absolute executing-time backstop; cancelling"
)

// logMsgRunYieldedToSlot is what a MANUAL run cut short by its own recipe's next
// scheduled slot logs under. INFO and its own message rather than a share of
// logMsgRunOverran: it is the bound WORKING, and reusing that one would page an
// operator through a rule reading "a schedule stopped producing".
const logMsgRunYieldedToSlot = "manual run reached its recipe's next scheduled slot; " +
	"cancelling so the schedule can run"

// logMsgCancelUnretried is what a run vibekit could not stop AT ALL logs under: the
// bound came due and every attempt failed. It claims nothing about the RUN, because
// retryTermination fires for every non-nil cancel error, an unknown workflow id
// included. A CONSTANT for greppability, not because a rule keys on it.
const logMsgCancelUnretried = "a run's cancel failed on every attempt; " +
	"vibekit has stopped trying to stop it"

// maxRunEndReasons bounds the recorded-termination map. The record has to OUTLIVE the
// run — the History row reads it after the run finished — so it cannot be cleared on
// the terminal frame. Only an abnormal termination writes an entry; the oldest goes first.
const maxRunEndReasons = 256

// runBoundsState is the runtime-side half of the bounds: the live timers, the claim
// that arbitrates every ending path, and the recorded reasons.
//
// IN-MEMORY. The DEADLINE lives on the run's durable lease instead, which is what
// makes a bound survive a restart; what a restart loses is a claim and a recorded
// reason. FIELD ORDER IS govet's fieldalignment, not readability.
type runBoundsState struct {
	// timers holds the live deadline timer per run, keyed by workflow id. A handle
	// rather than a set because `AfterFunc` cannot be un-fired. It carries no
	// generation: a fired callback re-reads the deadline it was armed for.
	timers map[string]*time.Timer
	// terminating names the runs whose termination has been CLAIMED: user cancel,
	// schedule deadline, the run's own deadline and step cap. Exactly one wins, and only
	// the winner records a reason and issues the cancel. Dropped when the run reports
	// terminal (forgetBounds) and when a retry re-drives it (clearEnd).
	terminating map[string]struct{}
	// heals counts the automatic resumes this process has issued for a run
	// SINCE IT LAST MADE PROGRESS (run_host.go healPaused). Reset by a node
	// completing and by the run ending.
	heals map[string]int
	// cancelRetries counts the re-attempts of a REFUSED cancel, SINCE THE RUN LAST
	// MADE PROGRESS — healProgress refills it beside heals. Its own counter, because
	// the two count opposite operations and one budget would let either starve out.
	cancelRetries map[string]int
	// armedAt is when each bounded run's CURRENT executing stretch began, written by the
	// arm and dropped by the park, so presence here mirrors Bounded() on the lease. The
	// BACKSTOP's anchor, and not Lease.StartedAt: that is wall time, so `StartedAt +
	// runBackstop` would cancel a run parked on a person.
	armedAt map[string]time.Time
	// executed is executing time accumulated across this run's COMPLETED stretches,
	// which is what makes the backstop a bound on EXECUTING time: a run parked for a week
	// burns none of it. IN-MEMORY, so a restart hands a surviving run a fresh backstop —
	// consistent with the claim and the reason, which a restart also loses.
	executed map[string]time.Duration
	// reasons maps a workflow id to why it was stopped; order is the FIFO
	// eviction queue for it.
	reasons map[string]string
	order   []string
}

// stampDeadline is the ONE TRANSACTION armDeadline and refillDeadline share: read the
// lease, let the caller's policy decide, write the deadline, swap the timer, all under
// a single hold of the mutex. Read as three separately-locked steps, two concurrent
// stampers can leave the lease carrying B's deadline with only A's timer alive — a run
// that reads BOUNDED with no callback anywhere. Lock order is mu then the lease
// store's, and leaseStore() takes mu itself, so it resolves BEFORE the hold. `decide`
// runs under the hold, so it must not take mu. A run with no lease is refused for both
// callers: the only ones without are the TUI's, which vibekit does not host.
func (rs *Runs) stampDeadline(
	ctx context.Context, workflowID string,
	decide func(l runlease.Lease, now time.Time) (time.Time, bool),
) {
	if workflowID == "" {
		return
	}
	store := rs.leaseStore()
	rs.mu.Lock()
	defer rs.mu.Unlock()
	l, held := store.Get(workflowID)
	if !held {
		return
	}
	deadline, stamp := decide(l, time.Now())
	if !stamp {
		return
	}
	err := store.SetDeadline(ctx, workflowID, deadline)
	if errors.Is(err, runlease.ErrNotFound) {
		// The lease went away between the read and the write: nothing to bound, and a
		// timer would schedule a cancel against a run this process no longer holds. The
		// stretch bookkeeping goes with it.
		delete(rs.bounds.armedAt, workflowID)
		delete(rs.bounds.executed, workflowID)
		return
	}
	if err != nil {
		// DURABILITY ONLY. SetDeadline sets the in-memory deadline whenever the lease
		// exists, so returning here would leave the lease bounded with no timer —
		// permanently, since the arm then skips and a refill only replaces a timer.
		slog.Error("a run's deadline is not durable, so it will not survive a restart; "+
			"this process still bounds the run",
			"workflow_id", workflowID, "deadline", deadline, "error", err)
	}
	rs.setTimerLocked(workflowID, deadline)
}

// runBoundsLocked composes the three inputs NextDeadline takes for one run.
//
// stretchStart is what makes the backstop measure EXECUTING time: the instant total
// executing time reaches runBackstop is FIXED for the whole stretch, so a refill
// recomputes the same value. Anchoring on `now` would let a refilling run outlive the
// backstop forever. The caller holds mu; the lease travels by pointer because it is a
// wide value and only two of its fields are read.
func (rs *Runs) runBoundsLocked(l *runlease.Lease, stretchStart time.Time) runlease.Bounds {
	return runlease.Bounds{
		SlotAt: l.SlotAt,
		// The floor may not lift this input: NextDeadline clamps on it LAST, so a remainder
		// tighter than the floor wins and a NEGATIVE one fires at once, which is what a
		// spent budget means. Let the floor answer and every stamp grants a fresh minimum.
		BackstopAt: stretchStart.Add(runBackstop - rs.bounds.executed[l.WorkflowID]),
		Idle:       runIdleWindow,
		Floor:      minRunBudget,
	}
}

// armDeadline gives a run a FRESH budget and the timer that enforces it.
//
// IDEMPOTENT on an already-bounded run — `run_start` re-fires on every resume and the
// launch verbs arm too, so the EARLIEST arm wins. It also opens the run's executing
// STRETCH, which is what the backstop is anchored on.
func (rs *Runs) armDeadline(ctx context.Context, workflowID string) {
	rs.stampDeadline(ctx, workflowID, func(l runlease.Lease, now time.Time) (time.Time, bool) {
		if l.Bounded() {
			return time.Time{}, false
		}
		if rs.bounds.armedAt == nil {
			rs.bounds.armedAt = map[string]time.Time{}
		}
		rs.bounds.armedAt[workflowID] = now
		return runlease.NextDeadline(now, rs.runBoundsLocked(&l, now)), true
	})
}

// refillDeadline rolls a bounded run's deadline forward on observable progress, which
// is what makes the primary bound a STALL bound.
//
// Its guard is armDeadline's INVERSE: a PARKED run is not refillable, because rolling
// its deadline forward resurrects a bound observePaused removed, and a run held paused
// would then be cancelled for having been held. THROTTLED at refillGranularity, which
// also keeps a refill from moving the deadline EARLIER — the backstop clamp makes an
// earlier value computable, and tightening a granted budget is not this operation's job.
func (rs *Runs) refillDeadline(ctx context.Context, workflowID string) {
	rs.stampDeadline(ctx, workflowID, func(l runlease.Lease, now time.Time) (time.Time, bool) {
		if !l.Bounded() {
			return time.Time{}, false
		}
		// A bounded run with no recorded stretch is one this process did not arm (a test
		// staging a deadline through the store). Treat the stretch as beginning now: this
		// process cannot say how much of the backstop is spent, so it claims none of it.
		start, armed := rs.bounds.armedAt[workflowID]
		if !armed {
			start = now
		}
		next := runlease.NextDeadline(now, rs.runBoundsLocked(&l, start))
		if !next.After(l.Deadline.Add(refillGranularity)) {
			return time.Time{}, false
		}
		return next, true
	})
}

// RunMadeProgress rolls a run's idle window forward. Satisfies
// translate.RunBoundsAccess. FIRE-AND-FORGET and idempotent, because it is called
// once per tool-call frame: the `bounded` pre-check keeps the common case one map read
// rather than a derived context plus a store transaction.
func (rs *Runs) RunMadeProgress(workflowID string) {
	if !rs.bounded(workflowID) {
		return
	}
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()
	rs.refillDeadline(ctx, workflowID)
}

// setTimerLocked installs the one timer a run's current deadline gets, replacing any
// left from an earlier one. The caller holds the mutex, because this is the last step
// of the stamp's transaction. The replaced timer is STOPPED rather than forgotten: an
// un-stopped AfterFunc stays live until its own deadline, so a busy run would hold a
// pending callback per frame it ever emitted.
func (rs *Runs) setTimerLocked(workflowID string, deadline time.Time) {
	if old := rs.bounds.timers[workflowID]; old != nil {
		old.Stop()
	}
	if rs.bounds.timers == nil {
		rs.bounds.timers = map[string]*time.Timer{}
	}
	// The deadline travels INTO the callback so it can compare against what the lease
	// says at fire time — the whole liveness test, no generation token needed.
	rs.bounds.timers[workflowID] = time.AfterFunc(time.Until(deadline),
		func() { rs.cancelExpired(workflowID, deadline) })
}

// stopTimer stops and forgets a run's timer.
func (rs *Runs) stopTimer(workflowID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	t, ok := rs.bounds.timers[workflowID]
	if !ok {
		return
	}
	t.Stop()
	delete(rs.bounds.timers, workflowID)
}

// disarmDeadline parks a run vibekit is no longer bounding: the lease's deadline is
// cleared and its timer stopped. Reports whether the run held a deadline at all.
// Clearing the LEASE is the load-bearing half — a stale deadline would make the step
// cap believe the run is executing and hand the next re-arm a run to skip.
func (rs *Runs) disarmDeadline(ctx context.Context, workflowID string) bool {
	if workflowID == "" {
		return false
	}
	l, ok := rs.lease(workflowID)
	if !ok || !l.Bounded() {
		// Stop any timer anyway: a lease already released by a terminal frame can
		// still have a timer pending against it.
		rs.stopTimer(workflowID)
		return false
	}
	// The stretch ends here, so bank what it spent BEFORE the park: this is the only
	// moment it is knowable, and it stops a pause/resume cycle earning a fresh backstop.
	rs.bankExecuted(workflowID)
	if err := rs.leaseStore().SetDeadline(ctx, workflowID, time.Time{}); err != nil {
		slog.Warn("could not park a run's deadline", "workflow_id", workflowID, "error", err)
	}
	rs.stopTimer(workflowID)
	return true
}

// bankExecuted adds the run's current executing stretch to its accumulated total and
// closes the stretch. The DELETE is what makes it safe from two paths at once: a
// second caller finds no stretch, so two parks cannot double-charge the backstop.
func (rs *Runs) bankExecuted(workflowID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	start, armed := rs.bounds.armedAt[workflowID]
	if !armed {
		return
	}
	if rs.bounds.executed == nil {
		rs.bounds.executed = map[string]time.Duration{}
	}
	rs.bounds.executed[workflowID] += time.Since(start)
	delete(rs.bounds.armedAt, workflowID)
}

// clearExecuted drops a run's whole backstop accounting, for a run that has
// stopped executing for good. Without it a workflow id KAS reuses would inherit
// a spent backstop and be cancelled minutes after it started.
func (rs *Runs) clearExecuted(workflowID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.bounds.armedAt, workflowID)
	delete(rs.bounds.executed, workflowID)
}

// bounded reports whether vibekit currently believes the run to be EXECUTING under a
// deadline it set. TWO readers, neither of which may CLEAR it the way the timer's
// callback path does: the step cap's gate and retryTermination's own test.
func (rs *Runs) bounded(workflowID string) bool {
	l, ok := rs.lease(workflowID)
	return ok && l.Bounded()
}

// claimTermination takes a run's single termination claim, reporting true for the ONE
// caller that may end it. Four race for it and they are not variations of one thing: a
// user pressing Cancel, a schedule's repeat interval, the wall clock, and a step's turn
// cap. Without the claim two could pass at once and the second recordEnd overwrote the
// first, turning a deliberate stop into a timeout on the History row.
func (rs *Runs) claimTermination(workflowID string) bool {
	if workflowID == "" {
		return false
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.claimLocked(workflowID)
}

// claimExpiredDeadline is the deadline callback's claim, and the deadline check is
// INSIDE it deliberately: `Timer.Stop` does not halt an already-running func, so
// checking and claiming in two steps leaves a stale callback able to cancel a run the
// resume has just given a fresh budget. Two locks are held, outer first, and only in
// this order anywhere: the runtime's bounds mutex, then the lease store's.
func (rs *Runs) claimExpiredDeadline(workflowID string, armedFor time.Time) bool {
	store := rs.leaseStore()
	rs.mu.Lock()
	defer rs.mu.Unlock()
	l, ok := store.Get(workflowID)
	if !ok || !l.Deadline.Equal(armedFor) {
		return false
	}
	return rs.claimLocked(workflowID)
}

// claimLocked is the claim itself, for a caller already holding unattendedMu.
func (rs *Runs) claimLocked(workflowID string) bool {
	if _, taken := rs.bounds.terminating[workflowID]; taken {
		return false
	}
	if rs.bounds.terminating == nil {
		rs.bounds.terminating = map[string]struct{}{}
	}
	rs.bounds.terminating[workflowID] = struct{}{}
	return true
}

// releaseTermination hands a claim back, for the one case where the winner terminated
// nothing: the cancel RPC failed, so the next caller must be able to try. Holding a
// claim on a run still executing would make the Cancel button silently do nothing.
func (rs *Runs) releaseTermination(workflowID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.bounds.terminating, workflowID)
}

// finishTermination is what the claim WINNER does, and the only place a run's reason is
// recorded alongside its cancel. An empty reason means a user cancel: recordEnd ignores
// it, and that absence is what makes a bound's cancel distinguishable from a person's.
//
// NOTHING IS TOUCHED UNTIL THE CANCEL LANDS: a refused cancel means the run did NOT
// stop, so it is still one vibekit is bounding and its row has no outcome to report. A
// landed cancel ends in releaseIfOver, and this is the ONE site that needs it — every
// deliberate stop reaches here except the orphan sweep, which releases its own lease.
func (rs *Runs) finishTermination(
	ctx context.Context, workflowID, reason string, carrier *sharedBridge,
) error {
	if err := rs.cancelRPC(ctx, workflowID, carrier); err != nil {
		rs.releaseTermination(workflowID)
		rs.retryTermination(workflowID, reason)
		return err
	}
	rs.disarmDeadline(ctx, workflowID)
	rs.recordEnd(workflowID, reason)
	rs.clearCancelRetries(workflowID)
	rs.releaseIfOver(ctx, workflowID)
	return nil
}

// maxCancelRetries bounds the automatic re-attempts one run's REFUSED cancel may spend
// BETWEEN TWO PIECES OF PROGRESS — healProgress refills it, because a refusal is
// evidence about a MOMENT. TWO SPENDERS share one budget: cancelOn and cancelBounded
// both reach finishTermination's error path.
const maxCancelRetries = 3

// cancelRetryBaseDelay is the wait before the FIRST re-attempt, doubling per attempt
// (5s, 10s, 20s). Not zero: the refusal means another process owns the run. A `var` so
// a test can drive it in milliseconds; never reassigned in production.
var cancelRetryBaseDelay = 5 * time.Second

// claimCancelRetry takes one of a run's cancel re-attempts, reporting false once the
// budget is spent. Returns the attempt NUMBER so the backoff is computed from the claim
// it took rather than from a second read. THE BOUND IS ON THE RETRY, NOT ON THE
// DEADLINE: the deadline states something about the RUN, this counts OUR attempts to
// end it, and bounding the deadline instead would unbound the run to bound the loop.
func (rs *Runs) claimCancelRetry(workflowID string) (attempt int, ok bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.bounds.cancelRetries == nil {
		rs.bounds.cancelRetries = map[string]int{}
	}
	if rs.bounds.cancelRetries[workflowID] >= maxCancelRetries {
		return rs.bounds.cancelRetries[workflowID], false
	}
	rs.bounds.cancelRetries[workflowID]++
	return rs.bounds.cancelRetries[workflowID], true
}

// clearCancelRetries gives a run its full re-attempt budget back — on a landed cancel,
// on PROGRESS, and when it stops for good.
func (rs *Runs) clearCancelRetries(workflowID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.bounds.cancelRetries, workflowID)
}

// retryTermination schedules ONE bounded re-attempt of a cancel KAS refused. Needed
// because the error path KEEPS the deadline while the fired timer is spent, so without
// this the run is bounded by a record nothing enforces. It re-reads before acting,
// which is what makes the untracked AfterFunc safe, and a refusal lands back here, so
// the ladder ends at maxCancelRetries per STALL.
func (rs *Runs) retryTermination(workflowID, reason string) {
	if workflowID == "" {
		return
	}
	attempt, ok := rs.claimCancelRetry(workflowID)
	if !ok {
		slog.Error(logMsgCancelUnretried, "workflow_id", workflowID,
			"attempts", attempt, "reason", reason)
		return
	}
	delay := cancelRetryBaseDelay * time.Duration(1<<(attempt-1))
	slog.Warn("a run's cancel was refused; re-attempting it, and the run stays bounded",
		"workflow_id", workflowID, "attempt", attempt, "delay", delay)
	// NOT tracked, unlike `bounds.timers`: this re-issues a stop the run already
	// earned rather than deciding one, and the guards below re-read.
	time.AfterFunc(delay, func() {
		// A pause parked the deadline or a terminal frame released the lease, so
		// vibekit has stopped bounding this run and it is not one to cancel.
		if !rs.bounded(workflowID) {
			return
		}
		if !rs.claimTermination(workflowID) {
			return
		}
		ctx, cancel := rs.lifecycle.derivedContext()
		defer cancel()
		// nil carrier: any hint the caller held is a ladder delay old, so re-resolve.
		if err := rs.finishTermination(ctx, workflowID, reason, nil); err != nil {
			slog.Error("a re-attempted cancel was refused too",
				"workflow_id", workflowID, "error", err)
		}
	})
}

// forgetBounds drops what a run that stopped executing for good no longer needs: its
// deadline, its per-stall budgets, its executing-time accounting, its termination claim
// and its lease. The lease goes here because this is the one site every origin reaches —
// an agent-parented run has no bridge of its own to close.
//
// THE LEASE LEADS, and that is a guard: it is the refill's own door, so releasing it
// first is what makes the timer clear below final. Cleared first instead, a progress
// frame landing mid-teardown files a tracked timer nothing removes.
func (rs *Runs) forgetBounds(ctx context.Context, workflowID string) {
	rs.releaseLease(ctx, workflowID)
	rs.stopTimer(workflowID)
	rs.releaseTermination(workflowID)
	rs.clearHeals(workflowID)
	rs.clearCancelRetries(workflowID)
	rs.clearExecuted(workflowID)
	// A run that has ended cannot be waiting on a person, so any ask still recorded for
	// it is a card no answer would reach. It ANNOUNCES rather than dropping quietly: a
	// clickable card is worse than none, and it hides every later ask for that chat.
	rs.settleAsksForRun(ctx, workflowID)
	// The SAME question for the run's request-shaped asks, which live in the pending-
	// decision tracker: without this the next SSE connect replays a step's permission for
	// a run that has ended. Silent, because SettledByMoot is for a run ask only.
	rs.clearRunPerms(workflowID)
}

// clearRunPerms drops a run's unanswered request-shaped decisions, tolerating the bare
// &Runs{} a bounds test builds — offerRunTab carries the same guard.
func (rs *Runs) clearRunPerms(workflowID string) {
	if rs.perms == nil {
		return
	}
	rs.perms.ClearPendingPermsForRun(workflowID)
}

// claimHeal takes one of a run's automatic-resume attempts, reporting false once the
// budget is spent. The budget exists because a heal and a pause drive each other: a
// network that is genuinely down fails the step the moment the run resumes, and the
// frame that says so is the one that triggered the heal. Returns the attempt NUMBER so
// the caller's backoff comes from the count this claim took, not a racing second read.
func (rs *Runs) claimHeal(workflowID string) (attempt int, ok bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.bounds.heals == nil {
		rs.bounds.heals = map[string]int{}
	}
	if rs.bounds.heals[workflowID] >= maxAutoHeals {
		return rs.bounds.heals[workflowID], false
	}
	rs.bounds.heals[workflowID]++
	return rs.bounds.heals[workflowID], true
}

// clearHeals gives a run its full heal budget back. Called when a node
// completes and when the run ends.
func (rs *Runs) clearHeals(workflowID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.bounds.heals, workflowID)
}

// clearEnd forgets a run's recorded termination, so a RE-DRIVEN run is bounded again
// and its row stops reading as the failure it used to be. Both halves are required: the
// client lets a recognised end_reason outrank live status, and a run holding a
// termination claim can never be bounded or cancelled again. The claim is dropped
// BEFORE the reason lookup returns, because a user-cancelled run holds a claim and
// records no reason.
func (rs *Runs) clearEnd(workflowID string) {
	if workflowID == "" {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.bounds.terminating, workflowID)
	// With the claim, and above the early return for its reason: a run whose cancels
	// were all refused records none, so keying on one would strand the next stop.
	delete(rs.bounds.cancelRetries, workflowID)
	if _, ok := rs.bounds.reasons[workflowID]; !ok {
		return
	}
	delete(rs.bounds.reasons, workflowID)
	// The order slice is the eviction QUEUE for the map, so leaving a
	// dangling entry would let the map grow past its cap.
	rs.bounds.order = slices.DeleteFunc(rs.bounds.order,
		func(id string) bool { return id == workflowID })
}

// rearmRetried gives a re-driven run a clean row and a FRESH budget.
//
// The disarm is not redundant with the terminal state that made retry legal: an
// already-hosted retry can still carry the deadline it was launched with. A retry after
// a TERMINAL frame has no lease left, so it needs a fresh one, with the recipe NAME off
// KAS's run list — a nameless lease is invisible to the single-run rule. The SLOT stays
// zero, so a retried run of a scheduled recipe is bounded by the window and backstop.
func (rs *Runs) rearmRetried(ctx context.Context, workflowID, recipe string) {
	rs.clearEnd(workflowID)
	rs.disarmDeadline(ctx, workflowID)
	if _, held := rs.lease(workflowID); !held {
		rs.grantLease(ctx, workflowID, recipe, manualLaunch())
	}
	rs.armDeadline(ctx, workflowID)
}

// recordEnd notes why a run was stopped, so its row can say so.
func (rs *Runs) recordEnd(workflowID, reason string) {
	if workflowID == "" || reason == "" {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.bounds.reasons == nil {
		rs.bounds.reasons = map[string]string{}
	}
	if _, dup := rs.bounds.reasons[workflowID]; !dup {
		rs.bounds.order = append(rs.bounds.order, workflowID)
	}
	rs.bounds.reasons[workflowID] = reason
	for len(rs.bounds.order) > maxRunEndReasons {
		delete(rs.bounds.reasons, rs.bounds.order[0])
		rs.bounds.order = rs.bounds.order[1:]
	}
}

// endReason reports why a run was stopped, or "" for a run that ended on its
// own terms — including a user cancel, which records nothing.
func (rs *Runs) endReason(workflowID string) string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.bounds.reasons[workflowID]
}

// cancelExpired is the deadline's callback: stop the run, say why, and tell the
// schedule row when the slot is what ran out. armedFor is the deadline it was armed
// for, checked against the lease before the claim.
//
// The slot test is "set and not AFTER the deadline", not equality: the floor outranks
// the slot, so a slot already gone produces a deadline LATER than SlotAt. FOUR outcomes
// over three messages, and the recorded reason is runEndOverran for all four — so a
// stalled run's History row reads "it ran past its time limit".
func (rs *Runs) cancelExpired(workflowID string, armedFor time.Time) {
	l, held := rs.lease(workflowID)
	if !rs.claimExpiredDeadline(workflowID, armedFor) {
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
	case rs.backstopSpent(workflowID):
		slog.Error(logMsgRunBackstop, "workflow_id", workflowID,
			"backstop", runBackstop.String(), "recipe", l.Recipe)
	default:
		slog.Error(logMsgRunStalled, "workflow_id", workflowID,
			"idle_window", runIdleWindow.String(), "recipe", l.Recipe)
	}
	rs.cancelBounded(workflowID, runEndOverran)
	if !slotRanOut || rs.schedules == nil || l.ScheduleID == "" {
		return
	}
	// Surface it on the row. Without this the schedule still reads "started"
	// while it silently stops producing.
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()
	if err := rs.schedules.RecordOutcome(ctx, l.ScheduleID, outcomeOverran); err != nil {
		slog.Warn("could not record the schedule's outcome",
			"schedule_id", l.ScheduleID, "error", err)
	}
}

// backstopSpent reports whether the run's absolute executing-time budget is gone at
// this instant, which is what tells the two expiry messages apart. It measures the OPEN
// stretch too: the backstop's deadline fires mid-stretch by construction, so reading
// only `executed` would report every backstop expiry as a stall.
func (rs *Runs) backstopSpent(workflowID string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	spent := rs.bounds.executed[workflowID]
	if start, armed := rs.bounds.armedAt[workflowID]; armed {
		spent += time.Since(start)
	}
	return spent >= runBackstop
}

// StepTurnCapExceeded stops the run a runaway step belongs to. Satisfies
// translate.RunBoundsAccess. Cancelling the whole RUN is the only enforcement
// available: every C→A workflow verb vibekit can issue is run-scoped, so there is no
// way to stop one step and let its run continue.
func (rs *Runs) StepTurnCapExceeded(workflowID, nodeID string, turns int) {
	if !rs.bounded(workflowID) {
		// A run vibekit is not bounding is not one it may cancel.
		return
	}
	if !rs.claimTermination(workflowID) {
		// Already terminating — a sibling step, the run's deadline, or the user
		// cancelled first.
		return
	}
	slog.Error(logMsgStepCap,
		"workflow_id", workflowID, "node_id", nodeID, "turns", turns, "cap", translate.StepTurnCap)
	rs.cancelBounded(workflowID, runEndStepCap)
}

// cancelBounded issues the cancel cancelExpired and StepTurnCapExceeded both end in,
// for a caller that has already WON the termination claim. Not the public Cancel: that
// one claims, so a bound calling it would refuse its own cancel. A failure is logged
// AND handed to finishTermination's ladder.
func (rs *Runs) cancelBounded(workflowID, reason string) {
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()
	if err := rs.finishTermination(ctx, workflowID, reason, nil); err != nil {
		slog.Error("could not cancel a run that breached its bound",
			"workflow_id", workflowID, "error", err)
	}
}

// runStartLaunch classifies a lease minted from a `run_start` frame by the CARRIER the
// frame arrived on, which is the only thing that says who launched the run. A real chat
// id means a CHAT's bridge — agent-launched, chat-parented, and excluded from the
// orphan sweep's cancel arm because the resume sweep owns it. Anything else is
// PARENTLESS and MUST be sweepable. Inferring the origin from lease ABSENCE is wrong
// for a retry, whose first lifecycle frame can beat its own lease grant.
func runStartLaunch(chatID vibekit.ChatID) launchOrigin {
	if chatID == "" || isRunChat(chatID) {
		return launchOrigin{origin: runlease.OriginManual}
	}
	return launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)}
}

// observeStart arms the run's deadline, then hands the frame to the translator.
//
// `run_start` is the arming point covering the launch path vibekit does not own: KAS
// creates and invokes an agent-launched run internally, so this frame is the FIRST
// thing vibekit sees of it and the lease for that population is minted HERE. It also
// re-arms a RESUMED run, since a pause parks the deadline. The tab offer runs before
// the translator, so the tab exists by the time `run_started` reaches the client.
func (rs *Runs) observeStart(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	if f := decodeLifecycleFrame(msg); f.WorkflowID != "" {
		if _, held := rs.lease(f.WorkflowID); !held {
			rs.grantLease(ctx, f.WorkflowID, f.WorkflowName, runStartLaunch(chatID))
		}
		rs.armDeadline(ctx, f.WorkflowID)
		rs.offerRunTab(ctx, chatID, f.WorkflowID)
	}
	rs.translate.HandleRunStart(ctx, chatID, msg)
}

// observeComplete drops the bounds AND the step-session registry of a TERMINAL run,
// then translates.
//
// Non-terminal run_complete frames keep the arm: KAS reports an `onMaxIterations`
// policy pause through this same frame, and that run is still this process's to resume.
// The registry and the step-driven TURN ride the same gate — a chat-parented run's step
// frames open a turn on the launching chat that the bracket path cannot close, because
// the attribution gate drops a step's own turn_end.
func (rs *Runs) observeComplete(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	if f := decodeLifecycleFrame(msg); f.WorkflowID != "" && terminalRunStatus(f.Status) {
		// FIRST, ahead of HandleRunComplete: the close persists the step's assistant
		// message, and the client repaints the run on the `run_finished` invalidation the
		// translator emits — so the content has to be on disk before that repaint.
		rs.closeStepTurn(ctx, chatID)
		rs.forgetBounds(ctx, f.WorkflowID)
		rs.translate.ForgetRunSteps(f.WorkflowID)
	}
	rs.translate.HandleRunComplete(ctx, chatID, msg)
}

// closeStepTurn closes the launching chat's step-driven turn, if it has one. An empty
// or `run:` chat id is a PARENTLESS run, which folds onto no chat and so has no turn to
// close — the same distinction runStartLaunch makes. The `rs.coord == nil` guard is for
// the bare &Runs{} a bounds test builds.
//
// It runs on the bridge's Forward goroutine SYNCHRONOUSLY: that is where WireTurnEnd
// already finalizes turns from, and a goroutine would break the caller's ordering.
func (rs *Runs) closeStepTurn(ctx context.Context, chatID vibekit.ChatID) {
	if rs.coord == nil || chatID == "" || isRunChat(chatID) {
		return
	}
	rs.coord.CloseStepTurn(ctx, chatID)
}

// observePaused parks the deadline of a run that stopped executing, then
// translates. The run-level `paused` kind only: a node-level pause is a step
// waiting inside a run that is still going.
func (rs *Runs) observePaused(next func(context.Context, vibekit.ChatID, *vibekit.RPCResponse)) func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {
	return func(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
		rs.disarmDeadline(ctx, workflowIDOfFrame(msg))
		next(ctx, chatID, msg)
	}
}

// lifecycleFrame is the three fields the bounds read off a workflow lifecycle frame.
// Its own minimal decode rather than a share of translate's wire structs, which are the
// translator's contract with KAS. WorkflowName is on `run_start` and is the recipe name
// KAS's own run list reports.
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
