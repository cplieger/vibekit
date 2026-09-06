package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/schedule"
	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// leased grants a manual lease so a bounds test has something to arm: a run with no
// lease is deliberately unbounded, so a fixture that forgot this passes vacuously.
func leased(t *testing.T, h *Runs, workflowID string) {
	t.Helper()
	h.grantLease(t.Context(), workflowID, "publish", manualLaunch())
}

// undurableLeaseStore returns a lease store whose every write fails: the parent it
// would write into is a regular FILE, so ENOTDIR at any uid — a mode-based fixture
// gates nothing under root. The in-memory half of the store is untouched.
func undurableLeaseStore(t *testing.T) *runlease.Store {
	t.Helper()
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("stage the unwritable store: %v", err)
	}
	st, _ := runlease.NewStore(notADir) // the error is diagnostic; the store is usable
	return st
}

// TestArmRunDeadline_FeedsTheLeasesSlotIntoTheOneDeadline covers the WIRING only;
// runlease.NextDeadline owns the arithmetic. A manual run of a scheduled recipe is a
// property of the LAUNCH, covered by
// TestLaunchRun_ManualRunOfAScheduledRecipeYieldsToItsNextSlot.
func TestArmRunDeadline_FeedsTheLeasesSlotIntoTheOneDeadline(t *testing.T) {
	for name, tc := range map[string]struct {
		slotIn time.Duration
		// spent is the only input that can make the backstop the tightest.
		spent  time.Duration
		wantIn time.Duration
	}{
		"no slot: the idle window is the whole bound":  {0, 0, runIdleWindow},
		"a slot inside the window wins":                {10 * time.Minute, 0, 10 * time.Minute},
		"a slot beyond the window loses":               {24 * time.Hour, 0, runIdleWindow},
		"a slot inside the floor is floored up":        {30 * time.Second, 0, minRunBudget},
		"a slot already gone is floored, not honoured": {-time.Minute, 0, minRunBudget},
		// The remainder is above minRunBudget deliberately: below it the floor answers.
		"a nearly-spent backstop wins over the window": {0, runBackstop - 7*time.Minute, 7 * time.Minute},
		// Deliberately in the PAST: the floor may not lift a spent backstop.
		"a spent backstop is honoured, not floored": {0, runBackstop + time.Hour, -time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			h := &Runs{}
			const id = "wf_1"
			o := manualLaunch()
			if tc.slotIn != 0 {
				o = scheduledLaunch("sched-1", time.Now().Add(tc.slotIn))
			}
			h.grantLease(t.Context(), id, "publish", o)
			if tc.spent != 0 {
				h.bounds.executed = map[string]time.Duration{id: tc.spent}
			}
			// A spent backstop's timer fires the moment the arm installs it. Taking the
			// claim first makes that callback refuse, so the STAMPED VALUE can be read
			// without racing the cancel it triggers.
			if !h.claimTermination(id) {
				t.Fatal("the fresh run already held a termination claim")
			}

			before := time.Now()
			h.armDeadline(t.Context(), id)
			// A window rather than an equality: the arm reads its own clock.
			inWindow := func(what string) {
				t.Helper()
				l, ok := h.lease(id)
				if !ok || !l.Bounded() {
					t.Fatalf("%s: the run holds no deadline, so nothing bounds it", what)
				}
				if got := l.Deadline.Sub(before); got < tc.wantIn-time.Second || got > tc.wantIn+time.Second {
					t.Errorf("%s: deadline is %v out, want ~%v", what, got.Round(time.Second), tc.wantIn)
				}
			}
			inWindow("the arm")

			// A refill recomputes the bound already granted rather than granting a fresh
			// one, and only spends a write past refillGranularity — hence the ageing.
			aged, _ := h.lease(id)
			if err := h.leaseStore().SetDeadline(t.Context(), id,
				aged.Deadline.Add(-refillGranularity-time.Second)); err != nil {
				t.Fatalf("age the stored deadline: %v", err)
			}
			h.refillDeadline(t.Context(), id)
			inWindow("after a refill")
		})
	}
}

// TestArmRunDeadline_ConcurrentArmsLeaveALiveTimerForTheStoredDeadline asserts the
// INVARIANT the arm's transaction establishes — a bounded lease always has a timer —
// rather than the divergent end state, which a probe reached in ~3% of rounds. The
// oracle is an observer reading the timer map and the lease under ONE hold of the
// mutex, plus making the surviving timer FIRE (Reset reschedules the same func with
// its captured deadline, so a mismatched survivor records nothing). The store is
// DISK-BACKED so the persist widens the window from nanoseconds to a file write.
func TestArmRunDeadline_ConcurrentArmsLeaveALiveTimerForTheStoredDeadline(t *testing.T) {
	const rounds, arms = 6, 4
	for round := range rounds {
		h, _, br := newTestHub()
		id := "wf_" + strconv.Itoa(round)
		br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
		h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
		st, err := runlease.NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("round %d: NewStore: %v", round, err)
		}
		h.runs.leases = st
		h.runs.grantLease(t.Context(), id, "publish", manualLaunch())

		release, halt := make(chan struct{}), make(chan struct{})
		torn := make(chan time.Time, 1)
		var wg, obs sync.WaitGroup
		obs.Go(func() {
			for {
				select {
				case <-halt:
					return
				default:
				}
				// ONE observation of both halves, through the store directly because
				// h.runs.lease would take the same mutex again.
				h.runs.mu.Lock()
				_, hasTimer := h.runs.bounds.timers[id]
				l, held := st.Get(id)
				h.runs.mu.Unlock()
				if held && l.Bounded() && !hasTimer && len(torn) == 0 {
					torn <- l.Deadline
				}
			}
		})
		for range arms {
			wg.Go(func() {
				<-release
				h.runs.armDeadline(t.Context(), id)
			})
		}
		close(release)
		wg.Wait()
		close(halt)
		obs.Wait()

		if len(torn) > 0 {
			t.Fatalf("round %d: the lease was bounded for deadline %v while no timer existed, so "+
				"the check, the store and the timer install are not one transaction — two arms can "+
				"leave the lease carrying one deadline and the surviving timer armed for another",
				round, <-torn)
		}

		l, ok := h.runs.lease(id)
		if !ok || !l.Bounded() {
			t.Fatalf("round %d: no arm recorded a deadline", round)
		}
		h.runs.mu.Lock()
		timer := h.runs.bounds.timers[id]
		timers := len(h.runs.bounds.timers)
		h.runs.mu.Unlock()
		if timer == nil {
			t.Fatalf("round %d: the arms left no timer, so nothing can ever stop the run", round)
		}
		if timers != 1 {
			t.Fatalf("round %d: %d arms left %d timers, want 1", round, arms, timers)
		}

		// And the survivor is armed for what the lease holds.
		timer.Reset(time.Millisecond)
		stop := time.Now().Add(2 * time.Second)
		for h.runs.endReason(id) == "" {
			if time.Now().After(stop) {
				t.Fatalf("round %d: the surviving timer was armed for a deadline the lease no "+
					"longer holds (lease says %v), so the run reads as bounded with no callback "+
					"that can act on it", round, l.Deadline)
			}
			time.Sleep(time.Millisecond)
		}
		if got := h.runs.endReason(id); got != runEndOverran {
			t.Fatalf("round %d: the fired timer recorded %q, want %q", round, got, runEndOverran)
		}
	}
}

