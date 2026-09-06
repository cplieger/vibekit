package agent

// Run bounds: ONE deadline every run gets, the per-step turn cap, and the one
// field that lets a run's row say which of those stopped it.
//
// ONE CLOCK, THREE INPUTS. The idle window, the absolute backstop and a
// scheduled run's own next slot are INPUTS to the single deadline the run's
// lease carries (runlease.NextDeadline): the tighter of the three, floored so
// no run is handed a budget it cannot finish inside — except a backstop already
// SPENT, which no floor may lift, because that one says the run is over rather
// than that its remaining bound is too small to work in. Before the lease the slot
// and the universal ceiling were two independent mechanisms, so a manual run of
// a scheduled recipe held that recipe for the whole ceiling and refused every
// slot underneath it.
//
// The deadline is MUTABLE and lives on the lease, which is what keeps the bound
// on EXECUTING time and on time spent making no progress: every start re-stamps
// it, every pause parks it, and every piece of observable progress rolls it
// FORWARD. So a run deliberately held for a week is not cancelled for having
// been held, and a run still producing steps after nine hours is not cancelled
// for being long. A zero deadline means vibekit is not bounding the run.
//
// THREE BOUNDS, NOT FOUR: the idle window, the backstop, and the per-step turn
// cap. There is still deliberately no PER-STEP wall clock, and the idle window
// is not one wearing a different name — it is keyed on the RUN and refilled by
// the run's own progress, so a step that legitimately takes an hour keeps the
// run alive by working, where a per-step wall clock would cancel it for being
// slow. What the window cancels is a run producing nothing at all, which is the
// one state a step's turn cap cannot see (a wedged step makes no tool calls, so
// its counter never moves). There is no token budget either — vibekit's
// per-step meter reads zero, since a step is not a chat.
//
// NONE IS A SETTING: a backstop the user can raise stops being a backstop.
// There is no per-run override either, since launch is shared between the
// Workflows tab's Run button and the scheduler.

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

// runIdleWindow is how long a run may execute without producing observable
// progress before it is cancelled. It is the PRIMARY bound, and it is a stall
// bound rather than a duration bound: every node completing and every tool call
// starting rolls it forward, so a healthy run of any length never approaches it.
//
// Strictly longer than KAS's own stream idle timeout (300s), and that is the
// derivation rather than a coincidence: a stalled STREAM is handled one layer
// down by KAS's `stream_error_retry`, which re-issues the stream and emits
// nothing on the wire, so anything shorter would cancel runs KAS was in the
// middle of recovering. This window is about a stalled RUN — the state where
// KAS has stopped producing frames at all — so it has to be long enough that
// KAS's own recovery has provably failed first.
//
// A CONSTANT, deliberately: no caller supplies one and no caller may.
const runIdleWindow = 15 * time.Minute

// runBackstop is the absolute EXECUTING-time budget a run gets whatever its
// progress says, and it exists for the one population the idle window cannot
// bound: a runaway loop that looks productive. A repeat node re-driving the same
// step forever emits a `node_complete` on every pass, so it refills the window
// indefinitely and would hold its recipe for the life of the container.
//
// Sized against the run the window is protecting rather than against the
// runaway: a bound healthy work can reach is a bound that fails healthy work,
// and the runaway is caught whenever the bound fires, so the only cost of a
// generous one is how long a wedged-but-productive loop holds its recipe.
//
// 36 hours, OBSERVED rather than derived: runs of this app's own recipes have
// been seen executing for eight, so the earlier eight-hour figure was inside the
// healthy range it was chosen to sit above. It is deliberately not the observed
// maximum plus a margin — the observation is a floor on what is legitimate, not a
// ceiling, so the bound is set far enough above it that the next longer run does
// not move it again. A CONSTANT for runIdleWindow's reason.
const runBackstop = 36 * time.Hour

// refillGranularity is the smallest amount of movement a refill will spend a
// write on.
//
// A refill is one whole-file fsynced rewrite of runs.json (Store.persistLocked
// re-marshals the entire document), and a busy step emits a tool call every few
// seconds — so without this the watchdog would cost more disk traffic than the
// run it watches. One minute against a fifteen-minute window means at most one
// write per run per minute, and the cost is stated rather than hidden: the
// effective stall tolerance becomes [runIdleWindow - refillGranularity,
// runIdleWindow], so a stall is detected between 14 and 15 minutes after the
// last progress.
//
// It lives HERE rather than inside Store.SetDeadline because it is policy: the
// store's contract is exact re-stamping, and armDeadline and disarmDeadline both
// depend on that. A store that silently rounded would be a second, invisible
// policy nothing could see.
const refillGranularity = time.Minute

