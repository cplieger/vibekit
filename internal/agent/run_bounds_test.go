package agent

import (
	"encoding/json"
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
	"github.com/cplieger/vibekit/internal/vibekit"
)

// leased grants a manual lease so a bounds test has something to arm. A run with
// no lease is deliberately unbounded (only the TUI's runs reach that state), so a
// fixture that forgot this would pass vacuously.
func leased(t *testing.T, h *Runs, workflowID string) {
	t.Helper()
	h.grantLease(t.Context(), workflowID, "publish", manualLaunch())
}

// TestArmRunDeadline_FeedsTheLeasesSlotIntoTheOneDeadline is the runtime's HALF of
// "one clock, two inputs": that the arm hands NextDeadline the lease's own slot
// alongside the universal ceiling and the schedule floor, and stores what comes
// back.
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
		wantIn time.Duration
	}{
		"no slot: the ceiling is the whole bound":      {0, runCeiling},
		"a slot inside the ceiling wins":               {10 * time.Minute, 10 * time.Minute},
		"a slot beyond the ceiling loses":              {24 * time.Hour, runCeiling},
		"a slot inside the floor is floored up":        {30 * time.Second, minRunBudget},
		"a slot already gone is floored, not honoured": {-time.Minute, minRunBudget},
	} {
		t.Run(name, func(t *testing.T) {
			h := &Runs{}
			const id = "wf_1"
			o := manualLaunch()
			if tc.slotIn != 0 {
				o = scheduledLaunch("sched-1", time.Now().Add(tc.slotIn))
			}
			h.grantLease(t.Context(), id, "publish", o)

			before := time.Now()
			h.armDeadline(t.Context(), id)
			l, ok := h.lease(id)
			if !ok || !l.Bounded() {
				t.Fatal("the run took no deadline, so nothing bounds it")
			}

			got := l.Deadline.Sub(before)
			// A window rather than an equality: the arm reads its own clock.
			if got < tc.wantIn-time.Second || got > tc.wantIn+time.Second {
				t.Errorf("deadline is %v out, want ~%v", got.Round(time.Second), tc.wantIn)
			}
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
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	// A store whose every write fails, with no seam and no injection: its directory
	// is a regular FILE, so the atomic write cannot even open the parent. The
	// in-memory half is untouched, which is exactly the split under test.
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("stage the unwritable store: %v", err)
	}
	st, _ := runlease.NewStore(notADir) // the error is diagnostic; the store is usable
	h.runs.leases = st

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
// hold is that the resumed budget is a FULL ceiling measured from the resume, and
// that the pause left no deadline behind for a remainder to be computed from.
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
	// A full ceiling from the RESUME. A window rather than an equality because the
	// arm reads its own clock, but a window this tight excludes every remainder: the
	// first arm's leftover budget, and any fraction of it.
	if budget := second.Deadline.Sub(resumedAt); budget < runCeiling-time.Second || budget > runCeiling+time.Second {
		t.Errorf("the resumed run got %v of budget, want a full %v measured from the resume; "+
			"a resumed run must not inherit the remainder of the clock it parked with",
			budget.Round(time.Millisecond), runCeiling)
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
// NextDeadline answers with the ceiling, the slot, or the FLOOR — and the floor
// outranks the slot deliberately, so a slot already gone or closer than
// minRunBudget produces a deadline LATER than SlotAt. Testing equality with SlotAt
// therefore classified exactly that case as a ceiling breach: the callback logged
// `run exceeded its wall-clock ceiling` with `ceiling=1h` five minutes after the
// arm, and skipped the schedule row entirely — so the row still read `started` for
// a schedule whose run vibekit had just cancelled, which is the one failure the
// outcome write exists to remove.
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
	if out := logs.String(); strings.Contains(out, logMsgRunCeiling) {
		t.Errorf("the callback reported a ceiling breach for a run cancelled at its schedule "+
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
	if strings.Contains(out, logMsgRunCeiling) {
		t.Errorf("the ceiling message was logged for a run cancelled at its slot: %s", out)
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

// TestRunStartOrigin_ClassifiesByTheCarrier is finding 7's core, at the predicate.
//
// A `run_start` frame arriving up a CHAT's bridge is an agent-launched run — KAS
// parents a run on a chat's session only when that chat's agent asked for it — and
// that population is excluded from the orphan sweep because the chat rehydrate's
// resume sweep owns it. Every other carrier means the run has no chat: run-bridge
// frames dispatch with an empty chat id, and the bridge is registered under the
// synthetic `run:<id>`.
//
// The retired rule inferred agent origin from lease ABSENCE, which is false for
// exactly the run that matters: a retry grants its lease after the retry call
// returns, so a `run_start` landing first stamped OriginAgent on a parentless run
// and the agent exclusion then made it permanently unsweepable.
func TestRunStartOrigin_ClassifiesByTheCarrier(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		chatID vibekit.ChatID
		want   runlease.Origin
	}{
		"a run bridge's frame, dispatched with no chat id": {"", runlease.OriginManual},
		"the synthetic run chat id":                        {runChatID("wf_1"), runlease.OriginManual},
		"a real chat id, so the chat's agent asked":        {"c-abc123", runlease.OriginAgent},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := runStartOrigin(tc.chatID); got != tc.want {
				t.Errorf("runStartOrigin(%q) = %q, want %q", tc.chatID, got, tc.want)
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

// TestRunCeiling_IsAConstantWithinCrewsClamp pins the number against the source
// it was taken from, and against the rule that it stays a constant.
//
// D57: a backstop the user can raise stops being a backstop, so there is no
// Settings key and no per-run override. Crew's configured value is clamped to
// [60s, 6h]; vibekit's is fixed, and this asserts it sits inside the range that
// clamp defines rather than merely being non-zero.
func TestRunCeiling_IsAConstantWithinCrewsClamp(t *testing.T) {
	t.Parallel()
	const crewMin, crewMax = 60 * time.Second, 6 * time.Hour
	if runCeiling < crewMin || runCeiling > crewMax {
		t.Errorf("runCeiling = %v, outside Crew's [%v, %v]", runCeiling, crewMin, crewMax)
	}
	if runCeiling != time.Hour {
		t.Errorf("runCeiling = %v, want Crew's 3600s default", runCeiling)
	}
}