// TestArmRunDeadline_KeepsBoundingWhenOnlyDurabilityFails: SetDeadline reports only
// the persist, so refusing to bound on its error leaves the lease BOUNDED with no
// timer — which the arm's idempotence check then makes permanent.
func TestArmRunDeadline_KeepsBoundingWhenOnlyDurabilityFails(t *testing.T) {
	logs := captureLogs(t)
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	h.runs.leases = undurableLeaseStore(t)

	h.runs.grantLease(t.Context(), id, "publish", manualLaunch())
	if _, held := h.runs.lease(id); !held {
		t.Fatal("a persist failure lost the lease from memory as well")
	}

	h.runs.armDeadline(t.Context(), id)

	l, _ := h.runs.lease(id)
	if !l.Bounded() {
		t.Fatal("the run took no deadline at all")
	}
	h.runs.mu.Lock()
	timer := h.runs.bounds.timers[id]
	h.runs.mu.Unlock()
	if timer == nil {
		t.Fatal("a run whose deadline could not be persisted got no timer, so it reads as " +
			"bounded and nothing can ever stop it")
	}
	// A live callback rather than a map entry: fire it and watch the run end.
	timer.Reset(time.Millisecond)
	stop := time.Now().Add(5 * time.Second)
	for h.runs.endReason(id) == "" {
		if time.Now().After(stop) {
			t.Fatal("the installed timer's callback could not act on the run")
		}
		time.Sleep(time.Millisecond)
	}
	if got := h.runs.endReason(id); got != runEndOverran {
		t.Errorf("recorded %q, want %q", got, runEndOverran)
	}
	// The log line is the whole compensation: a restart silently loses the clock.
	const wantLine = "a run's deadline is not durable, so it will not survive a restart; this process still bounds the run"
	if out := logs.String(); !strings.Contains(out, `"msg":"`+wantLine+`"`) {
		t.Errorf("a run whose deadline could not be persisted was bounded silently; want a "+
			"line reading %q. Got: %s", wantLine, out)
	}
	if out := logs.String(); !strings.Contains(out, `"workflow_id":"`+id+`"`) {
		t.Errorf("the durability line does not name the run it is about: %s", out)
	}
}

// TestDisarmRunDeadline_ParksInMemoryWhenTheParkCannotBePersisted is the disarm's
// half of the arm's durability split: refusing to park on a failed persist leaves
// the lease BOUNDED, which the arm's idempotence check then makes permanent.
func TestDisarmRunDeadline_ParksInMemoryWhenTheParkCannotBePersisted(t *testing.T) {
	logs := captureLogs(t)
	h := &Runs{leases: undurableLeaseStore(t)}
	const id = "wf_1"
	leased(t, h, id)
	h.armDeadline(t.Context(), id)

	if !h.disarmDeadline(t.Context(), id) {
		t.Fatal("the disarm reported holding no deadline, so the run stays bounded with no timer")
	}
	if l, _ := h.lease(id); l.Bounded() {
		t.Errorf("the parked lease still carries deadline %v", l.Deadline)
	}
	const wantLine = "could not park a run's deadline"
	if out := logs.String(); !strings.Contains(out, `"msg":"`+wantLine+`"`) {
		t.Errorf("a park that lost its durability said nothing; want a line reading %q. Got: %s",
			wantLine, out)
	}
}

// TestCancelExpiredRun_ReportsAScheduleRowItCouldNotWrite: the run is cancelled
// whether or not the row lands, so a failed write is the silence the outcome exists
// to remove. Reachable when a schedule is DELETED while its run executes.
func TestCancelExpiredRun_ReportsAScheduleRowItCouldNotWrite(t *testing.T) {
	logs := captureLogs(t)
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	// An EMPTY schedule store: the lease names a schedule that is no longer there.
	st, err := schedule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("schedule.NewStore: %v", err)
	}
	h.runs.schedules = st

	h.runs.grantLease(t.Context(), id, "nightly", scheduledLaunch("sched-gone", time.Now().Add(30*time.Second)))
	h.runs.armDeadline(t.Context(), id)
	l, _ := h.runs.lease(id)

	h.runs.cancelExpired(id, l.Deadline)

	if got := h.runs.endReason(id); got != runEndOverran {
		t.Errorf("endReason = %q, want %q; the run is cancelled whatever the row does", got, runEndOverran)
	}
	const wantLine = "could not record the schedule's outcome"
	if out := logs.String(); !strings.Contains(out, `"msg":"`+wantLine+`"`) {
		t.Errorf("the schedule row could not be written and nothing said so; want a line reading "+
			"%q. Got: %s", wantLine, out)
	}
	if out := logs.String(); !strings.Contains(out, `"schedule_id":"sched-gone"`) {
		t.Errorf("the failed-outcome line does not name the schedule it is about: %s", out)
	}
}

// TestStepTurnCap_ReportsACancelItCouldNotIssue: the breach is reported, and the
// claim is handed BACK so the user's Cancel still works on a run vibekit failed
// to stop.
func TestStepTurnCap_ReportsACancelItCouldNotIssue(t *testing.T) {
	logs := captureLogs(t)
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("bridge gone")}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.grantLease(t.Context(), id, "publish", manualLaunch())
	h.runs.armDeadline(t.Context(), id)

	h.runs.StepTurnCapExceeded(id, "node-3", translate.StepTurnCap+1)

	const wantLine = "could not cancel a run that breached its bound"
	if out := logs.String(); !strings.Contains(out, `"msg":"`+wantLine+`"`) {
		t.Errorf("a bound whose cancel failed reported nothing; want a line reading %q. Got: %s",
			wantLine, out)
	}
	if !h.runs.claimTermination(id) {
		t.Error("the run stayed claimed after its cancel failed, so the user's own Cancel " +
			"silently does nothing on a run that is still executing")
	}
}

// TestArmRunDeadline_IsIdempotent: `run_start` re-fires on every resume and the
// launch verbs arm too, so the EARLIEST arm must win or a run emitting frames
// extends its own budget indefinitely.
func TestArmRunDeadline_IsIdempotent(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	leased(t, h, id)

	h.armDeadline(t.Context(), id)
	first, _ := h.lease(id)
	h.armDeadline(t.Context(), id)
	h.armDeadline(t.Context(), id)
	after, _ := h.lease(id)

	if !after.Deadline.Equal(first.Deadline) {
		t.Errorf("a second arm moved the deadline from %v to %v, so a run emitting frames "+
			"extends its own budget", first.Deadline, after.Deadline)
	}
	// One timer, whatever the arm count: a second live timer means a second callback.
	h.mu.Lock()
	timers := len(h.bounds.timers)
	h.mu.Unlock()
	if timers != 1 {
		t.Errorf("three arms left %d timers, want 1", timers)
	}
}