// minRunBudget is the smallest EXECUTING budget any run may be given, whatever
// the idle window or the SLOT says.
//
// DERIVED, not chosen: it is internal/schedule's own minimum interval
// (minMinuteInterval, 5 minutes), the shortest repeat the schedule form will
// accept — so a run gets at least as long as the tightest schedule this app
// allows. That derivation is about the SLOT, which is exactly why it does not
// reach the BACKSTOP — any backstop tighter than the composed value, of which one
// already spent is the sharpest case. Applied there it would hand out a fresh five
// minutes on every progress frame and the absolute bound would never come due.
// Change it with internal/schedule/spec.go's minMinuteInterval.
const minRunBudget = 5 * time.Minute

// The per-step cap is Crew's 200 and it lives in translate.StepTurnCap, beside
// the counter that enforces it. Tool calls rather than model turns, because a
// turn is not observable per step: a step's boundary is node_start/node_complete
// and everything between is ordinary `session/update` traffic.

// The abnormal terminations a run's row can report, and the vocabulary the
// client's verdict branches on. A user cancel records NOTHING — its absence is
// what makes these three distinguishable from it.
//
// FOUR facts, four values: a user cancel, a blown deadline, a step-cap trip and
// a restart orphan are four different things that happened. Every value here
// needs a matching sentence in static-src/history.ts END_REASON_TEXT.
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

// logMsgRunStalled and logMsgRunBackstop are the two ways the deadline's own
// bound comes due, split because they describe opposite failures and an operator
// acts on them differently: a stalled run stopped producing and probably needs
// looking at, while a run that spent its whole absolute budget was working the
// entire time and needs its workflow shortened. Folding them into one message
// would make the interesting one unfindable.
//
// CONSTANTS for greppability rather than because a rule keys on them: no homelab
// Loki rule names either, verified against that repo's own alert rules, and
// logMsgRunStalled is the line worth one — a wedged run is exactly what nobody is
// watching for.
const (
	logMsgRunStalled  = "run made no progress inside its idle window; cancelling"
	logMsgRunBackstop = "run spent its absolute executing-time backstop; cancelling"
)

// logMsgRunYieldedToSlot is what a MANUAL run cut short by its own recipe's
// next scheduled slot logs under.
//
// INFO, its own message rather than a share of logMsgRunOverran: it is the
// bound WORKING rather than a failure, and reusing logMsgRunOverran would page
// the operator through a rule reading "a schedule stopped producing", for the
// one case where it did not.
const logMsgRunYieldedToSlot = "manual run reached its recipe's next scheduled slot; " +
	"cancelling so the schedule can run"

// logMsgCancelUnretried is what a run vibekit could not stop AT ALL logs under: the
// bound came due and every attempt failed. It claims nothing about the RUN, because
// retryTermination fires for every non-nil cancel error, an unknown workflow id
// included, and it cannot fire for a run whose progress refills the budget inside every
// ladder delay (vibekit-runtime.md "A REFUSED CANCEL"). A CONSTANT for greppability,
// not because a rule keys on it: none names it yet, and this is the line worth one.
const logMsgCancelUnretried = "a run's cancel failed on every attempt; " +
	"vibekit has stopped trying to stop it"

// maxRunEndReasons bounds the recorded-termination map.
//
// The record has to OUTLIVE the run — the History row reads it after the run
// finished — so it cannot be cleared on the terminal frame. Only an abnormal
// termination writes an entry, so this is a large multiple of what a healthy
// container produces; the oldest is dropped first.
const maxRunEndReasons = 256

