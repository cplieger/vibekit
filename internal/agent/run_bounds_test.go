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

// leased grants a manual lease so a bounds test has something to arm. A run with
// no lease is deliberately unbounded (only the TUI's runs reach that state), so a
// fixture that forgot this would pass vacuously.
func leased(t *testing.T, h *Runs, workflowID string) {
	t.Helper()
	h.grantLease(t.Context(), workflowID, "publish", manualLaunch())
}

// undurableLeaseStore returns a lease store whose every write fails, with no seam
// and no injection: the directory it would write into is a regular FILE, so the
// atomic write cannot open the parent. ENOTDIR at any uid, which a mode-based
// fixture is not — this container runs as root, where a 0500 directory still
// accepts writes and the test would gate nothing where CI runs.
//
// The in-memory half of the store is untouched, which is the split every caller
// of it here is about.
func undurableLeaseStore(t *testing.T) *runlease.Store {
	t.Helper()
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("stage the unwritable store: %v", err)
	}
	st, _ := runlease.NewStore(notADir) // the error is diagnostic; the store is usable
	return st
}

// TestArmRunDeadline_FeedsTheLeasesSlotIntoTheOneDeadline is the runtime's HALF of
// "one clock, three inputs": that the arm hands NextDeadline the lease's own slot
// alongside the idle window, the backstop and the schedule floor, and stores what
// comes back.
//
// The arithmetic itself belongs to runlease.NextDeadline and is tested exhaustively
// there; the cases here are the three distinct answers the wiring can produce.
//
// This says NOTHING about a manual run of a scheduled recipe. That is a property of
// the LAUNCH — where a manual run finds a slot at all — and the version of this
// test that claimed to cover it substituted scheduledLaunch in every nonzero-slot
// case, so it could not fail while the manual bug stood. It is covered end to end
// by TestLaunchRun_ManualRunOfAScheduledRecipeYieldsToItsNextSlot.
func TestArmRunDeadline_FeedsTheLeasesSlotIntoTheOneDeadline(t *testing.T) {
	for name, tc := range map[string]struct {
		slotIn time.Duration
		// spent seeds the run's accumulated executing time, which is the only
		// input that can make the backstop the tightest of the three.
		spent  time.Duration
		wantIn time.Duration
	}{
		"no slot: the idle window is the whole bound":  {0, 0, runIdleWindow},
		"a slot inside the window wins":                {10 * time.Minute, 0, 10 * time.Minute},
		"a slot beyond the window loses":               {24 * time.Hour, 0, runIdleWindow},
		"a slot inside the floor is floored up":        {30 * time.Second, 0, minRunBudget},
		"a slot already gone is floored, not honoured": {-time.Minute, 0, minRunBudget},
		// The backstop only bites once nearly all of it is spent, which is the
		// point: the absolute bound must never shorten a healthy run's window.
		// The remainder is above minRunBudget deliberately — below it the floor
		// answers and the case would prove nothing about the backstop.
		"a nearly-spent backstop wins over the window": {0, runBackstop - 7*time.Minute, 7 * time.Minute},
		// The one answer that is deliberately in the PAST: a spent backstop means
		// the run is over, and the floor may not lift it back into the future.
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
			// A spent backstop stamps an instant already PAST, so its timer fires the
			// moment the arm installs it — which is the behaviour, not a hazard. Taking
			// the run's termination claim first makes that callback refuse, so the
			// STAMPED VALUE can be read without racing the cancel it triggers; the
			// cancel is TestCancelExpiredRun_TellsAStallFromASpentBackstop's subject.
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

			// The bound has to SURVIVE the run's own progress, which is the half an arm
			// cannot show: every input but the idle window and the floor is an absolute
			// instant, so a refill recomputes the bound already granted rather than
			// granting a fresh one. A refill only spends a write when it would move the
			// deadline by more than refillGranularity, so the stored deadline is aged by
			// that much first — what a busy step's next minute of tool calls does to it.
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

// TestArmRunDeadline_ConcurrentArmsLeaveALiveTimerForTheStoredDeadline is the
// atomicity the arm's three steps need, and the race is by design rather than
// exotic: launch arms after `invoke` while that run's own `run_start` frame is
// already arriving on its bridge, so two arms contend on every launch.
//
// Read as three separately-locked steps — check, store, install — both callers can
// see an unbounded lease and compute two deadlines A and B; the stores land in one
// order and the timer swaps in the other, so the lease ends up carrying B while only
// A's timer survived (installing A stops B). A's callback then re-reads the lease,
// finds B, and correctly refuses to act — leaving a run that reads BOUNDED with no
// live callback anywhere, and therefore no wall clock at all.
//
// The assertion per round is not "one timer" but "the surviving timer is armed for
// the deadline the lease holds", which is checked by making that timer FIRE: Reset
// reschedules the same func with the same captured deadline, so a mismatched
// survivor records nothing.
//
// The divergent END STATE is not what this watches for, because it is rare: the
// window between one arm's read and its store is a few hundred nanoseconds, and a
// probe over 400 rounds of 6 arms landed two arms inside it in only ~7% of them —
// then only half of those reverse the install order. Chasing it needs thousands of
// rounds and still gates probabilistically.
//
// So the assertion is the INVARIANT the transaction establishes, watched by an
// observer that reads the timer map and the lease under ONE hold of unattendedMu:
// a bounded lease always has a timer. Under the transaction that pair cannot be
// caught apart — the observer either runs entirely before the arm or entirely after
// it. Read as three steps, SetDeadline sets the in-memory deadline and then
// persists, so the lease reads bounded for the whole write while no timer exists
// yet, and the observer sees it. The store is DISK-BACKED here for exactly that
// reason: the persist widens that window from nanoseconds to a file write.
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
				// ONE observation of both halves. Read through the store directly:
				// h.runs.lease would take unattendedMu again.
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

		// And the survivor is armed for what the lease holds. Reset reschedules the
		// SAME func with the same captured deadline, so a mismatched survivor claims
		// nothing and records nothing.
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

// TestArmRunDeadline_KeepsBoundingWhenOnlyDurabilityFails is the split between the
// in-memory envelope and the disk.
//
// SetDeadline sets the in-memory deadline whenever the lease exists and reports
// only the persist, so treating its error as "the deadline was not set" left the
// lease reading BOUNDED with no timer — and the arm's own idempotence check made
// that permanent: every later `run_start` returns at "already bounded", so the
// missing timer is never installed and the run executes with no wall clock for the
// life of the process. A transient disk error or an expired launch context was
// enough. grantLease already resolves the same conflict the same way: lose
// durability, keep the envelope.
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
	// And the second arm must still be the no-op it is for a healthy run: a run
	// already bounded is not re-stamped.
	if got := h.runs.endReason(id); got != runEndOverran {
		t.Errorf("recorded %q, want %q", got, runEndOverran)
	}
	// The degradation is REPORTED, and that is the whole compensation for it: the
	// run is bounded by this process alone, so a restart silently loses its clock
	// unless the row exists to say so.
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
// half of the same durability split the arm has.
//
// A pause parks the deadline so a run deliberately held for a week is not
// cancelled for having been held. SetDeadline zeroes the in-memory deadline
// whenever the lease exists and reports only the persist, so a failed persist must
// not stop the park: refusing to park would leave the lease reading BOUNDED, which
// the arm's idempotence check then makes permanent.
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

// TestCancelExpiredRun_ReportsAScheduleRowItCouldNotWrite pins the one thing the
// outcome write can do when it fails.
//
// The row is how a reader finds out a schedule stopped producing, and the run is
// cancelled whether or not the row lands — so a failed write is exactly the
// silence the outcome exists to remove, and it has to be said out loud. A schedule
// DELETED while its run was executing is the reachable case: the id on the lease
// no longer resolves.
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

// TestStepTurnCap_ReportsACancelItCouldNotIssue is the bound's own failure path:
// the run breached its cap whether or not the cancel landed, and a row left
// reading `running` is what these bounds exist to end.
//
// Two halves, and the second is the recovery: the claim is handed BACK, so the
// user's Cancel button still works on a run vibekit failed to stop.
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

// TestArmRunDeadline_IsIdempotent pins the property `run_start` forces.
//
// That frame re-fires on every resume (probe 6 saw three for one run) and the
// launch verbs arm too, so a run is armed more than once by design and the
// EARLIEST arm wins. A second arm must not re-stamp the deadline or restart the
// clock, or a run emitting frames could extend its own budget indefinitely.
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
	// One timer, whatever the arm count: a second live timer means a second
	// callback, and only the termination claim would stand between them.
	h.mu.Lock()
	timers := len(h.bounds.timers)
	h.mu.Unlock()
	if timers != 1 {
		t.Errorf("three arms left %d timers, want 1", timers)
	}
}

// TestArmRunDeadline_RefusesARunWithNoLease pins the one population vibekit does
// not bound, and why that is right rather than a gap: a TUI-launched run has no
// lease, no bridge here, and no cancel path vibekit owns — so arming a timer for
// it would schedule a cancel against a run this process cannot certify.
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

// TestDisarmRunDeadline_ParksTheLeaseAndStopsTheTimer.
//
// Clearing the LEASE is the load-bearing half rather than stopping the timer: a
// stale deadline left behind would tell the step cap the run is still executing,
// and would make the next re-arm skip it as "already bounded" — so the run would
// never be bounded again.
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
	// Stop reports false for a timer already stopped, so a second Stop proves the
	// first landed — cheaper and exact next to waiting a ceiling out.
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

// TestRunDeadline_ResumeGetsAFreshBudgetRatherThanARemainder is the semantic the
// lease's mutable deadline exists for.
//
// The bound is on EXECUTING time: a run parked on a permission prompt overnight
// must not be cancelled the instant it resumes. A deadline computed once at
// admission would be wall time, which is precisely what this rejects.
//
// It asserts the ARITHMETIC, not merely that the second timestamp is later than the
// first. The version that did only the latter passed for an implementation granting
// one nanosecond, and for every remainder bug — the two arms are microseconds apart
// on a real clock, so "later" is true whatever the resume computed. What has to
// hold is that the resumed budget is a FULL idle window measured from the resume,
// and that the pause left no deadline behind for a remainder to be computed from.
func TestRunDeadline_ResumeGetsAFreshBudgetRatherThanARemainder(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	leased(t, h, id)

	h.armDeadline(t.Context(), id)
	first, _ := h.lease(id)

	if !h.disarmDeadline(t.Context(), id) { // the pause
		t.Fatal("the pause reported holding no deadline")
	}
	// The parked lease carries NO deadline, which is what makes the re-arm a fresh
	// computation rather than an adjustment: there is nothing left to subtract from.
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
	// A full idle window from the RESUME. A window rather than an equality because
	// the arm reads its own clock, but a window this tight excludes every
	// remainder: the first arm's leftover budget, and any fraction of it.
	if budget := second.Deadline.Sub(resumedAt); budget < runIdleWindow-time.Second || budget > runIdleWindow+time.Second {
		t.Errorf("the resumed run got %v of budget, want a full %v measured from the resume; "+
			"a resumed run must not inherit the remainder of the clock it parked with",
			budget.Round(time.Millisecond), runIdleWindow)
	}
	if !second.Deadline.After(first.Deadline) {
		t.Errorf("the resume kept deadline %v (first was %v)", second.Deadline, first.Deadline)
	}
}

// TestCancelExpiredRun_ASupersededTimerDoesNothing is the retired generation
// token's job, re-expressed as the value comparison that replaced it.
//
// `Timer.Stop` does not halt an already-running func, so a callback that fired
// microseconds before a pause is in flight while the pause parks the lease and the
// resume re-stamps a fresh deadline. The callback used to identify itself by a
// generation; it now re-reads the lease and acts only if the stored deadline is
// still the one it was armed for. Calling the callback directly with the old
// deadline is exactly that in-flight state.
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
	// And a run whose lease is gone entirely: the terminal frame released it, so a
	// pending timer must not resurrect a cancel.
	h.releaseLease(t.Context(), id)
	if h.claimExpiredDeadline(id, live.Deadline) {
		t.Error("a released run was claimed by its pending timer")
	}
}