// TestArmRunDeadline_RefusesARunWithNoLease: a TUI-launched run has no lease, no
// bridge here and no cancel path vibekit owns, so arming a timer would schedule a
// cancel against a run this process cannot certify.
func TestArmRunDeadline_RefusesARunWithNoLease(t *testing.T) {
	h := &Runs{}
	h.armDeadline(t.Context(), "wf_tui")
	if h.bounded("wf_tui") {
		t.Error("a run with no lease was bounded")
	}
	h.mu.Lock()
	timers := len(h.bounds.timers)
	h.mu.Unlock()
	if timers != 0 {
		t.Errorf("a leaseless run left %d timers behind", timers)
	}
}

// TestDisarmRunDeadline_ParksTheLeaseAndStopsTheTimer: clearing the LEASE is the
// load-bearing half, because a stale deadline makes the next re-arm skip the run as
// "already bounded" and it is never bounded again.
func TestDisarmRunDeadline_ParksTheLeaseAndStopsTheTimer(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	leased(t, h, id)
	h.armDeadline(t.Context(), id)

	h.mu.Lock()
	timer := h.bounds.timers[id]
	h.mu.Unlock()
	if timer == nil {
		t.Fatal("the arm installed no timer, so nothing can ever stop the run")
	}

	if !h.disarmDeadline(t.Context(), id) {
		t.Fatal("the disarm reported holding no deadline")
	}
	if l, _ := h.lease(id); l.Bounded() {
		t.Errorf("the parked lease still carries deadline %v", l.Deadline)
	}
	// Stop reports false for a timer already stopped, so this proves the first landed.
	if timer.Stop() {
		t.Error("the timer was still live after its run was parked")
	}
	if h.disarmDeadline(t.Context(), id) {
		t.Error("a parked run reported holding a deadline")
	}
	if h.disarmDeadline(t.Context(), "wf_never_armed") {
		t.Error("an unleased run reported holding a deadline")
	}
	if h.disarmDeadline(t.Context(), "") {
		t.Error("the empty workflow id reported holding a deadline")
	}
}

// TestRunDeadline_ResumeGetsAFreshBudgetRatherThanARemainder asserts the
// ARITHMETIC: the resumed budget is a FULL idle window measured from the resume.
// "Later than the first deadline" is true of every remainder bug, because the two
// arms are microseconds apart on a real clock.
func TestRunDeadline_ResumeGetsAFreshBudgetRatherThanARemainder(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	leased(t, h, id)

	h.armDeadline(t.Context(), id)
	first, _ := h.lease(id)

	if !h.disarmDeadline(t.Context(), id) { // the pause
		t.Fatal("the pause reported holding no deadline")
	}
	// A parked lease carries NO deadline, so there is nothing left to subtract from.
	if parked, _ := h.lease(id); parked.Bounded() {
		t.Fatalf("the parked lease still carries deadline %v, so a resume could compute a "+
			"remainder from it", parked.Deadline)
	}

	resumedAt := time.Now()
	h.armDeadline(t.Context(), id) // the resume

	second, _ := h.lease(id)
	if !second.Bounded() {
		t.Fatal("the resumed run took no deadline")
	}
	// A window rather than an equality because the arm reads its own clock, but one
	// tight enough to exclude the first arm's leftover budget and any fraction of it.
	if budget := second.Deadline.Sub(resumedAt); budget < runIdleWindow-time.Second || budget > runIdleWindow+time.Second {
		t.Errorf("the resumed run got %v of budget, want a full %v measured from the resume; "+
			"a resumed run must not inherit the remainder of the clock it parked with",
			budget.Round(time.Millisecond), runIdleWindow)
	}
	if !second.Deadline.After(first.Deadline) {
		t.Errorf("the resume kept deadline %v (first was %v)", second.Deadline, first.Deadline)
	}
}

// TestCancelExpiredRun_ASupersededTimerDoesNothing: `Timer.Stop` does not halt an
// already-running func, so a callback that fired just before a pause is in flight
// while the resume re-stamps a fresh deadline. Calling the callback directly with the
// old deadline is exactly that in-flight state.
func TestCancelExpiredRun_ASupersededTimerDoesNothing(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	leased(t, h, id)

	h.armDeadline(t.Context(), id)
	stale, _ := h.lease(id)

	h.disarmDeadline(t.Context(), id) // the pause
	h.armDeadline(t.Context(), id)    // the resume
	live, _ := h.lease(id)
	if live.Deadline.Equal(stale.Deadline) {
		t.Fatal("the resume reused the same deadline, so the guard cannot distinguish them")
	}

	// The stale timer's callback, arriving now.
	if h.claimExpiredDeadline(id, stale.Deadline) {
		t.Fatal("a superseded timer claimed the resumed run; it would be cancelled after the " +
			"old deadline's remainder")
	}
	if !h.bounded(id) {
		t.Error("the resumed run lost its deadline to the superseded callback, so nothing bounds it")
	}

	// The CURRENT deadline is the one that may act, and only once.
	if !h.claimExpiredDeadline(id, live.Deadline) {
		t.Error("the live timer's own callback was refused")
	}
	if h.claimExpiredDeadline(id, live.Deadline) {
		t.Error("the deadline was claimed twice")
	}
	// A released lease: a pending timer must not resurrect a cancel.
	h.releaseLease(t.Context(), id)
	if h.claimExpiredDeadline(id, live.Deadline) {
		t.Error("a released run was claimed by its pending timer")
	}
}

// TestRunDeadline_FiresAndCancelsAtTheDeadline drives the real timer, because every
// other case calls the callback directly and stays green with no AfterFunc installed.
func TestRunDeadline_FiresAndCancelsAtTheDeadline(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	leased(t, h.runs, id)

	deadline := time.Now().Add(20 * time.Millisecond)
	if err := h.runs.leaseStore().SetDeadline(t.Context(), id, deadline); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	// The arm's transaction staged by hand: NextDeadline floors at minRunBudget, so no
	// budget it would compute is short enough to observe.
	h.runs.mu.Lock()
	h.runs.setTimerLocked(id, deadline)
	h.runs.mu.Unlock()

	// A deadline-bounded poll rather than a sleep: it cannot flake into a false pass.
	stop := time.Now().Add(5 * time.Second)
	for h.runs.endReason(id) == "" {
		if time.Now().After(stop) {
			t.Fatalf("the deadline never fired: bounded=%v calls=%v", h.runs.bounded(id), br.callLog())
		}
		time.Sleep(time.Millisecond)
	}
	if got := h.runs.endReason(id); got != runEndOverran {
		t.Errorf("the expired run recorded %q, want %q", got, runEndOverran)
	}
	if !slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Errorf("no cancel went out for the expired run: %v", br.callLog())
	}
}