// runBoundsState is the runtime-side half of the bounds: the live timers, the
// claim that arbitrates every ending path, and the recorded reasons.
//
// IN-MEMORY. The DEADLINE itself is not here — it lives on the run's durable
// lease, which is what makes a run's bound survive a restart. What a restart
// still loses is a claim (the first path to ask afterwards wins) and a
// recorded reason (an already-finished run's row falls back to plain
// "aborted").
//
// FIELD ORDER IS govet's fieldalignment, not readability: `order` is the only
// field carrying non-pointer words, so it goes LAST.
type runBoundsState struct {
	// timers holds the live deadline timer per run, keyed by workflow id.
	//
	// A handle rather than a set, because `AfterFunc` cannot be un-fired: a
	// timer has to be STOPPABLE or a run paused and resumed a hundred times
	// carries a hundred pending callbacks.
	//
	// It carries NO generation: a fired callback re-reads the stored deadline
	// and does nothing unless it is still the one it was armed for, which a
	// pause or resume has already changed.
	timers map[string]*time.Timer
	// terminating names the runs whose termination has been CLAIMED: user
	// cancel, schedule deadline, the run's own deadline and step cap. Exactly one
	// of them wins a run, and only the winner records a reason and issues the
	// cancel.
	//
	// Dropped when the run reports terminal (forgetBounds) and when a retry
	// re-drives it (clearEnd).
	terminating map[string]struct{}
	// heals counts the automatic resumes this process has issued for a run
	// SINCE IT LAST MADE PROGRESS (run_host.go healPaused). Reset by a node
	// completing and by the run ending.
	heals map[string]int
	// cancelRetries counts the re-attempts of a REFUSED cancel, SINCE THE RUN LAST
	// MADE PROGRESS — healProgress refills it beside heals. Its own counter, because
	// the two count opposite operations and one budget would let either starve out.
	cancelRetries map[string]int
	// armedAt is when each bounded run's CURRENT executing stretch began. Written
	// by the arm that installs a deadline, dropped by the park that accumulates
	// it, so a run's presence here mirrors Bounded() on its lease.
	//
	// The BACKSTOP's anchor, and the reason it is not Lease.StartedAt: that field
	// is wall time and survives a restart, so `StartedAt + runBackstop` would
	// cancel a run parked longer than the backstop on a person five minutes after
	// they answered.
	armedAt map[string]time.Time
	// executed is executing time accumulated across this run's COMPLETED
	// stretches, which is what makes the backstop a bound on EXECUTING time
	// rather than on wall time: a run parked for a week burns none of it.
	//
	// IN-MEMORY like everything else here, so a restart hands a surviving run a
	// fresh backstop. Bounded in practice — a parentless run executing across a
	// restart is cancelled by the orphan sweep and a chat-parented one is the
	// agent's own — and consistent with the claim and the reason, which a restart
	// also loses.
	executed map[string]time.Duration
	// reasons maps a workflow id to why it was stopped; order is the FIFO
	// eviction queue for it.
	reasons map[string]string
	order   []string
}

// stampDeadline is the ONE TRANSACTION armDeadline and refillDeadline share:
// read the lease, let the caller's policy decide, write the deadline, swap the
// timer — all under a single hold of the mutex.
//
// ONE TRANSACTION, and the refill makes it MORE load-bearing rather than less.
// Two arms already contended on every launch (the launch verb arms after
// `invoke` while that run's own `run_start` frame is arriving on its bridge);
// now every tool call the run makes is a further concurrent stamper. Read as
// three separately-locked steps, two stampers compute deadlines A and B, the
// stores land in one order and the timer swaps in the other, so the lease ends
// up carrying B while only A's timer survived — a run that reads BOUNDED with
// no live callback anywhere, and therefore no bound at all.
//
// Lock order is the one taken nowhere else in reverse: mu, then the lease
// store's — leaseStore() takes mu itself, so it is resolved BEFORE the hold.
//
// A run with no lease is refused here for both callers: the only runs reaching
// this point without one are the TUI's, which vibekit does not host and has no
// business bounding.
//
// `decide` is the POLICY, and it runs under the hold — so it must not take mu or
// reach anything that does. It reads and writes rs.bounds directly, which is
// exactly what the hold makes safe.
//
// AfterFunc rather than a goroutine: it parks nothing while waiting.
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
		// The lease went away between the read and the write. There is nothing
		// to bound, and arming a timer would schedule a cancel against a run
		// whose envelope this process no longer holds. The stretch bookkeeping
		// goes with it: a run this process is not bounding has no executing
		// stretch to account for.
		delete(rs.bounds.armedAt, workflowID)
		delete(rs.bounds.executed, workflowID)
		return
	}
	if err != nil {
		// DURABILITY ONLY. SetDeadline sets the in-memory deadline whenever the
		// lease exists, so the store DOES carry this deadline — returning here
		// would leave the lease bounded with no timer, permanent because the
		// arm's idempotence check then always skips installing one and a refill
		// only ever replaces a timer that already exists.
		slog.Error("a run's deadline is not durable, so it will not survive a restart; "+
			"this process still bounds the run",
			"workflow_id", workflowID, "deadline", deadline, "error", err)
	}
	rs.setTimerLocked(workflowID, deadline)
}

// runBoundsLocked composes the three inputs NextDeadline takes for one run.
//
// stretchStart is when the run's CURRENT executing stretch began, and it is what
// makes the backstop measure EXECUTING time: the instant at which total executing
// time reaches runBackstop is a FIXED point for the whole stretch, so a refill
// recomputes the same value rather than pushing it further out. Anchoring on
// `now` instead would let a refilling run outlive the backstop forever, which is
// the one thing the backstop exists to stop.
//
// The caller holds mu.
func (rs *Runs) runBoundsLocked(l runlease.Lease, stretchStart time.Time) runlease.Bounds {
	return runlease.Bounds{
		SlotAt: l.SlotAt,
		// The floor may not lift this input, whatever remains of it: NextDeadline
		// clamps on it LAST, so a remainder tighter than the floor wins and a NEGATIVE
		// remainder is returned as an instant in the past that the timer fires on at
		// once — which is what an absolute budget already spent means. Let the floor
		// answer instead and every stamp grants a fresh minimum.
		BackstopAt: stretchStart.Add(runBackstop - rs.bounds.executed[l.WorkflowID]),
		Idle:       runIdleWindow,
		Floor:      minRunBudget,
	}
}