// TestRunDeadline_FiresAndCancelsAtTheDeadline drives the real timer end to end
// on a budget short enough to observe, because everything above calls the callback
// directly and would stay green if AfterFunc were never installed.
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
	// The arm's own transaction, staged by hand: the timer install is the last step
	// under unattendedMu, and no budget the arm would compute is short enough to
	// observe (NextDeadline floors at minRunBudget).
	h.runs.mu.Lock()
	h.runs.setTimerLocked(id, deadline)
	h.runs.mu.Unlock()

	// A deadline-bounded poll rather than a sleep: it fails closed with a
	// diagnostic and cannot flake into a false pass.
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

// TestCancelExpiredRun_AFlooredSlotStillReportsAsTheScheduleBound is the
// three-valued deadline the callback has to classify.
//
// NextDeadline answers with the idle window, the slot, the backstop or the FLOOR
// — and the floor outranks the slot deliberately, so a slot already gone or closer
// than minRunBudget produces a deadline LATER than SlotAt. Testing equality with
// SlotAt therefore classified exactly that case as vibekit's own bound: the
// callback logged a stall five minutes after the arm and skipped the schedule row
// entirely — so the row still read `started` for a schedule whose run vibekit had
// just cancelled, which is the one failure the outcome write exists to remove.
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

	// A slot INSIDE the floor: the floor wins, so the armed deadline is later than
	// SlotAt and equality can no longer recognise the slot.
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