// TestCancelExpiredRun_AFlooredSlotStillReportsAsTheScheduleBound: the floor outranks
// the slot, so a slot closer than minRunBudget yields a deadline LATER than SlotAt.
// A callback classifying by equality with SlotAt reads that as its own bound and
// skips the schedule row.
func TestCancelExpiredRun_AFlooredSlotStillReportsAsTheScheduleBound(t *testing.T) {
	logs := captureLogs(t)
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	st, err := schedule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("schedule.NewStore: %v", err)
	}
	entry := schedule.Entry{
		ID: "sched-1", Source: "bundled://nightly", Enabled: true,
		Spec: schedule.Spec{Freq: schedule.FreqDaily, Hour: 2},
	}
	if pErr := st.Put(t.Context(), &entry); pErr != nil {
		t.Fatalf("Put schedule: %v", pErr)
	}
	h.runs.schedules = st

	// A slot INSIDE the floor, so the armed deadline lands later than SlotAt.
	h.runs.grantLease(t.Context(), id, "nightly", scheduledLaunch("sched-1", time.Now().Add(30*time.Second)))
	h.runs.armDeadline(t.Context(), id)
	l, _ := h.runs.lease(id)
	if !l.Deadline.After(l.SlotAt) {
		t.Fatalf("the fixture did not produce a floor-adjusted deadline: slot %v, deadline %v",
			l.SlotAt, l.Deadline)
	}

	h.runs.cancelExpired(id, l.Deadline)

	if got := h.runs.endReason(id); got != runEndOverran {
		t.Errorf("endReason = %q, want %q", got, runEndOverran)
	}
	if out := logs.String(); !strings.Contains(out, logMsgRunOverran) {
		t.Errorf("the callback did not log the schedule bound (%q); a floor-adjusted slot is "+
			"still the slot, and the ceiling message would page the operator about the wrong "+
			"thing. Got: %s", logMsgRunOverran, out)
	}
	if out := logs.String(); strings.Contains(out, logMsgRunStalled) {
		t.Errorf("the callback reported a stall for a run cancelled at its schedule "+
			"bound: %s", out)
	}
	// The row, which is the half a reader actually sees.
	rows := st.List()
	if len(rows) != 1 {
		t.Fatalf("the schedule store holds %d rows", len(rows))
	}
	if rows[0].LastResult != outcomeOverran {
		t.Errorf("the schedule row reads %q, want the overran outcome; without it the row still "+
			"says `started` while the schedule has silently stopped producing", rows[0].LastResult)
	}
}

// TestCancelExpiredRun_AManualRunYieldingToASlotIsNotAScheduleFailure: a homelab Loki
// rule reads logMsgRunOverran as "a schedule stopped producing", so reusing it for a
// manual run standing aside for its slot would page somebody for correct behaviour.
func TestCancelExpiredRun_AManualRunYieldingToASlotIsNotAScheduleFailure(t *testing.T) {
	logs := captureLogs(t)
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	// A manual lease carrying a slot, and no schedule id: no row asked for this run.
	h.runs.grantLease(t.Context(), id, "publish",
		launchOrigin{origin: runlease.OriginManual, slotAt: time.Now().Add(10 * time.Minute)})
	h.runs.armDeadline(t.Context(), id)
	l, _ := h.runs.lease(id)

	h.runs.cancelExpired(id, l.Deadline)

	if got := h.runs.endReason(id); got != runEndOverran {
		t.Errorf("endReason = %q, want %q; the run did run past its bound", got, runEndOverran)
	}
	out := logs.String()
	if !strings.Contains(out, logMsgRunYieldedToSlot) {
		t.Errorf("the manual run's cancellation was not reported as yielding to its slot: %s", out)
	}
	if strings.Contains(out, logMsgRunOverran) {
		t.Errorf("a manual run yielding to a slot logged the schedule-failure message, which a "+
			"homelab alert rule keys on: %s", out)
	}
	if strings.Contains(out, logMsgRunStalled) {
		t.Errorf("the stall message was logged for a run cancelled at its slot: %s", out)
	}
}

// TestClaimRunTermination_IsTakenOnce: four callers race for the claim — the user's
// Cancel, a schedule's repeat interval, the wall clock and a step's turn cap — and
// only one may cancel and record.
func TestClaimRunTermination_IsTakenOnce(t *testing.T) {
	t.Parallel()
	h := &Runs{}

	if !h.claimTermination("wf_1") {
		t.Fatal("the first claim failed")
	}
	if h.claimTermination("wf_1") {
		t.Error("a second caller also took the claim; both would cancel and record")
	}
	// Independent per run: one run terminating must not stop another's cancel.
	if !h.claimTermination("wf_2") {
		t.Error("an unrelated run could not be terminated")
	}
	if h.claimTermination("") {
		t.Error("the empty workflow id took a claim")
	}
}

// TestClaimRunTermination_UserCancelBeatsALaterBound: a user cancel records NOTHING,
// which is the only thing distinguishing it from the two bounds on the History row,
// so a bound claiming alongside it rewrites what the user did.
func TestClaimRunTermination_UserCancelBeatsALaterBound(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	leased(t, h, id)
	h.armDeadline(t.Context(), id)

	// The user's cancel, arriving first and recording nothing.
	if !h.claimTermination(id) {
		t.Fatal("the user's cancel could not claim the run")
	}
	h.recordEnd(id, "")

	// The deadline's stored value still matches, so only the claim can stop it writing.
	l, _ := h.lease(id)
	if h.claimExpiredDeadline(id, l.Deadline) {
		t.Fatal("the deadline claimed a run the user had already cancelled")
	}

	if got := h.endReason(id); got != "" {
		t.Errorf("the row reads %q for a user cancel; the absence IS the third value", got)
	}
}

// TestClaimRunTermination_ScheduleDeadlineAndStepCapCannotBothRecord: the first
// reason stands, or the later write overwrites it and both issue a cancel.
func TestClaimRunTermination_ScheduleDeadlineAndStepCapCannotBothRecord(t *testing.T) {
	t.Parallel()
	h := &Runs{}
	const id = "wf_1"

	// The step cap gets there first.
	if !h.claimTermination(id) {
		t.Fatal("the step cap could not claim the run")
	}
	h.recordEnd(id, runEndStepCap)

	// The schedule deadline, arriving on the same run.
	if h.claimTermination(id) {
		t.Fatal("the schedule deadline claimed a run the step cap was already ending")
	}
	if got := h.endReason(id); got != runEndStepCap {
		t.Errorf("endReason = %q, want the first reason %q", got, runEndStepCap)
	}
}

// TestReleaseRunTermination_ReopensAFailedCancel: holding the claim after a failed
// cancel leaves the Cancel button silently doing nothing on a run still executing.
func TestReleaseRunTermination_ReopensAFailedCancel(t *testing.T) {
	t.Parallel()
	h := &Runs{}

	if !h.claimTermination("wf_1") {
		t.Fatal("the first claim failed")
	}
	h.releaseTermination("wf_1")
	if !h.claimTermination("wf_1") {
		t.Error("the run stayed claimed after its cancel failed, so nothing can stop it")
	}
}