// armDeadline gives a run a FRESH budget and the timer that enforces it.
//
// IDEMPOTENT on an already-bounded run: `run_start` re-fires on every resume
// and the launch verbs arm too, so a run is armed more than once by design
// and the EARLIEST arm wins. A resumed run gets a fresh budget because the
// pause parked its lease.
//
// It also opens the run's executing STRETCH, which is what the backstop is
// anchored on. A run cycling through pause and resume therefore accumulates its
// executing time across stretches (disarmDeadline banks each one) while the wall
// time it spent parked burns none of the backstop.
func (rs *Runs) armDeadline(ctx context.Context, workflowID string) {
	rs.stampDeadline(ctx, workflowID, func(l runlease.Lease, now time.Time) (time.Time, bool) {
		if l.Bounded() {
			return time.Time{}, false
		}
		if rs.bounds.armedAt == nil {
			rs.bounds.armedAt = map[string]time.Time{}
		}
		rs.bounds.armedAt[workflowID] = now
		return runlease.NextDeadline(now, rs.runBoundsLocked(l, now)), true
	})
}

// refillDeadline rolls a bounded run's deadline forward on observable progress,
// which is what makes the primary bound a STALL bound: a run stays alive by
// working, not by being young.
//
// Its guard is armDeadline's INVERSE, and that is why these are two doors rather
// than one operation with a flag. A PARKED run is not refillable — rolling its
// deadline forward would resurrect a bound observePaused deliberately removed,
// and a run held paused would then be cancelled for having been held, which is
// the one thing the mutable deadline exists to prevent.
//
// THROTTLED at refillGranularity, and the throttle carries two properties beyond
// the disk cost it exists for. A refill never moves the deadline EARLIER: the
// backstop clamp makes an earlier value computable, and tightening a budget the
// run was already granted is not this operation's job. And the backstop becomes
// TERMINAL ONCE IT BINDS, which is a property of NextDeadline's own precedence
// rather than of this throttle: the clamp is the LAST step there, so from the first
// stamp the backstop wins — ahead of the idle window or already spent — the answer is
// BackstopAt, and BackstopAt is fixed for the whole stretch, so every later refill
// computes that same instant, fails the test below, and leaves the timer armed for
// it. While it is further out than the window it binds on nothing and the window
// answers. Let a floor answer for a backstop tighter than the floor and each refill
// computes a LATER instant instead, clears the test, and rolls the absolute bound
// forward one floor at a time for as long as the run keeps making progress.
func (rs *Runs) refillDeadline(ctx context.Context, workflowID string) {
	rs.stampDeadline(ctx, workflowID, func(l runlease.Lease, now time.Time) (time.Time, bool) {
		if !l.Bounded() {
			return time.Time{}, false
		}
		// A bounded run with no recorded stretch is one this process did not arm
		// (a test staging a deadline through the store, a lease re-stamped by a
		// path that predates the accumulator). Treat the stretch as beginning now:
		// this process cannot say how much of the backstop is spent, so it claims
		// none of it.
		start, armed := rs.bounds.armedAt[workflowID]
		if !armed {
			start = now
		}
		next := runlease.NextDeadline(now, rs.runBoundsLocked(l, start))
		if !next.After(l.Deadline.Add(refillGranularity)) {
			return time.Time{}, false
		}
		return next, true
	})
}

// RunMadeProgress rolls a run's idle window forward. Satisfies
// translate.RunBoundsAccess.
//
// FIRE-AND-FORGET and idempotent, because it is called once per tool-call frame:
// the `bounded` pre-check is what keeps the common case a single map read under
// one mutex rather than a derived context plus a store transaction. The
// authoritative read is the transaction's own, so a run that stops being bounded
// between the two is refused there.
func (rs *Runs) RunMadeProgress(workflowID string) {
	if !rs.bounded(workflowID) {
		return
	}
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()
	rs.refillDeadline(ctx, workflowID)
}