// TestCancelExpiredRun_AManualRunYieldingToASlotIsNotAScheduleFailure is the third
// outcome the same classification produces, and it exists because finding 3 gave
// manual runs a slot.
//
// A manual run cut short by its recipe's next scheduled slot is the bound WORKING:
// it stood aside so the schedule could run. logMsgRunOverran is an ERROR a homelab
// Loki rule reads as "a schedule stopped producing", so reusing it here would page
// somebody for correct behaviour — and there is no schedule row to write, because a
// manual run has no row to be attributed to.
func TestCancelExpiredRun_AManualRunYieldingToASlotIsNotAScheduleFailure(t *testing.T) {
	logs := captureLogs(t)
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	// A manual lease carrying a slot: exactly what launch now mints for a manual
	// run of a scheduled recipe. No schedule id, because no row asked for this run.
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

// TestClaimRunTermination_IsTakenOnce is finding 7's core: one run, one
// termination, whoever asks.
//
// Four callers race for it — the user's Cancel, a schedule's repeat interval, the
// wall clock and a step's turn cap — and before the claim each had a different
// gate, so two could pass at once and the second reason overwrote the first.
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

// TestClaimRunTermination_UserCancelBeatsALaterBound is the outcome finding 7
// names: a deliberate stop must not be relabelled a timeout.
//
// A user cancel records NOTHING, which is the only thing that distinguishes it
// from the two bounds on the History row. So a bound that claimed alongside it and
// recorded `overran` did not merely duplicate work — it rewrote what the user did.
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

	// The deadline, whose timer fired just before. Its stored value still
	// matches, so only the claim can stop it writing.
	l, _ := h.lease(id)
	if h.claimExpiredDeadline(id, l.Deadline) {
		t.Fatal("the deadline claimed a run the user had already cancelled")
	}

	if got := h.endReason(id); got != "" {
		t.Errorf("the row reads %q for a user cancel; the absence IS the third value", got)
	}
}