// TestForgetRunBounds_ClearsTheClaimOnATerminalRun: the claim map holds the runs
// currently terminating, not a log of every run that ever was.
func TestForgetRunBounds_ClearsTheClaimOnATerminalRun(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	leased(t, h, id)
	h.armDeadline(t.Context(), id)
	h.claimTermination(id)

	h.forgetBounds(t.Context(), id)

	h.mu.Lock()
	claims := len(h.bounds.terminating)
	timers := len(h.bounds.timers)
	h.mu.Unlock()
	if claims != 0 {
		t.Errorf("the terminal frame left %d claims behind", claims)
	}
	if timers != 0 {
		t.Errorf("the terminal frame left %d timers behind", timers)
	}
	// And the lease: a run that is over has no envelope for a timer to be armed against.
	if _, held := h.lease(id); held {
		t.Error("the terminal frame left the lease behind, so the recipe still reads as busy")
	}
}

// refillingBus refills a run's deadline from INSIDE the teardown, using
// settleAsksForRun's broadcast as the seam — the slowest step in that body, so where a
// concurrent tool-call frame is likeliest to interleave.
type refillingBus struct {
	rs      *Runs
	id      string
	refills int
}

func (b *refillingBus) Broadcast(ctx context.Context, _ vibekit.ServerEvent) {
	b.refills++
	b.rs.refillDeadline(ctx, b.id)
}

// TestForgetRunBounds_ARefillInsideTheTeardownLeavesNoTimer pins the teardown's ORDER:
// a refill can only file a timer while the lease still reads Bounded(), so releasing
// the lease FIRST is what makes the timer clear final. bounds.timers has no eviction,
// so a timer filed after the clear outlives the container.
func TestForgetRunBounds_ARefillInsideTheTeardownLeavesNoTimer(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	bus := &refillingBus{rs: h, id: id}
	h.bus = bus
	leased(t, h, id)
	h.armDeadline(t.Context(), id)
	// Near-expiry, so a refill landing mid-teardown clears the throttle and genuinely
	// installs a timer rather than being refused for an unrelated reason.
	if err := h.leaseStore().SetDeadline(t.Context(), id, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("stage a near-expiry deadline: %v", err)
	}
	// One unanswered ask, or the teardown broadcasts nothing and this passes vacuously.
	if !h.asks.Add(askOf("c1", id, "a1", "review")) {
		t.Fatal("the ask was not recorded, so nothing in the teardown broadcasts")
	}

	h.forgetBounds(t.Context(), id)

	if bus.refills == 0 {
		t.Fatal("the teardown broadcast nothing, so no refill was driven inside it")
	}
	h.mu.Lock()
	_, mine := h.bounds.timers[id]
	timers := len(h.bounds.timers)
	h.mu.Unlock()
	if mine || timers != 0 {
		t.Errorf("the teardown left %d timers behind (this run's: %v); a refill filed one after "+
			"the clear, and bounds.timers has no eviction — so that entry outlives the container",
			timers, mine)
	}
	if _, held := h.lease(id); held {
		t.Error("the teardown left the lease behind, so a later frame could bound the run again")
	}
}

// TestClearRunEnd_RestoresARetriedRunToUnbounded: retry reuses the workflow id, so two
// things about the old run outlive it — the recorded reason (which history.ts lets
// outrank live status) and the termination claim, which no bound can take twice.
func TestClearRunEnd_RestoresARetriedRunToUnbounded(t *testing.T) {
	t.Parallel()
	h := &Runs{}
	const id = "wf_1"

	h.claimTermination(id)
	h.recordEnd(id, runEndOverran)
	h.recordEnd("wf_other", runEndStepCap)

	h.clearEnd(id)

	if got := h.endReason(id); got != "" {
		t.Errorf("the retried run still reads %q, so its row renders as aborted", got)
	}
	if !h.claimTermination(id) {
		t.Error("the retried run kept its termination claim, so no bound can ever stop it")
	}
	// The queue must lose the entry too, or eviction stops bounding the map.
	h.mu.Lock()
	order := slices.Clone(h.bounds.order)
	h.mu.Unlock()
	if slices.Contains(order, id) {
		t.Errorf("the eviction queue still names the cleared run: %v", order)
	}
	// A neighbour is untouched.
	if got := h.endReason("wf_other"); got != runEndStepCap {
		t.Errorf("clearing one run's reason changed another's to %q", got)
	}
}

// TestClearRunEnd_ClearsTheClaimOfAUserCancelledRun: a user cancel takes a claim and
// records NO reason, so keying the clear on a recorded reason leaves that run unbounded.
func TestClearRunEnd_ClearsTheClaimOfAUserCancelledRun(t *testing.T) {
	t.Parallel()
	h := &Runs{}
	const id = "wf_1"

	h.claimTermination(id) // the user's cancel
	h.clearEnd(id)         // the retry

	if !h.claimTermination(id) {
		t.Error("a user-cancelled run stayed claimed through its retry, so nothing bounds it")
	}
}

// TestRearmRetriedRun_GivesAFreshClock: a run aborted WITHOUT a terminal frame still
// carries its launch deadline, and the arm is idempotent on an already-bounded run, so
// without the disarm the retry runs under the remainder of the old clock.
func TestRearmRetriedRun_GivesAFreshClock(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	leased(t, h, id)

	h.armDeadline(t.Context(), id)
	before, _ := h.lease(id)

	h.rearmRetried(t.Context(), id, "publish")

	after, held := h.lease(id)
	if !held || !after.Bounded() {
		t.Fatal("the retried run holds no deadline at all")
	}
	if after.Deadline.Equal(before.Deadline) {
		t.Error("the retry kept the previous deadline, so its clock is the old one's remainder")
	}
}

// TestRearmRetriedRun_MintsALeaseForARunWhoseTerminalFrameReleasedIt: with the lease
// released there is nothing to re-arm, so one is minted carrying the recipe the CALLER
// read off KAS's run list. A nameless lease is invisible to the single-run rule.
func TestRearmRetriedRun_MintsALeaseForARunWhoseTerminalFrameReleasedIt(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"

	h.rearmRetried(t.Context(), id, "nightly")

	l, held := h.lease(id)
	if !held {
		t.Fatal("a retry after a terminal frame got no lease, so nothing bounds the re-driven run")
	}
	if !l.Bounded() {
		t.Error("the re-minted lease carries no deadline")
	}
	if l.Recipe != "nightly" {
		t.Errorf("recipe = %q, want nightly; a nameless lease cannot be recognised by the "+
			"single-run rule as the run holding its own recipe", l.Recipe)
	}
	if l.Origin != runlease.OriginManual {
		t.Errorf("origin = %q, want manual: a retried parentless run is the user's own and "+
			"must stay sweepable", l.Origin)
	}
	if !l.SlotAt.IsZero() {
		t.Errorf("SlotAt = %v; the run list reports a name, not a launch source, so no slot is "+
			"resolvable for a re-hosted run", l.SlotAt)
	}
	if l.Unattended {
		t.Error("a retried run was marked unattended; the user clicked Retry and can answer")
	}
}

