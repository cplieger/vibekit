package agent

// Run bounds: ONE wall clock every run gets, the per-step turn cap, and the one
// field that lets a run's row say which of those stopped it.
//
// ONE CLOCK, TWO INPUTS. The universal ceiling and a scheduled run's own next
// slot are INPUTS to the single deadline the run's lease carries
// (runlease.NextDeadline): the tighter of the two, floored so no run is handed
// a budget it cannot finish inside. Before this they were two independent
// mechanisms, so a manual run of a scheduled recipe held that recipe for the
// whole ceiling and refused every slot underneath it.
//
// The deadline is MUTABLE and lives on the lease, which is what keeps the bound
// on EXECUTING time: every start re-stamps it, every pause parks it, so a run
// deliberately held for a week is not cancelled for having been held. A zero
// deadline means vibekit is not bounding the run.
//
// TWO BOUNDS, NOT THREE. The run clock and the per-step turn cap; there is
// deliberately no per-step wall clock (a step is already bounded by its turn
// cap and by the run clock above it) and no token budget (vibekit's per-step
// meter reads zero — a step is not a chat).
//
// NEITHER IS A SETTING: a backstop the user can raise stops being a backstop.
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

// runCeiling is how long any single run may execute before it is cancelled.
//
// KiroCrew's `_RUN_TIMEOUT_SECS` default (3600s) taken as the starting point,
// the one comparable figure measured against real unattended workflow runs.
// A CONSTANT, deliberately: no caller supplies one and no caller may.
const runCeiling = time.Hour

// minRunBudget is the smallest EXECUTING budget any run may be given, whatever
// the other input says.
//
// DERIVED, not chosen: it is internal/schedule's own minimum interval
// (minMinuteInterval, 5 minutes), the shortest repeat the schedule form will
// accept — so a run gets at least as long as the tightest schedule this app
// allows. Change it with internal/schedule/spec.go's minMinuteInterval.
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

// logMsgRunCeiling / logMsgStepCap / logMsgRunOrphaned are CONSTANTS because a
// homelab Loki rule keys on the message.
const (
	logMsgRunCeiling  = "run exceeded its wall-clock ceiling; cancelling"
	logMsgStepCap     = "workflow step exceeded its turn cap; cancelling the run"
	logMsgRunOrphaned = "run was orphaned by a restart; cancelling so its recipe is idle again"
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
	// cancel, schedule deadline, universal ceiling and step cap. Exactly one
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
	// reasons maps a workflow id to why it was stopped; order is the FIFO
	// eviction queue for it.
	reasons map[string]string
	order   []string
}

// armDeadline gives a run its deadline and the timer that enforces it.
//
// IDEMPOTENT on an already-bounded run: `run_start` re-fires on every resume
// and the launch verbs arm too, so a run is armed more than once by design
// and the EARLIEST arm wins. A resumed run gets a fresh budget because the
// pause parked its lease.
//
// A run with no lease is not armed at all — the only runs reaching here
// without one are the TUI's, which vibekit does not host and has no business
// bounding.
//
// AfterFunc rather than a goroutine: it parks nothing while waiting.
//
// ONE TRANSACTION: the launch verb arms after `invoke` while that run's own
// `run_start` frame is already arriving on its bridge, so two arms race on
// every launch. As three separately-locked steps, both callers could compute
// two deadlines and install their timers in different orders, leaving the
// lease carrying deadline B while only timer A survived. So the idempotence
// check, the store and the timer swap happen under one hold of the mutex.
//
// Lock order is the one taken nowhere else in reverse: mu, then the lease
// store's — leaseStore() takes mu itself, so it is resolved BEFORE the hold.
func (rs *Runs) armDeadline(ctx context.Context, workflowID string) {
	if workflowID == "" {
		return
	}
	store := rs.leaseStore()
	rs.mu.Lock()
	defer rs.mu.Unlock()
	l, ok := store.Get(workflowID)
	if !ok || l.Bounded() {
		return
	}
	deadline := runlease.NextDeadline(time.Now(), runCeiling, minRunBudget, l.SlotAt)
	err := store.SetDeadline(ctx, workflowID, deadline)
	if errors.Is(err, runlease.ErrNotFound) {
		// The lease went away between the read and the write. There is nothing
		// to bound, and arming a timer would schedule a cancel against a run
		// whose envelope this process no longer holds.
		return
	}
	if err != nil {
		// DURABILITY ONLY. SetDeadline sets the in-memory deadline whenever the
		// lease exists, so the store DOES carry this deadline — returning here
		// would leave the lease bounded with no timer, permanent because the
		// idempotence check above then always skips arming one.
		slog.Error("a run's deadline is not durable, so it will not survive a restart; "+
			"this process still bounds the run",
			"workflow_id", workflowID, "deadline", deadline, "error", err)
	}
	rs.setTimerLocked(workflowID, deadline)
}