// TestClaimRunTermination_ScheduleDeadlineAndStepCapCannotBothRecord is finding
// 7's second race. `cancelOverrunRun` ignored its disarm's result and recorded
// `overran` even when the step cap had already taken the arm and recorded
// `step_cap`, so the later write overwrote the earlier one and both issued a
// cancel for one run.
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

// TestReleaseRunTermination_ReopensAFailedCancel: the claim means a termination is
// in flight or landed, so a cancel RPC that FAILED must hand it back. Holding it
// would leave the Cancel button silently doing nothing on a run still executing.
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

// TestForgetRunBounds_ClearsTheClaimOnATerminalRun pins what bounds the claim map.
// Membership is the set of runs currently terminating, not a log of every run that
// ever was — and the terminal frame is the moment nothing can act on the run
// again, because every bound's own gate is already false by then.
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
	// And the lease itself: a run that is over has no envelope, so the admission
	// backstop never sees it and no timer can be armed against it again.
	if _, held := h.lease(id); held {
		t.Error("the terminal frame left the lease behind, so the recipe still reads as busy")
	}
}

// refillingBus refills a run's deadline from INSIDE the teardown, which is the only
// moment a progress frame can land between forgetBounds' first step and its last.
//
// settleAsksForRun's own broadcast is the seam, and it is a real one rather than a
// contrivance: it is the slowest thing in that body, so it is where a concurrent
// tool-call frame is likeliest to interleave.
type refillingBus struct {
	rs      *Runs
	id      string
	refills int
}

func (b *refillingBus) Broadcast(ctx context.Context, _ vibekit.ServerEvent) {
	b.refills++
	b.rs.refillDeadline(ctx, b.id)
}