// TestRunStartLaunch_ClassifiesByTheCarrier: a `run_start` up a CHAT's bridge is an
// agent-launched run, and that population is excluded from the orphan sweep's cancel
// arm. Inferring agent origin from lease ABSENCE instead is false for the run that
// matters — a retry grants its lease after the call returns.
func TestRunStartLaunch_ClassifiesByTheCarrier(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		chatID     vibekit.ChatID
		want       runlease.Origin
		wantChatID string
	}{
		"a run bridge's frame, dispatched with no chat id": {"", runlease.OriginManual, ""},
		"the synthetic run chat id":                        {runChatID("wf_1"), runlease.OriginManual, ""},
		"a real chat id, so the chat's agent asked":        {"c-abc123", runlease.OriginAgent, "c-abc123"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := runStartLaunch(tc.chatID)
			if got.origin != tc.want {
				t.Errorf("runStartLaunch(%q).origin = %q, want %q", tc.chatID, got.origin, tc.want)
			}
			if got.chatID != tc.wantChatID {
				t.Errorf("runStartLaunch(%q).chatID = %q, want %q", tc.chatID, got.chatID, tc.wantChatID)
			}
		})
	}
}

// TestObserveRunStart_AParentlessFrameMintsASweepableLease: an unsweepable parentless
// run is a permanent wedge, because its restart-paused row is never cleared and blocks
// every later launch of that recipe.
func TestObserveRunStart_AParentlessFrameMintsASweepableLease(t *testing.T) {
	h, _, _ := newTestHub()
	const id = "wf_retry"

	h.runs.observeStart(t.Context(), "", runNotif(methodWFRunStart, map[string]any{
		"workflowId": id, "workflowName": "nightly",
	}))

	l, held := h.runs.lease(id)
	if !held {
		t.Fatal("a parentless run_start minted no lease")
	}
	if l.Origin == runlease.OriginAgent {
		t.Error("a parentless run was leased as agent-origin, which excludes it from the orphan " +
			"sweep for good: a restart would leave its paused row blocking the recipe forever")
	}
	if l.Origin != runlease.OriginManual {
		t.Errorf("origin = %q, want manual", l.Origin)
	}
	if l.Recipe != "nightly" {
		t.Errorf("recipe = %q, want the frame's own workflowName", l.Recipe)
	}
	if !l.Bounded() {
		t.Error("the minted lease was not armed")
	}

	// The other carrier: a chat's own agent run stays agent.
	h.runs.observeStart(t.Context(), "c-abc", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_agent", "workflowName": "publish",
	}))
	if l, _ := h.runs.lease("wf_agent"); l.Origin != runlease.OriginAgent {
		t.Errorf("a chat-parented run was leased as %q, want agent", l.Origin)
	}
}

// TestRunEndReason_DistinguishesABoundFromAUserCancel: a cancel lands on `aborted`
// whoever asked for it, so the row can only tell a backstop from a person if the
// deciding side records it — and a user cancel records NOTHING.
func TestRunEndReason_DistinguishesABoundFromAUserCancel(t *testing.T) {
	t.Parallel()
	h := &Runs{}

	if got := h.endReason("wf_user_cancelled"); got != "" {
		t.Errorf("a run nothing recorded reported %q; a user cancel must read as empty", got)
	}
	h.recordEnd("wf_overran", runEndOverran)
	h.recordEnd("wf_step", runEndStepCap)

	if got := h.endReason("wf_overran"); got != runEndOverran {
		t.Errorf("endReason(overran) = %q, want %q", got, runEndOverran)
	}
	if got := h.endReason("wf_step"); got != runEndStepCap {
		t.Errorf("endReason(step cap) = %q, want %q", got, runEndStepCap)
	}
	// A shared map must not answer for a key it does not hold.
	if got := h.endReason("wf_user_cancelled"); got != "" {
		t.Errorf("the reason leaked to an unrecorded run: %q", got)
	}
}

// TestRecordRunEnd_IsBounded: the record outlives its run (the History row reads it
// after the run finished), so it cannot be cleared on the terminal frame and FIFO
// eviction is the only thing bounding the map.
func TestRecordRunEnd_IsBounded(t *testing.T) {
	t.Parallel()
	h := &Runs{}

	for i := range maxRunEndReasons + 10 {
		h.recordEnd("wf_"+strconv.Itoa(i), runEndOverran)
	}
	h.mu.Lock()
	got := len(h.bounds.reasons)
	order := len(h.bounds.order)
	h.mu.Unlock()

	// Exactly the cap, not merely at-most: the bound is what the map is allowed
	// to hold, so evicting one entry early quietly shrinks the history a
	// finished run's row reads.
	if got != maxRunEndReasons {
		t.Errorf("kept %d reasons, want exactly the cap %d", got, maxRunEndReasons)
	}
	if order != got {
		t.Errorf("the eviction queue (%d) and the map (%d) disagree", order, got)
	}
	// Oldest first: the reason for a run nobody is still looking at is the one to
	// lose.
	if h.endReason("wf_0") != "" {
		t.Error("the oldest reason survived eviction")
	}
	if h.endReason("wf_"+strconv.Itoa(maxRunEndReasons+9)) != runEndOverran {
		t.Error("the newest reason was evicted")
	}
}

// TestRecordRunEnd_RewriteDoesNotDoubleQueue guards the eviction bookkeeping: a
// second record for one run must not enqueue it twice, or the queue drifts from
// the map and eviction starts deleting keys that are already gone.
func TestRecordRunEnd_RewriteDoesNotDoubleQueue(t *testing.T) {
	t.Parallel()
	h := &Runs{}

	h.recordEnd("wf_1", runEndOverran)
	h.recordEnd("wf_1", runEndStepCap)

	h.mu.Lock()
	order := len(h.bounds.order)
	h.mu.Unlock()
	if order != 1 {
		t.Errorf("the eviction queue holds %d entries for one run, want 1", order)
	}
	if got := h.endReason("wf_1"); got != runEndStepCap {
		t.Errorf("endReason = %q, want the latest reason %q", got, runEndStepCap)
	}
	// An empty reason is not a reason: recording one would put a run in the queue
	// whose row then reads as unbounded anyway.
	h.recordEnd("wf_2", "")
	if got := h.endReason("wf_2"); got != "" {
		t.Errorf("an empty reason was recorded as %q", got)
	}
}

// TestStepTurnCapExceeded_CancelsOncePerRun pins the enforcement's arithmetic
// without a bridge behind it.
//
// The cancel itself needs a live utility session, so this asserts the two facts
// that decide whether it is issued at all and what the row says afterwards: the
// arm is taken exactly once, and the reason recorded is the step cap rather than
// the wall clock.
func TestStepTurnCapExceeded_CancelsOncePerRun(t *testing.T) {
	t.Parallel()
	h := &Runs{}

	// Unarmed: a run vibekit is not bounding is not one it may cancel, and a
	// breach reported for it records nothing.
	h.StepTurnCapExceeded("wf_unarmed", "node-1", 200)
	if got := h.endReason("wf_unarmed"); got != "" {
		t.Errorf("an unarmed run recorded %q; the arm is the authority to act", got)
	}
}