// setTimerLocked installs the one timer a run's current deadline gets,
// replacing any timer left from an earlier one. The caller holds the mutex,
// because installing the timer is the last step of the arm's transaction.
//
// The replaced timer is STOPPED rather than forgotten: an un-stopped AfterFunc
// stays live until its own deadline, and a run cycling through pause and
// resume would accumulate one per cycle.
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
	if err := rs.leaseStore().SetDeadline(ctx, workflowID, time.Time{}); err != nil {
		slog.Warn("could not park a run's deadline", "workflow_id", workflowID, "error", err)
	}
	rs.stopTimer(workflowID)
	return true
}

// bounded reports whether vibekit currently believes the run to be EXECUTING
// under a deadline it set — the step cap's gate, which must not CLEAR that
// deadline the way the timer's own callback path does: a breach that loses the
// termination claim leaves the wall clock to whoever won it.
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
// what makes the two bounds distinguishable from a person.
func (rs *Runs) finishTermination(ctx context.Context, workflowID, reason string) error {
	rs.disarmDeadline(ctx, workflowID)
	rs.recordEnd(workflowID, reason)
	err := rs.cancelRPC(ctx, workflowID)
	if err != nil {
		rs.releaseTermination(workflowID)
	}
	return err
}

// forgetBounds drops what a run that stopped executing for good no longer
// needs: its wall clock, its termination claim and its lease.
//
// The lease goes HERE because this is the one site every origin reaches: an
// agent-parented run has no bridge of its own to close, and its lease must
// be released all the same.
//
// The recorded REASON deliberately survives — the History row reads it after
// the run finished, which is the only moment it is useful.
func (rs *Runs) forgetBounds(ctx context.Context, workflowID string) {
	rs.stopTimer(workflowID)
	rs.releaseTermination(workflowID)
	rs.clearHeals(workflowID)
	rs.releaseLease(ctx, workflowID)
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
	if _, ok := rs.bounds.reasons[workflowID]; !ok {
		return
	}
	delete(rs.bounds.reasons, workflowID)
	// The order slice is the eviction QUEUE for the map, so leaving a
	// dangling entry would let the map grow past its cap.
	rs.bounds.order = slices.DeleteFunc(rs.bounds.order,
		func(id string) bool { return id == workflowID })
}

// rearmRetried gives a re-driven run a clean row and a FRESH wall clock.
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
// recipe is bounded by the ceiling alone.
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
// written. The test is "the slot is set and is not AFTER the deadline", not
// equality with it: NextDeadline has THREE possible answers (ceiling, slot,
// or floor), and the floor outranks the slot, so a slot already gone or
// closer than minRunBudget produces a deadline LATER than SlotAt. Equality
// misclassified that as a ceiling breach, logging the wrong message and
// skipping the schedule row.
//
// Three outcomes: the two ERROR-level constants are matched by homelab Loki
// rules, and a manual run standing aside for its recipe's next slot is
// neither — it is the bound working, so it logs at INFO instead of paging
// somebody.
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
	default:
		slog.Error(logMsgRunCeiling, "workflow_id", workflowID, "ceiling", runCeiling.String())
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
		// Already terminating — a sibling step, the ceiling, or the user
		// cancelled first.
		return
	}
	slog.Error(logMsgStepCap,
		"workflow_id", workflowID, "node_id", nodeID, "turns", turns, "cap", translate.StepTurnCap)
	rs.cancelBounded(workflowID, runEndStepCap)
}

// cancelBounded issues the cancel both bounds end in, for a caller that has
// already WON the termination claim.
//
// Not the public Cancel, deliberately: that one claims, so a bound calling
// it would race itself and refuse its own cancel. Reported on failure rather
// than retried: the run breached its bound whether or not the cancel
// landed.
func (rs *Runs) cancelBounded(workflowID, reason string) {
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()
	if err := rs.finishTermination(ctx, workflowID, reason); err != nil {
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
// each arm is a fresh budget of EXECUTING time.
func (rs *Runs) observeStart(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	if f := decodeLifecycleFrame(msg); f.WorkflowID != "" {
		if _, held := rs.lease(f.WorkflowID); !held {
			rs.grantLease(ctx, f.WorkflowID, f.WorkflowName, runStartLaunch(chatID))
		}
		rs.armDeadline(ctx, f.WorkflowID)
	}
	rs.translate.HandleRunStart(ctx, chatID, msg)
}

// observeComplete drops the bounds of a TERMINAL run, then translates.
//
// Non-terminal run_complete frames keep the arm: KAS reports an
// `onMaxIterations` policy pause through this same frame, and that run is
// still this process's to resume.
func (rs *Runs) observeComplete(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	if f := decodeLifecycleFrame(msg); f.WorkflowID != "" && terminalRunStatus(f.Status) {
		rs.forgetBounds(ctx, f.WorkflowID)
	}
	rs.translate.HandleRunComplete(ctx, chatID, msg)
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