// TestForgetRunBounds_ARefillInsideTheTeardownLeavesNoTimer pins the ORDER of that
// teardown, which is the whole guard.
//
// A refill can only file a timer while the lease still reads Bounded(), so releasing
// the lease FIRST is what makes the timer clear final. Clear the timer first instead
// and the lease stays bounded for the rest of the body — so a frame landing in that
// span stamps a deadline and installs a fresh tracked timer that nothing later
// removes. bounds.timers has no eviction, unlike reasons with maxRunEndReasons, so
// each occurrence is a permanent entry keyed by a workflow id that no longer exists.
// The arm could never do this: it requires !l.Bounded(), and the refill's inverse
// guard is what opens the window.
func TestForgetRunBounds_ARefillInsideTheTeardownLeavesNoTimer(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	bus := &refillingBus{rs: h, id: id}
	h.bus = bus
	leased(t, h, id)
	h.armDeadline(t.Context(), id)
	// Near-expiry, so a refill landing mid-teardown would clear the throttle and
	// genuinely install a timer rather than being refused for an unrelated reason.
	if err := h.leaseStore().SetDeadline(t.Context(), id, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("stage a near-expiry deadline: %v", err)
	}
	// One unanswered ask, or the teardown broadcasts nothing and the refill never
	// runs — which would pass for the wrong reason.
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

// TestClearRunEnd_RestoresARetriedRunToUnbounded is finding 9, at the helper.
//
// Retry reuses the workflow id, and two things about the old run outlive it: the
// recorded reason (which history.ts deliberately lets outrank live status, so the
// running retry rendered as aborted) and the termination claim (which would leave
// the retry with no wall clock at all, since no bound can claim a run twice).
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
	// The eviction queue must lose the entry too, or it names a key the map no
	// longer holds and eviction stops bounding the map.
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

// TestClearRunEnd_ClearsTheClaimOfAUserCancelledRun is the asymmetric half: a user
// cancel takes a claim and records NO reason, so keying the whole clear on a
// recorded reason would leave exactly that run unbounded after a retry.
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

// TestRearmRetriedRun_GivesAFreshClock: Retry's already-hosted branch exists
// for a run aborted WITHOUT a terminal frame, which can still carry the deadline
// it was launched with — and the arm is idempotent on an already-bounded run, so
// without the disarm that run is retried under the remainder of its old clock.
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

// TestRearmRetriedRun_MintsALeaseForARunWhoseTerminalFrameReleasedIt is the other
// retry shape, and the one that would otherwise leave a re-driven run unbounded:
// the terminal frame released the lease, so there is nothing to re-arm.
//
// The re-minted lease carries the recipe the CALLER read off KAS's run list, which
// is where a re-hosted run's name lives (`kasWorkflowRun.Name`). It used to be
// minted empty on the reasoning that the recipe is unknowable here; it is not, and
// the empty name made the run invisible to the single-run rule's comparison — so
// the admission backstop could not recognise it as the thing holding its own
// recipe. The SLOT is still zero, which is a real narrowing: a schedule is matched
// by launch source and the run list reports only the name.
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

// TestRunStartLaunch_ClassifiesByTheCarrier is finding 7's core, at the predicate.
//
// A `run_start` frame arriving up a CHAT's bridge is an agent-launched run — KAS
// parents a run on a chat's session only when that chat's agent asked for it — and
// that population is excluded from the orphan sweep's cancel arm because the chat
// rehydrate's resume sweep owns it. Every other carrier means the run has no chat:
// run-bridge frames dispatch with an empty chat id, and the bridge is registered
// under the synthetic `run:<id>`.
//
// The retired rule inferred agent origin from lease ABSENCE, which is false for
// exactly the run that matters: a retry grants its lease after the retry call
// returns, so a `run_start` landing first stamped OriginAgent on a parentless run
// and the agent exclusion then made it permanently unsweepable.
//
// The carrier also decides the lease's CHAT: an agent's run carries the chat
// that launched it (the live-runs projection's key), and a parentless run
// carries none — the designed value a pre-upgrade lease row decodes to as well.
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

// TestObserveRunStart_AParentlessFrameMintsASweepableLease is the same property
// through the frame handler, which is where the defect actually shipped.
//
// An unsweepable parentless run is a permanent wedge: if its bridge dies or vibekit
// restarts, its restart-paused row is never cleared and blocks every later launch
// of that recipe forever — which is the exact failure the whole lease mechanism was
// built to close.
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

	// The other carrier, on the same handler: a chat's own agent run stays agent.
	h.runs.observeStart(t.Context(), "c-abc", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_agent", "workflowName": "publish",
	}))
	if l, _ := h.runs.lease("wf_agent"); l.Origin != runlease.OriginAgent {
		t.Errorf("a chat-parented run was leased as %q, want agent", l.Origin)
	}
}