// TestStepTurnCapExceeded_DoesNotConsumeTheDeadlineItLoses: the step cap's gate
// reads the lease's deadline rather than clearing it, because a breach that loses
// the termination claim must leave the wall clock to whoever won. Clearing it would
// strip the bound from a run that is still executing.
func TestStepTurnCapExceeded_DoesNotConsumeTheDeadlineItLoses(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	leased(t, h, id)
	h.armDeadline(t.Context(), id)

	// Something else is already ending the run.
	if !h.claimTermination(id) {
		t.Fatal("the fixture could not take the claim it needs to hold")
	}
	h.StepTurnCapExceeded(id, "node-1", 200)

	if !h.bounded(id) {
		t.Error("a losing step-cap breach dropped the deadline of a run it did not terminate")
	}
	if got := h.endReason(id); got != "" {
		t.Errorf("a losing step-cap breach recorded %q over the winner's reason", got)
	}
}

// recordingRunTranslator is noopRunTranslator plus a log of which runs had their
// step sessions forgotten. The registry itself is package-private to
// `internal/translate`, so the gate is observed through the ROLE — which is the
// right level: observeComplete's job is routing and gating, not bookkeeping.
type recordingRunTranslator struct {
	noopRunTranslator
	forgotten []string
}

func (r *recordingRunTranslator) ForgetRunSteps(workflowID string) {
	r.forgotten = append(r.forgotten, workflowID)
}

// TestObserveComplete_ForgetsStepSessionsOnlyOnATerminalStatus is the root cause of the
// stale-parent-dot symptom. Wiping the step-session registry on EVERY `run_complete` empties
// it mid-run, because `paused` is the ordinary frame for a step parked on a question — KAS
// sends it seconds after the ask and the run resumes minutes later. The resumed run's next
// request-shaped ask then resolves no run id, `omitempty` keeps `run_id` off the wire, and the
// ask lands under the launching chat's id where no run-scoped surface can see it.
func TestObserveComplete_ForgetsStepSessionsOnlyOnATerminalStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status string
		forget bool
	}{
		{"completed", true},
		{"failed", true},
		{"aborted", true},
		{"cancelled", true},
		// The one that matters: the run is still this process's to resume, so its
		// step sessions have to survive or its next ask arrives unattributed.
		{"paused", false},
	} {
		t.Run(tc.status, func(t *testing.T) {
			t.Parallel()
			rec := &recordingRunTranslator{}
			rs := &Runs{translate: rec}

			rs.observeComplete(t.Context(), "c-1", runNotif(methodWFRunComplete, map[string]any{
				"workflowId": "wf_1", "status": tc.status,
			}))

			want := []string(nil)
			if tc.forget {
				want = []string{"wf_1"}
			}
			if !slices.Equal(rec.forgotten, want) {
				t.Errorf("forgotten = %v after status %q, want %v", rec.forgotten, tc.status, want)
			}
		})
	}
}

// TestObserveComplete_ClearsAStepsPendingDecisionOnlyWhenTheRunEnds is the server-side half
// of the same symptom, driven end to end through the real runtime. The client's own sweep only
// empties one page's dock queue, while the tracker is what the SSE connect replay reads — so
// without a server-side clear the next connect re-offers a step's ask for a run that has
// ended. `paused` must NOT clear: that run is still this process's to resume, and a step
// really is still waiting.
func TestObserveComplete_ClearsAStepsPendingDecisionOnlyWhenTheRunEnds(t *testing.T) {
	for _, tc := range []struct {
		status   string
		survives bool
	}{
		{"completed", false},
		{"paused", true},
	} {
		t.Run(tc.status, func(t *testing.T) {
			h, _, _ := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			const runID = "wf_1"
			const launching vibekit.ChatID = "c-parent"
			// A step's question, filed the way translate files one: keyed to the
			// LAUNCHING chat (that is where the answer will arrive from) with the run
			// stamped on the payload by the step-session registry.
			h.bus.pendingPerms.Add(7, vibekit.NewEvent(vibekit.EventUserInputNeeded, launching,
				vibekit.UserInputNeededPayload{RequestID: 7, RunID: runID, NodeID: "review"}))

			h.runs.observeComplete(t.Context(), launching, runNotif(methodWFRunComplete, map[string]any{
				"workflowId": runID, "status": tc.status,
			}))

			replayed := len(h.bus.pendingPerms.List("")) == 1
			if replayed != tc.survives {
				t.Errorf("the step's question replayable = %v after status %q, want %v",
					replayed, tc.status, tc.survives)
			}
		})
	}
}

// TestObserveRunComplete_DisarmsOnlyOnATerminalStatus pins the one distinction
// this wrapper exists to make.
//
// KAS reports an `onMaxIterations` policy PAUSE through run_complete too, and
// that run is still this process's to resume — so dropping the arm there would
// leave a resumed run unbounded until its next run_start happened to arrive.
func TestObserveRunComplete_DisarmsOnlyOnATerminalStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status   string
		terminal bool
	}{
		{"completed", true},
		{"failed", true},
		{"aborted", true},
		{"cancelled", true},
		{"paused", false},
		{"", false},
	} {
		t.Run(tc.status, func(t *testing.T) {
			t.Parallel()
			if got := terminalRunStatus(tc.status); got != tc.terminal {
				t.Errorf("terminalRunStatus(%q) = %v, want %v", tc.status, got, tc.terminal)
			}
		})
	}
}

// TestDecodeLifecycleFrame reads the two fields the bounds need off a frame, and
// pins the failure direction: an undecodable frame yields no workflow id, so it
// arms nothing rather than arming a run called "".
func TestDecodeLifecycleFrame(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		params     string
		wantID     string
		wantStatus string
	}{
		"a run_start frame":        {`{"workflowId":"wf_1","workflowName":"x"}`, "wf_1", ""},
		"a terminal frame":         {`{"workflowId":"wf_1","status":"completed"}`, "wf_1", "completed"},
		"a frame with no id":       {`{"status":"completed"}`, "", "completed"},
		"malformed params":         {`{"workflowId":`, "", ""},
		"params that are not JSON": {`not json`, "", ""},
		"an array":                 {`["wf_1"]`, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			msg := &vibekit.RPCResponse{Params: json.RawMessage(tc.params)}
			got := decodeLifecycleFrame(msg)
			if got.WorkflowID != tc.wantID {
				t.Errorf("WorkflowID = %q, want %q", got.WorkflowID, tc.wantID)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
		})
	}
	// A nil message and an empty body are the shapes a wrapper meets before any
	// decode: both must be "no run", never a run named "".
	if got := workflowIDOfFrame(nil); got != "" {
		t.Errorf("workflowIDOfFrame(nil) = %q, want empty", got)
	}
	if got := workflowIDOfFrame(&vibekit.RPCResponse{}); got != "" {
		t.Errorf("workflowIDOfFrame(empty) = %q, want empty", got)
	}
}