// setTimerLocked installs the one timer a run's current deadline gets,
// replacing any timer left from an earlier one. The caller holds the mutex,
// because installing the timer is the last step of the stamp's transaction.
//
// The replaced timer is STOPPED rather than forgotten: an un-stopped AfterFunc
// stays live until its own deadline. That would leak one per pause/resume cycle
// on its own, and one per TOOL CALL now that progress re-stamps the deadline —
// so a busy run would hold a pending callback per frame it ever emitted, every
// one of them scheduled to fire at an instant the lease will have moved past.
func (rs *Runs) setTimerLocked(workflowID string, deadline time.Time) {
	if old := rs.bounds.timers[workflowID]; old != nil {
		old.Stop()
	}
	if rs.bounds.timers == nil {
		rs.bounds.timers = map[string]*time.Timer{}
	}
	// The deadline travels INTO the callback so it can compare against what
	// the lease says at fire time — the whole liveness test, no generation
	// token needed.
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

// disarmDeadline parks a run vibekit is no longer bounding: the lease's
// deadline is cleared and its timer stopped. Reports whether the run held a
// deadline at all.
//
// Clearing the LEASE is the load-bearing half. A stopped timer that leaves a
// stale deadline behind would make the step cap believe the run is still
// executing, and would hand the next re-arm an "already bounded" run to skip.
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
	// The stretch ends here, so bank what it spent BEFORE the park: this is the
	// only moment the run's executing time is knowable, and it is what stops a
	// run cycling through pause and resume from getting a fresh backstop per
	// cycle. The park itself is what makes the wall time that follows free.
	rs.bankExecuted(workflowID)
	if err := rs.leaseStore().SetDeadline(ctx, workflowID, time.Time{}); err != nil {
		slog.Warn("could not park a run's deadline", "workflow_id", workflowID, "error", err)
	}
	rs.stopTimer(workflowID)
	return true
}

// bankExecuted adds the run's current executing stretch to its accumulated total
// and closes the stretch.
//
// The DELETE is what makes it safe to call from two paths at once: a second
// caller finds no stretch and adds nothing, so two concurrent parks cannot
// double-charge the backstop. A run with no open stretch is a no-op.
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

// bounded reports whether vibekit currently believes the run to be EXECUTING
// under a deadline it set. TWO readers and neither may CLEAR it the way the timer's
// callback path does — the step cap's gate, where a breach that loses the claim
// leaves the wall clock to whoever won it, and retryTermination's own test.
func (rs *Runs) bounded(workflowID string) bool {
	l, ok := rs.lease(workflowID)
	return ok && l.Bounded()
}

// claimTermination takes a run's single termination claim, reporting true for
// the ONE caller that may end it.
//
// Four callers race for this and they are not variations of one thing: a
// user pressing Cancel, a schedule's own repeat interval, the universal wall
// clock, and a step's turn cap. Before the claim, two could pass
// simultaneously and the second `recordEnd` overwrote the first. The user's
// cancel was the one that lost worst: it records nothing, so a bound that
// claimed alongside it turned a deliberate stop into a timeout on the
// History row.
func (rs *Runs) claimTermination(workflowID string) bool {
	if workflowID == "" {
		return false
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.claimLocked(workflowID)
}

// claimExpiredDeadline is the deadline callback's claim, and the deadline
// check is INSIDE it deliberately.
//
// `Timer.Stop` does not halt an already-running func, so a callback that
// fired microseconds before a pause is in flight while the pause parks the
// lease and the resume re-stamps a fresh deadline. Checking the deadline and
// taking the claim in two separate steps would leave a stale callback able to
// cancel a run that has just been given a fresh budget.
//
// Two locks are held, outer first, and only in this order anywhere: the
// runtime's bounds mutex, then the lease store's.
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

// releaseTermination hands a claim back, for the one case where the winner
// did not actually terminate anything: the cancel RPC failed, so the next
// caller must be able to try. Holding a claim on a run still executing would
// make the Cancel button silently do nothing.
func (rs *Runs) releaseTermination(workflowID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.bounds.terminating, workflowID)
}

// finishTermination is what the claim WINNER does, and the only place a
// run's reason is recorded alongside its cancel.
//
// reason empty means a user cancel: recordEnd ignores it, and that absence is
// what makes a bound's cancel distinguishable from a person's.
//
// A LANDED cancel ends in releaseIfOver, and this is the ONE site that needs
// it: every deliberate stop this process issues arrives here. A user cancel from
// the composer, the REST verb, a chat tab close and `delete_chat` all reach it
// through cancelOn (Cancel delegates to it, CancelForSessions calls it per run), and
// cancelExpired and StepTurnCapExceeded reach it through cancelBounded. So both
// routes the defect was measured on — a cancel of a PAUSED run, and the tab-close
// cancel that tears the carrier down in the same operation — are covered by one
// call, not one per door.
// The orphan sweep is the exception that needs none: clearOrphaned issues its own
// cancelRPC and releases the lease itself.
//
// NOTHING IS TOUCHED UNTIL THE CANCEL LANDS: a refused cancel means the run did NOT
// stop, so it is still one vibekit is bounding and its row has no outcome to report.
// What the earlier order cost: vibekit-runtime.md "A REFUSED CANCEL NO LONGER".
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
// evidence about a MOMENT. Three, for maxAutoHeals' reason: a fourth attempt against a
// process that has refused three tells nobody anything the third did not. TWO SPENDERS
// share one budget: cancelOn (the user's Cancel) and cancelBounded (cancelExpired and
// StepTurnCapExceeded) both reach finishTermination's error path.
const maxCancelRetries = 3