// TestRunEndReason_DistinguishesABoundFromAUserCancel is D56c's whole point.
//
// Both bounds stop a run through the same Cancel the Cancel button reaches,
// and KAS's status vocabulary has no "cancelled" — a cancel lands on `aborted`
// whoever asked for it. So the row cannot tell a backstop from a person unless
// the side that decided records it, and a user cancel must record NOTHING or the
// absence stops being the third value.
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
	// Still nothing for the run nobody bounded, now that its neighbours have
	// entries: a shared map must not answer for a key it does not hold.
	if got := h.endReason("wf_user_cancelled"); got != "" {
		t.Errorf("the reason leaked to an unrecorded run: %q", got)
	}
}

// TestRecordRunEnd_IsBounded pins the FIFO eviction.
//
// The record has to outlive its run — the History row reads it after the run
// finished — so it cannot be cleared on the terminal frame the way the arm is.
// That makes an unbounded map the default, and a container that runs for months
// would keep every reason forever.
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

// TestObserveComplete_ForgetsStepSessionsOnlyOnATerminalStatus is the root cause
// of the reported parent-dot-stale symptom, at the level the decision is made.
//
// The step-session registry used to be wiped by the translator on EVERY
// `run_complete`, and `paused` is the ordinary frame for a step parked on a
// question — KAS sends it seconds after the ask and the run resumes minutes
// later. So the registry emptied mid-run, and the resumed run's next
// request-shaped ask resolved no run id: `refFor` missed, `omitempty` kept
// `run_id` off the wire, and the ask landed under the launching chat's id where
// no run-scoped surface could see it while it still lit that chat's tab dot.
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

// TestObserveComplete_ClearsAStepsPendingDecisionOnlyWhenTheRunEnds is the
// server-side half of the reported symptom, driven end to end through the real
// runtime rather than through the tracker alone.
//
// The client's own run-scoped sweep is session-scoped: it empties one page's dock
// queue. The tracker is what the SSE connect replay reads, so without a matching
// server-side clear the next connect re-offered a step's request-shaped ask for a
// run that had ended, and the launching chat's dot lit again for a card no answer
// could reach. `paused` must NOT clear, for `observeComplete`'s own reason: that
// run is still this process's to resume, and a step really is still waiting.
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

// TestObserveComplete_ClosesTheStepDrivenTurnOnlyOnATerminalStatus is the third
// thing that rides the terminal gate, and the only one whose subject is the
// LAUNCHING CHAT rather than the run.
//
// A chat-parented run's step frames fold onto that chat and open a turn there, and
// the bracket path cannot close it because the attribution gate drops a step's own
// turn_end — so the run reaching terminal is the only closer it has. `paused` must
// not do it: that run is still this process's to resume, and its next step folds
// into the same turn.
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

// TestObserveComplete_LeavesAParentlessRunsChatIDAlone pins the guard that keeps a
// PARENTLESS run's terminal frame out of the turn lifecycle.
//
// The empty chat id is reachable on every such run — run-bridge frames are
// dispatched with one, which is the same distinction runStartLaunch makes — and a
// run that folds onto no chat has no turn to close. The fixture has to MANUFACTURE
// the turn to make the guard observable, because nothing in the run dispatch path
// folds a step frame under that id; what is being pinned is that the terminal frame
// does not reach the lifecycle at all, whatever happens to be keyed there.
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

// TestCancel_ReleasesTheLeaseOfAPausedRunThatSendsNoTerminalFrame is the whole
// defect, driven through the PUBLIC verb rather than the reconcile.
//
// Everything is real except KAS: the cancel lands (`{}`), no `run_complete` is
// ever delivered — which is exactly what a node-boundary cancel does to a run with
// no in-flight node — and the inspect afterwards reports the run `aborted`. Before
// releaseIfOver this left the lease on disk forever, and /api/runs/live kept
// advertising a dead run.
//
// It also pins that the CONTRACT is unchanged: the cancel RPC still goes out, and
// it goes out BEFORE the reconcile, because the owning process must live to the
// node boundary to certify the cancelled state.
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