// TestRunBoundConstants_HoldTheirRelationships pins the three numbers against the
// relationships that make them mean what their names say, rather than against the
// numbers themselves. Each is derived from something outside this file, so an edit
// that breaks a relationship is the failure worth catching.
//
// A backstop the user can raise stops being a backstop, so there is no Settings
// key and no per-run override either; these stay constants.
func TestRunBoundConstants_HoldTheirRelationships(t *testing.T) {
	t.Parallel()

	// The floor is the smallest budget any run may be handed, so a window below
	// it would never be the answer NextDeadline returns and the stall bound would
	// silently become minRunBudget.
	if runIdleWindow <= minRunBudget {
		t.Errorf("runIdleWindow = %v, not above the floor %v; the floor would swallow it",
			runIdleWindow, minRunBudget)
	}
	// KAS's own StreamIdleTimeoutError fires at 300s and its stream_error_retry
	// re-issues the stream emitting NOTHING on the wire, so a window at or below
	// that would cancel runs KAS was in the middle of recovering. This is the
	// window's derivation, not a coincidence.
	if kasStreamIdle := 300 * time.Second; runIdleWindow <= kasStreamIdle {
		t.Errorf("runIdleWindow = %v, not longer than KAS's own stream idle timeout %v; a "+
			"stalled STREAM is KAS's to retry, and this window is about a stalled RUN",
			runIdleWindow, kasStreamIdle)
	}
	// The backstop is the absolute bound, so it has to be the loosest of the
	// three or it would be the bound that always fires.
	if runBackstop <= runIdleWindow {
		t.Errorf("runBackstop = %v, not above the idle window %v; it would fire first and no "+
			"run could ever stall", runBackstop, runIdleWindow)
	}
	// The throttle skips a refill that would move the deadline by less than this,
	// so a granularity at or above the window means no refill ever lands and the
	// watchdog degrades to a fixed 15-minute run ceiling.
	if refillGranularity >= runIdleWindow {
		t.Errorf("refillGranularity = %v, not below the idle window %v; no refill could ever "+
			"clear the throttle", refillGranularity, runIdleWindow)
	}
}

// TestObserveComplete_ClosesTheStepDrivenTurnOnlyOnATerminalStatus is the third thing riding
// the terminal gate, and the only one whose subject is the LAUNCHING CHAT rather than the run.
// A chat-parented run's step frames fold onto that chat and open a turn there, and the bracket
// path cannot close it because the attribution gate drops a step's own turn_end — so the run
// reaching terminal is its only closer. `paused` must not do it: the next step folds into the
// same turn.
func TestObserveComplete_ClosesTheStepDrivenTurnOnlyOnATerminalStatus(t *testing.T) {
	for _, tc := range []struct {
		status    string
		stillOpen bool
	}{
		{"completed", false},
		{"failed", false},
		// The one that matters: closing here would take the turn away from a run
		// that is about to carry on folding into it.
		{"paused", true},
	} {
		t.Run(tc.status, func(t *testing.T) {
			h, cs, _ := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			const launching vibekit.ChatID = "c-parent"
			stagedStepTurn(t, h, cs, launching, "the step's reply")

			h.runs.observeComplete(t.Context(), launching, runNotif(methodWFRunComplete, map[string]any{
				"workflowId": "wf_1", "status": tc.status,
			}))

			if open := h.liveTurnBuffer(launching) != nil; open != tc.stillOpen {
				t.Errorf("the step turn is still open = %v after status %q, want %v",
					open, tc.status, tc.stillOpen)
			}
		})
	}
}

// TestObserveComplete_LeavesAParentlessRunsChatIDAlone pins the guard keeping a PARENTLESS
// run's terminal frame out of the turn lifecycle. The empty chat id is reachable on every such
// run, and a run that folds onto no chat has no turn to close. The fixture MANUFACTURES the
// turn to make the guard observable, because nothing in the run dispatch path folds a step
// frame under that id; what is pinned is that the terminal frame does not reach the lifecycle
// at all, whatever happens to be keyed there.
func TestObserveComplete_LeavesAParentlessRunsChatIDAlone(t *testing.T) {
	for _, chatID := range []vibekit.ChatID{"", "run:wf_1"} {
		t.Run(string(chatID), func(t *testing.T) {
			h, cs, _ := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			stagedStepTurn(t, h, cs, chatID, "content keyed under a parentless run's id")

			h.runs.observeComplete(t.Context(), chatID, runNotif(methodWFRunComplete, map[string]any{
				"workflowId": "wf_1", "status": "completed",
			}))

			if h.liveTurnBuffer(chatID) == nil {
				t.Errorf("a parentless run's terminal frame closed a turn keyed under %q", chatID)
			}
		})
	}
}

// TestCancel_ReleasesTheLeaseOfAPausedRunThatSendsNoTerminalFrame drives the defect through
// the PUBLIC verb. Everything is real except KAS: the cancel lands (`{}`), no `run_complete`
// is ever delivered — what a node-boundary cancel does to a run with no in-flight node — and
// the inspect afterwards reports `aborted`, which without releaseIfOver left the lease on disk
// forever. It also pins the ordering: the cancel RPC goes out BEFORE the reconcile, because
// the owning process must live to the node boundary to certify the cancelled state.
func TestCancel_ReleasesTheLeaseOfAPausedRunThatSendsNoTerminalFrame(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "aborted", ""),
	}
	leased(t, h.runs, "wf_1")

	if err := h.runs.Cancel(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	if _, held := h.runs.lease("wf_1"); held {
		t.Error("a landed cancel of a paused run left its lease behind, so the run stays " +
			"advertised as live and its chat stays exempt from eviction")
	}
	log := br.callLog()
	cancelAt := slices.Index(log, methodKiroWorkflowCancel)
	inspectAt := slices.Index(log, methodKiroWorkflowInspect)
	if cancelAt < 0 {
		t.Fatalf("no cancel went out: %v", log)
	}
	if inspectAt >= 0 && inspectAt < cancelAt {
		t.Errorf("the reconcile ran BEFORE the cancel: %v", log)
	}
}

// TestCancel_LeavesTheLeaseOfARunThatIsStillRunning: the reconcile is a NO-OP for
// a running run, because a cancel on one DOES reach a node boundary and KAS's own
// terminal frame releases the lease through forgetBounds. Releasing here instead
// would unbound a run that is still executing.
func TestCancel_LeavesTheLeaseOfARunThatIsStillRunning(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowCancel: json.RawMessage(`{}`),
		// The boundary has not been reached yet, which is what a client sees between
		// the cancel and the frame.
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "running", ""),
	}
	leased(t, h.runs, "wf_1")

	if err := h.runs.Cancel(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	if _, held := h.runs.lease("wf_1"); !held {
		t.Error("the reconcile released the lease of a run KAS still reports as running")
	}
}

// TestCancel_ReconcilesNothingWhenTheCancelFAILED: a refused cancel means the run
// did NOT stop, so its lease is still describing something live and no inspect is
// worth issuing. The claim goes back instead, which is what keeps a later Cancel
// from being silently refused.
func TestCancel_ReconcilesNothingWhenTheCancelFAILED(t *testing.T) {
	h, _, br := newTestHub()
	br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("bridge gone")}
	leased(t, h.runs, "wf_1")

	if err := h.runs.Cancel(t.Context(), "wf_1"); err == nil {
		t.Fatal("Cancel reported success for a cancel that failed")
	}

	if _, held := h.runs.lease("wf_1"); !held {
		t.Error("a FAILED cancel released the lease, stranding a live run with no clock " +
			"and nothing to explain the row it blocks")
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowInspect) {
		t.Errorf("a failed cancel still paid for a reconcile: %v", br.callLog())
	}
}