// cancelRetryBaseDelay is the wait before the FIRST re-attempt, doubling per attempt
// (5s, 10s, 20s). Not zero: the refusal means another process owns the run, which
// clears when that owner's stamp goes stale or not at all. A `var` so a test can
// drive it in milliseconds, like healBaseDelay; never reassigned in production.
var cancelRetryBaseDelay = 5 * time.Second

// claimCancelRetry takes one of a run's cancel re-attempts, reporting false once
// the budget is spent. Returns the attempt NUMBER so the backoff is computed from
// the claim it took rather than from a second read.
//
// THE BOUND IS ON THE RETRY, NOT ON THE DEADLINE: the deadline is a statement about
// the RUN, this counts OUR attempts to end it, and bounding the deadline instead
// would unbound the run to bound the loop. claimHeal's shape, for its reason too — an
// attempt and its refusal drive each other, so a budget terminates that within a STALL.
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
// on PROGRESS (healProgress), and when it stops for good, so nothing is refused a stop
// over refusals that described an earlier moment.
func (rs *Runs) clearCancelRetries(workflowID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.bounds.cancelRetries, workflowID)
}

// retryTermination schedules ONE bounded re-attempt of a cancel KAS refused.
//
// Needed because the error path KEEPS the deadline: the fired timer is spent and
// claimExpiredDeadline compares against a lease that path did not change, so the
// next attempt is installed deliberately or the run is bounded by a record nothing
// enforces. It re-reads before acting, which is what makes the untracked AfterFunc
// safe (healPaused's precedent), and a refusal lands back here and spends the next
// slot — so the ladder ends at maxCancelRetries per STALL (healProgress refills it).
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

// forgetBounds drops what a run that stopped executing for good no longer
// needs: its deadline, its three per-stall budgets, its executing-time
// accounting, its termination claim and its lease.
//
// The lease goes HERE because this is the one site every origin reaches: an
// agent-parented run has no bridge of its own to close, and its lease must
// be released all the same.
//
// THE LEASE LEADS, and that order is a guard rather than a preference: the lease is
// the refill's own door, so releasing it FIRST is what makes the timer clear below
// final. Cleared first instead, the lease reads Bounded() for the rest of this
// teardown and a progress frame landing in that span files a tracked timer nothing
// removes — a permanent entry in bounds.timers, which unlike `reasons` has no
// eviction. It also makes the run's own timer INERT rather than early when it fires
// mid-teardown: claimExpiredDeadline finds no lease and refuses, where a claim over
// a run that just completed normally would stamp `overran` on its row.
//
// The recorded REASON deliberately survives — the History row reads it after
// the run finished, which is the only moment it is useful.
func (rs *Runs) forgetBounds(ctx context.Context, workflowID string) {
	rs.releaseLease(ctx, workflowID)
	rs.stopTimer(workflowID)
	rs.releaseTermination(workflowID)
	rs.clearHeals(workflowID)
	rs.clearCancelRetries(workflowID)
	rs.clearExecuted(workflowID)
	// A run that has ended cannot be waiting on a person, so any ask still
	// recorded for it is a card no answer would reach. HERE for the reason the
	// lease is: this is the one site every origin reaches. It ANNOUNCES rather
	// than dropping quietly — a card the reader can still click is worse than no
	// card, and while it sits at the head of a per-chat dock queue it also hides
	// every later ask for that chat.
	rs.settleAsksForRun(ctx, workflowID)
	// And the SAME question for the run's request-shaped asks, which live in the
	// pending-decision tracker rather than the run-ask registry. Without it the
	// client's own run-scoped sweep is undone by the next SSE connect: the replay
	// re-offers a step's permission / elicitation / user_input for a run that has
	// ended, and the launching chat's dot lights again for a card no answer can
	// reach. Silent, unlike settleAsksForRun above — SettledByMoot is restricted to
	// a run ask by its own contract, because a request-shaped ask is claimed by
	// whoever answers its JSON-RPC request.
	rs.clearRunPerms(workflowID)
}

// clearRunPerms drops a run's unanswered request-shaped decisions, tolerating the
// bare &Runs{} a bounds test builds — the precedent is offerRunTab's own nil
// check, for the same reason.
func (rs *Runs) clearRunPerms(workflowID string) {
	if rs.perms == nil {
		return
	}
	rs.perms.ClearPendingPermsForRun(workflowID)
}

// claimHeal takes one of a run's automatic-resume attempts, reporting false
// once the budget is spent.
//
// The budget exists because a heal and a pause can drive each other: a
// network that is genuinely down fails the step again the moment the run
// resumes, and the frame that says so is the same frame that triggered the
// heal. Three attempts, then the run stays paused and the ordinary
// chat-rehydration path owns it.
//
// Returns the attempt NUMBER as well as the verdict, so the caller's backoff
// is computed from the count this claim took rather than a second read two
// interleaved frames could race.
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

// clearEnd forgets a run's recorded termination, so a RE-DRIVEN run is
// bounded again and its row stops reading as the failure it used to be.
//
// Both halves are required. The reason has to go because the client lets a
// recognised end_reason outrank live status, so a retry of an `overran` run
// would render as aborted. The claim has to go because a terminated run
// holds one, and a run holding a claim cannot be bounded or cancelled again.
//
// The claim is dropped BEFORE the reason lookup returns, because a
// user-cancelled run holds a claim and records no reason: keying the whole
// clear on a recorded reason would leave that run unbounded on retry.
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
// The disarm is not redundant with the terminal state that made retry
// legal: an already-hosted retry can still carry the deadline it was
// launched with, and the arm is idempotent, so without the disarm it would
// be retried under the remainder of its previous clock.
//
// A retry after a TERMINAL frame has no lease left (forgetBounds released
// it), so it needs a fresh one — with the recipe NAME read off KAS's own run
// list, since a nameless lease would be invisible to the single-run rule's
// comparison.
//
// The SLOT is still zero: a schedule is matched by launch SOURCE (manualSlot)
// and the run list reports only the name, so a retried run of a scheduled
// recipe is bounded by the idle window and the backstop alone.
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

// cancelExpired is the deadline's callback: stop the run, say why, and tell
// the schedule row when the slot is what ran out.
//
// armedFor is the deadline this callback was armed for; claimExpiredDeadline
// checks it against the lease before taking the termination claim, so a
// resumed or re-stamped run carrying a different one is not cancelled after
// the old deadline's remainder.
//
// WHICH input ran out decides the log line and whether the schedule row is
// written. The slot test is "the slot is set and is not AFTER the deadline",
// not equality with it: NextDeadline has FOUR possible answers (the idle
// window, the slot, the backstop, or the floor), and the floor outranks the
// slot, so a slot already gone or closer than minRunBudget produces a deadline
// LATER than SlotAt. Equality misclassified that as vibekit's own bound, logging
// the wrong message and skipping the schedule row.
//
// FOUR outcomes over three messages. The slot arm splits by whether a schedule
// row asked for the run: logMsgRunOverran is an ERROR a homelab Loki rule reads
// as "a schedule stopped producing", while a MANUAL run standing aside for its
// recipe's next slot is the bound working, so it logs at INFO instead of paging
// somebody. What is left is vibekit's own bound, and it splits again on which
// of the two came due, because a stalled run and a run that spent its whole budget
// of real work are opposite failures with opposite remedies.
//
// The recorded REASON is runEndOverran for all four. A fifth end_reason value
// would be a wire change plus a client sentence, which this did not ratify — so
// the cost is stated rather than hidden: a stalled run's History row reads "it
// ran past its time limit", which is true but says the wrong thing about why.
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

// backstopSpent reports whether the run's absolute executing-time budget is gone
// at this instant, which is what tells the two expiry messages apart.
//
// It measures the OPEN stretch too, not just the banked total: the backstop's own
// deadline fires mid-stretch by construction, so a version reading only `executed`
// would report every backstop expiry as a stall.
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
// translate.RunBoundsAccess.
//
// Cancelling the whole RUN is the only enforcement available: every C→A
// workflow verb vibekit can issue is run-scoped, so there is no way to stop
// one step and let its run continue.
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
// for a caller that has already WON the termination claim.
//
// Not the public Cancel, deliberately: that one claims, so a bound calling it would
// race itself and refuse its own cancel. A failure is reported AND handed to
// finishTermination's own ladder: the run breached its bound whether or not the
// cancel landed, so the line is the operator's while the ladder keeps trying.
func (rs *Runs) cancelBounded(workflowID, reason string) {
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()
	if err := rs.finishTermination(ctx, workflowID, reason, nil); err != nil {
		slog.Error("could not cancel a run that breached its bound",
			"workflow_id", workflowID, "error", err)
	}
}

// runStartLaunch classifies a lease minted from a `run_start` frame by the
// CARRIER the frame arrived on, which is the only thing that actually says
// who launched the run.
//
// A real chat id means the frame came up a CHAT's bridge — an agent-launched
// run, chat-parented by construction and excluded from the orphan sweep's
// cancel arm because the chat rehydrate's resume sweep owns it.
//
// Anything else is PARENTLESS: run-bridge frames are dispatched with an empty
// chat id, and the bridge itself is registered under the synthetic
// `run:<id>`. Such a run is vibekit's own manual work and MUST be sweepable.
//
// Inferring the origin from lease ABSENCE, as this used to, was wrong for a
// retry: a retry re-hosts a parentless run and grants its lease after the
// retry call returns, while the first lifecycle frame can arrive before that
// — so `run_start` landing first saw no lease and stamped OriginAgent on a
// run no chat owns, making it permanently unsweepable.
func runStartLaunch(chatID vibekit.ChatID) launchOrigin {
	if chatID == "" || isRunChat(chatID) {
		return launchOrigin{origin: runlease.OriginManual}
	}
	return launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)}
}

// observeStart arms the run's deadline, then hands the frame to the
// translator.
//
// `run_start` is the arming point that covers the launch path vibekit does
// not own: KAS's RunWorkflowTool creates and invokes an agent-launched run
// internally, so this frame is the FIRST thing vibekit sees of it — which is
// why the lease for that population is minted HERE rather than at a launch
// verb.
//
// The frame's own WorkflowName is the recipe, the same string KAS's run list
// reports.
//
// The launch verbs arm too, so a run this process started is bounded from
// the instant it started; the arm is idempotent, so the earlier one wins.
//
// It also re-arms a RESUMED run, because a pause parks the deadline, and
// each arm is a fresh budget of EXECUTING time. The tab offer runs before the
// translator: it reads the lease just granted, so the tab exists by the time the
// client's `run_started` frame lands.
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

// observeComplete drops the bounds AND the step-session registry of a TERMINAL
// run, then translates.
//
// Non-terminal run_complete frames keep the arm: KAS reports an
// `onMaxIterations` policy pause through this same frame, and that run is
// still this process's to resume.
//
// The registry rides the same gate for the same reason, and it used to ride none
// at all — the translator wiped it on every `run_complete`, so a step parked on a
// question (`status: paused`, seconds after the ask, minutes before the resume)
// emptied it mid-run. What that costs is the run id on the NEXT ask of the
// resumed run: `refFor` misses, `omitempty` keeps `run_id` off the wire, and the
// ask lands under the launching chat's id where no run-scoped remover can see it.
// The step-driven TURN rides the same gate too, and it is the only one of the
// three whose subject is the launching chat rather than the run: a chat-parented
// run's step frames fold onto that chat and open a turn there, and the bracket path
// cannot close it because the attribution gate drops a step's own turn_end. So the
// run reaching terminal is the only thing left, and `paused` must not do it — that
// run is still this process's to resume and its next step folds into the same turn.
func (rs *Runs) observeComplete(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	if f := decodeLifecycleFrame(msg); f.WorkflowID != "" && terminalRunStatus(f.Status) {
		// FIRST, ahead of HandleRunComplete: the close persists the step's assistant
		// message and broadcasts its `message_appended`, and the client repaints the run
		// on the `run_finished` invalidation the translator emits — so the content has
		// to be on disk before the repaint that goes looking for it.
		rs.closeStepTurn(ctx, chatID)
		rs.forgetBounds(ctx, f.WorkflowID)
		rs.translate.ForgetRunSteps(f.WorkflowID)
	}
	rs.translate.HandleRunComplete(ctx, chatID, msg)
}

// closeStepTurn closes the launching chat's step-driven turn, if it has one.
//
// The `rs.coord == nil` guard is for the bare &Runs{} a bounds test builds — coord
// is a REQUIRED field, and clearRunPerms and offerRunTab carry the same guard for
// the same reason.
//
// An empty or `run:` chat id is a PARENTLESS run, which folds onto no chat and so
// has no turn to close: run-bridge frames are dispatched with an empty chat id and
// the bridge is registered under the synthetic `run:<id>`, which is the same
// distinction runStartLaunch makes.
//
// It runs on the bridge's Forward goroutine, SYNCHRONOUSLY, deliberately: that is
// where WireTurnEnd already finalizes turns from, a goroutine would break the
// ordering the caller depends on, and it would race the shutdown drain.
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

// lifecycleFrame is the three fields the bounds read off a workflow lifecycle
// frame. Deliberately its own minimal decode rather than a share of
// translate's wire structs, which are the translator's own contract with
// KAS.
//
// WorkflowName is on `run_start` and is the recipe name KAS's own run list
// reports, so an agent-launched run's lease is keyed consistently with every
// other one.
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
