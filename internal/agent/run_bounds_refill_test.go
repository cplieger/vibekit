package agent

// The activity-based watchdog: the refill that rolls a run's idle window forward,
// the backstop that stops a productive-looking runaway, and the classification
// that tells those two expiries apart.
//
// Every fixture here stages the deadline and the executing stretch BY HAND rather
// than waiting for one, for the reason TestRunDeadline_FiresAndCancelsAtTheDeadline
// already states: no budget the arm computes is short enough to observe, since
// NextDeadline floors at minRunBudget. Staging the stretch's start is what lets a
// case place a run past its whole backstop of work without spending that long.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// stagedStretch gives a run a lease, a deadline, and an OPEN executing stretch
// beginning at stretchStart — the three pieces of state a refill reads.
//
// It does not go through armDeadline deliberately: the arm reads its own clock and
// opens the stretch at that instant, so a case needing a run already deep into its
// work could not express one.
func stagedStretch(t *testing.T, rs *Runs, workflowID string, deadline, stretchStart time.Time) {
	t.Helper()
	leased(t, rs, workflowID)
	if err := rs.leaseStore().SetDeadline(t.Context(), workflowID, deadline); err != nil {
		t.Fatalf("stage the deadline: %v", err)
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.bounds.armedAt == nil {
		rs.bounds.armedAt = map[string]time.Time{}
	}
	rs.bounds.armedAt[workflowID] = stretchStart
}

// TestRefillDeadline_RollsTheWindowForwardSoALongRunSurvives is the whole point of
// the change: a run stays alive by WORKING, not by being young.
//
// The stretch is staged 90 minutes deep, which is what makes this the retired
// ceiling's own case: under a one-hour bound on executing time this run was
// cancelled half an hour ago, mid-work, with nothing wrong. The assertion is
// therefore two-part — the refilled deadline is a full idle window from the
// progress, AND it lands past where the retired ceiling would have fired.
func TestRefillDeadline_RollsTheWindowForwardSoALongRunSurvives(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	stretchStart := time.Now().Add(-90 * time.Minute)
	// Two minutes from expiring: the state a run is in just before a stall would
	// be called.
	stagedStretch(t, h, id, time.Now().Add(2*time.Minute), stretchStart)

	progressAt := time.Now()
	h.refillDeadline(t.Context(), id)

	l, ok := h.lease(id)
	if !ok || !l.Bounded() {
		t.Fatal("the refill left the run unbounded")
	}
	if budget := l.Deadline.Sub(progressAt); budget < runIdleWindow-time.Second || budget > runIdleWindow+time.Second {
		t.Errorf("the refilled deadline is %v out, want a full idle window %v measured from the "+
			"progress", budget.Round(time.Millisecond), runIdleWindow)
	}
	// The retired bound, stated as the property rather than as a number: a run
	// this deep into its work must not be bounded by when it STARTED.
	if retired := stretchStart.Add(time.Hour); !l.Deadline.After(retired) {
		t.Errorf("the refilled deadline %v is not past %v, one hour into the run's own executing "+
			"stretch — a run still making progress must not be cancelled for being long",
			l.Deadline, retired)
	}
}

// TestRefillDeadline_RefusesAPausedRun is armDeadline's guard inverted, and the
// reason the two are separate doors rather than one operation with a flag.
//
// disarmDeadline parks the deadline precisely so a run held paused is never
// cancelled for having been held. A refill that re-armed one would resurrect the
// bound the pause deliberately removed — and worse, it would do so from a frame
// the run emitted BEFORE it parked, so the run would be cancelled while waiting on
// a person.
func TestRefillDeadline_RefusesAPausedRun(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	leased(t, h, id)
	h.armDeadline(t.Context(), id)
	if !h.disarmDeadline(t.Context(), id) { // the pause
		t.Fatal("the pause reported holding no deadline")
	}

	h.refillDeadline(t.Context(), id)

	if l, _ := h.lease(id); l.Bounded() {
		t.Errorf("a refill re-armed a PARKED run with deadline %v, so a run held on a person's "+
			"answer would be cancelled for having been held", l.Deadline)
	}
	h.mu.Lock()
	timers := len(h.bounds.timers)
	h.mu.Unlock()
	if timers != 0 {
		t.Errorf("a refill left %d timers on a parked run", timers)
	}
}

// TestRefillDeadline_RefusesARunWithNoLease is the arm's own refusal, for the same
// population: a TUI-launched run has no lease, no bridge here and no cancel path
// vibekit owns, so a frame from one must not install a timer against it.
func TestRefillDeadline_RefusesARunWithNoLease(t *testing.T) {
	h := &Runs{}
	h.refillDeadline(t.Context(), "wf_tui")
	if h.bounded("wf_tui") {
		t.Error("a refill bounded a run with no lease")
	}
	h.mu.Lock()
	timers := len(h.bounds.timers)
	h.mu.Unlock()
	if timers != 0 {
		t.Errorf("a leaseless run left %d timers behind", timers)
	}
	// And the empty id, which is what a frame carrying no workflow block decodes to.
	h.refillDeadline(t.Context(), "")
}

// TestRefillDeadline_ThrottlesAWriteItWouldBarelyMove is the disk cost the
// granularity exists for, asserted as the two things a naive throttle gets wrong.
//
// Each landed refill is a whole-file fsynced rewrite of runs.json, and a busy step
// emits a tool call every few seconds — so a refill that moves the deadline by
// milliseconds must not spend one. The SAME-TIMER assertion is the second half and
// the sharper one: a skip that still swapped the timer would stop the live callback
// and install a replacement on every tool call, which is a run whose enforcement
// depends on the last frame it happened to emit.
func TestRefillDeadline_ThrottlesAWriteItWouldBarelyMove(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	leased(t, h, id)
	h.armDeadline(t.Context(), id)

	armed, _ := h.lease(id)
	h.mu.Lock()
	first := h.bounds.timers[id]
	h.mu.Unlock()
	if first == nil {
		t.Fatal("the arm installed no timer, so there is nothing for a refill to preserve")
	}

	// Two refills immediately after the arm: each would move the deadline by the
	// microseconds since it, which is far below the granularity.
	h.refillDeadline(t.Context(), id)
	h.refillDeadline(t.Context(), id)

	after, _ := h.lease(id)
	if !after.Deadline.Equal(armed.Deadline) {
		t.Errorf("a throttled refill moved the deadline from %v to %v, so every tool call costs "+
			"a whole-file fsynced rewrite of runs.json", armed.Deadline, after.Deadline)
	}
	h.mu.Lock()
	second := h.bounds.timers[id]
	h.mu.Unlock()
	if second != first {
		t.Error("a throttled refill swapped the timer, so the arm's own callback was stopped " +
			"and replaced for a deadline that did not move")
	}
}

// TestRefillDeadline_SwapsTheTimerForTheNewDeadline is the invariant a store-only
// refill would break, and the one that makes the whole watchdog inert if missed.
//
// The deadline travels INTO the callback, and claimExpiredDeadline compares it
// against what the lease says at fire time — that comparison is the entire liveness
// test, and there is no generation token. So a refill that wrote the store and left
// the old closure installed would arm a callback that ALWAYS refuses: the run reads
// bounded, a timer exists, and nothing can ever stop it.
//
// Driven by Reset rather than by waiting: Reset reschedules the SAME func with the
// same captured deadline, so a timer armed for the superseded instant records
// nothing and the poll fails closed with a diagnostic.
func TestRefillDeadline_SwapsTheTimerForTheNewDeadline(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	leased(t, h.runs, id)

	h.runs.armDeadline(t.Context(), id)
	armed, _ := h.runs.lease(id)
	h.runs.mu.Lock()
	stale := h.runs.bounds.timers[id]
	h.runs.mu.Unlock()

	// Push the stored deadline back to near-expiry so the refill clears the
	// throttle. The stretch stays open, so the backstop is untouched.
	if err := h.runs.leaseStore().SetDeadline(t.Context(), id, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("stage a near-expiry deadline: %v", err)
	}

	h.runs.refillDeadline(t.Context(), id)

	live, _ := h.runs.lease(id)
	if !live.Deadline.After(armed.Deadline.Add(-time.Second)) {
		t.Fatalf("the refill did not land: deadline %v", live.Deadline)
	}
	h.runs.mu.Lock()
	timer := h.runs.bounds.timers[id]
	h.runs.mu.Unlock()
	if timer == nil {
		t.Fatal("the refill left no timer, so nothing can stop the run")
	}
	if timer == stale {
		t.Fatal("the refill wrote the store and kept the old timer, whose captured deadline the " +
			"lease no longer holds — that callback can only ever refuse")
	}
	// Stop reports false for a timer already stopped, so this proves the replaced
	// one was stopped rather than left to fire. Without it a run leaks one live
	// AfterFunc per tool call.
	if stale.Stop() {
		t.Error("the replaced timer was left live, so a refilling run accumulates one pending " +
			"callback per progress frame")
	}

	timer.Reset(time.Millisecond)
	stop := time.Now().Add(5 * time.Second)
	for h.runs.endReason(id) == "" {
		if time.Now().After(stop) {
			t.Fatalf("the installed timer was armed for a deadline the lease no longer holds "+
				"(lease says %v), so the refilled run is bounded by a record nothing enforces",
				live.Deadline)
		}
		time.Sleep(time.Millisecond)
	}
	if got := h.runs.endReason(id); got != runEndOverran {
		t.Errorf("the fired timer recorded %q, want %q", got, runEndOverran)
	}
}

// TestRefillDeadline_CannotMoveTheDeadlinePastTheBackstop is the bound the idle
// window cannot supply: a repeat node re-driving the same step forever emits a
// completed node on every pass, so it refills the window indefinitely and would
// hold its recipe for the life of the container.
//
// Two parts, and the second is the one that makes the backstop terminal. The first
// refill CLAMPS at the backstop rather than granting a full window; every later
// refill computes that same fixed instant, fails the throttle, and leaves the timer
// armed for it. So "keeps refreshing forever" is not a state the refill can produce.
// The stretch is staged HALF AN HOUR DEEP, and that depth is load-bearing rather
// than decoration: BackstopAt has to be anchored on the stretch's start, so a
// version anchoring it on `now` is only distinguishable once the two differ by more
// than the refill granularity. With a stretch that began an instant ago, the
// throttle hides that drift and the test passes for the bug.
func TestRefillDeadline_CannotMoveTheDeadlinePastTheBackstop(t *testing.T) {
	h := &Runs{}
	const id = "wf_1"
	// 37 minutes of backstop left against a stretch 30 minutes old, so the
	// backstop comes due 7 minutes from now — inside the idle window, and above
	// minRunBudget so the floor is not the answer either.
	const deep, left = 30 * time.Minute, 37 * time.Minute
	stretchStart := time.Now().Add(-deep)
	stagedStretch(t, h, id, time.Now().Add(2*time.Minute), stretchStart)
	h.mu.Lock()
	h.bounds.executed = map[string]time.Duration{id: runBackstop - left}
	h.mu.Unlock()

	h.refillDeadline(t.Context(), id)
	clamped, _ := h.lease(id)
	if budget := clamped.Deadline.Sub(stretchStart); budget > left+time.Second {
		t.Fatalf("the refill granted %v against %v of backstop left; a run that keeps making "+
			"progress must not outlive its absolute budget", budget.Round(time.Second), left)
	}
	if budget := clamped.Deadline.Sub(stretchStart); budget < left-time.Second {
		t.Fatalf("the refill granted only %v of the %v of backstop left",
			budget.Round(time.Second), left)
	}

	// Progress arriving over and over: every refill recomputes the same instant,
	// because BackstopAt is anchored on the stretch rather than on now.
	for range 5 {
		h.refillDeadline(t.Context(), id)
		after, _ := h.lease(id)
		if !after.Deadline.Equal(clamped.Deadline) {
			t.Fatalf("a later refill moved the clamped deadline from %v to %v, so a runaway loop "+
				"could refresh its way past the backstop forever", clamped.Deadline, after.Deadline)
		}
	}
}

// TestRunBackstop_MeasuresExecutingTimeNotWallTime is the anchor decision, and the
// test that fails for the obvious alternative.
//
// The backstop is anchored on an in-memory accumulator of executing STRETCHES, not
// on Lease.StartedAt. StartedAt is wall time and survives a restart, so
// `StartedAt + runBackstop` would cancel a run parked on a person's answer for
// longer than the backstop five minutes after they gave it. This fixture is that exact shape read
// from both sides: the lease STARTED longer ago than the backstop and has executed
// that long too, so a wall-clock anchor and an executing-time anchor agree it is
// spent — then the second half separates them, with a lease started that long ago
// that has barely executed at all.
func TestRunBackstop_MeasuresExecutingTimeNotWallTime(t *testing.T) {
	t.Run("stretches accumulate across pauses", func(t *testing.T) {
		h := &Runs{}
		const id = "wf_1"
		leased(t, h, id)

		// Two UNEQUAL stretches, each closed by a pause, together exceeding the
		// backstop. The pause is what banks them, and the delete inside it is what
		// stops a double charge; unequal so a sum that ignored one of them cannot
		// land on the same total.
		//
		// DERIVED from runBackstop, never hardcoded: this fixture's whole subject is
		// that the next arm finds the backstop SPENT, so a literal chosen against
		// today's value silently becomes a not-spent fixture asserting the opposite
		// the first time the constant is raised. It was 3h + 6h against an
		// eight-hour backstop and broke on the move to 36.
		firstStretch := runBackstop / 3
		secondStretch := runBackstop - firstStretch + time.Hour
		wantBanked := firstStretch + secondStretch
		for _, spent := range []time.Duration{firstStretch, secondStretch} {
			h.armDeadline(t.Context(), id)
			h.mu.Lock()
			h.bounds.armedAt[id] = time.Now().Add(-spent)
			h.mu.Unlock()
			if !h.disarmDeadline(t.Context(), id) {
				t.Fatalf("the pause after a %v stretch reported holding no deadline", spent)
			}
		}

		h.mu.Lock()
		banked := h.bounds.executed[id]
		open := len(h.bounds.armedAt)
		h.mu.Unlock()
		if banked < wantBanked || banked > wantBanked+time.Minute {
			t.Errorf("the run banked %v of executing time across two stretches, want ~%v",
				banked.Round(time.Second), wantBanked)
		}
		if open != 0 {
			t.Errorf("%d stretches left open after the park; a stretch banked twice would "+
				"double-charge the backstop", open)
		}

		// The observable consequence: the next arm finds the backstop spent, so the
		// deadline it hands out is already PAST and its timer fires at once. The claim
		// is taken first so that callback refuses, leaving the stamped value readable
		// without racing the cancel — TestCancelExpiredRun_TellsAStallFromASpentBackstop
		// is where the cancel itself is the subject.
		if !h.claimTermination(id) {
			t.Fatal("the parked run already held a termination claim")
		}
		before := time.Now()
		h.armDeadline(t.Context(), id)
		armed, _ := h.lease(id)
		if !armed.Deadline.Before(before) {
			t.Errorf("a run that has executed %v resumed with %v of budget, want a deadline "+
				"already past — its backstop is spent, so the run is over",
				banked.Round(time.Hour), armed.Deadline.Sub(before).Round(time.Second))
		}

		// And PROGRESS ARRIVING AFTERWARDS CANNOT LIFT IT, which the arm alone does
		// not show and the whole absolute bound rests on. Each lap ages the stored
		// deadline by a granularity so the refill is not merely throttled — what a real
		// minute of a busy step's tool calls does to it — and the refill must then
		// recompute the same spent instant. Answer with the floor instead and every lap
		// grants a fresh minimum, so the absolute bound degrades into a rolling
		// five-minute window that a run resuming spent never reaches, which is the
		// ordinary pause-and-resume path.
		for lap := range 3 {
			stored, _ := h.lease(id)
			aged := stored.Deadline.Add(-refillGranularity - time.Second)
			if err := h.leaseStore().SetDeadline(t.Context(), id, aged); err != nil {
				t.Fatalf("lap %d: age the stored deadline: %v", lap, err)
			}
			h.refillDeadline(t.Context(), id)
			after, _ := h.lease(id)
			if !after.Deadline.Equal(armed.Deadline) {
				t.Fatalf("lap %d: a refill moved the deadline of a run whose backstop is spent, "+
					"from %v to %v", lap, armed.Deadline, after.Deadline)
			}
		}
	})

	t.Run("a run ending clears its accounting", func(t *testing.T) {
		h := &Runs{}
		const id = "wf_1"
		leased(t, h, id)
		h.armDeadline(t.Context(), id)
		h.mu.Lock()
		h.bounds.executed = map[string]time.Duration{id: runBackstop}
		h.mu.Unlock()

		h.forgetBounds(t.Context(), id)

		h.mu.Lock()
		banked, open := h.bounds.executed[id], len(h.bounds.armedAt)
		h.mu.Unlock()
		if banked != 0 || open != 0 {
			t.Errorf("a terminal run left %v banked and %d stretches open; a workflow id KAS "+
				"reuses would inherit a spent backstop and be cancelled minutes after it started",
				banked, open)
		}

		// The observable consequence, since the accumulator is white-box state: the
		// reused id gets a full window rather than the floor.
		leased(t, h, id)
		before := time.Now()
		h.armDeadline(t.Context(), id)
		l, _ := h.lease(id)
		if budget := l.Deadline.Sub(before); budget < runIdleWindow-time.Second {
			t.Errorf("a reused workflow id got %v of budget, want a full idle window %v",
				budget.Round(time.Second), runIdleWindow)
		}
	})

	t.Run("wall time spent parked burns none of it", func(t *testing.T) {
		h := &Runs{}
		const id = "wf_1"
		// A lease granted longer ago than the whole backstop that has executed almost
		// nothing: the overnight run parked on a person. StartedAt is the only field a
		// wall-clock anchor could read, and it is staler than the entire budget.
		//
		// DERIVED, and for a sharper reason than the sibling above: a literal short of
		// the backstop makes this test pass for BOTH implementations, because a
		// StartedAt anchor would not read as spent either, so the fixture stops
		// discriminating instead of going red. It was -9h against an eight-hour
		// backstop, which discriminated only while the constant stayed under nine.
		staleBy := runBackstop + time.Hour
		l := runlease.Lease{
			StartedAt:  time.Now().Add(-staleBy),
			WorkflowID: id,
			Recipe:     "publish",
			Origin:     runlease.OriginManual,
		}
		if err := h.leaseStore().Put(t.Context(), &l); err != nil {
			t.Fatalf("stage the old lease: %v", err)
		}

		before := time.Now()
		h.armDeadline(t.Context(), id)

		armed, ok := h.lease(id)
		if !ok || !armed.Bounded() {
			t.Fatal("the resumed run took no deadline")
		}
		if budget := armed.Deadline.Sub(before); budget < runIdleWindow-time.Second {
			t.Errorf("a run parked for %v resumed with %v of budget, want a full idle "+
				"window %v; anchoring the backstop on StartedAt would cancel it minutes after "+
				"the person answered", staleBy, budget.Round(time.Second), runIdleWindow)
		}
	})
}

// TestCancelExpiredRun_TellsAStallFromASpentBackstop is why there are two messages
// rather than one.
//
// They describe opposite failures and an operator acts on them differently: a
// stalled run stopped producing and probably needs looking at, while a run that
// spent its whole budget of real work was working the whole time and needs its workflow
// shortened. Folding them into one line would make the interesting one unfindable,
// and logMsgRunStalled is the line worth a Loki rule.
func TestCancelExpiredRun_TellsAStallFromASpentBackstop(t *testing.T) {
	// The backstop case banks LESS than the whole budget and carries the rest in an
	// open stretch, because that is the only shape the backstop's own deadline can
	// fire in: it comes due mid-stretch by construction, so a classifier reading
	// only the banked total would report every backstop expiry as a stall.
	const openStretch = 10 * time.Minute
	for name, tc := range map[string]struct {
		banked     time.Duration
		stretchAge time.Duration
		want       string
		unwant     string
		wantKey    string
	}{
		"a run that stopped producing is a stall": {
			banked: 0, stretchAge: 0,
			want: logMsgRunStalled, unwant: logMsgRunBackstop, wantKey: "idle_window",
		},
		"a run that spent its whole budget is not": {
			banked: runBackstop - openStretch, stretchAge: openStretch,
			want: logMsgRunBackstop, unwant: logMsgRunStalled, wantKey: "backstop",
		},
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLogs(t)
			h, _, br := newTestHub()
			const id = "wf_1"
			br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
			h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
			leased(t, h.runs, id)
			h.runs.armDeadline(t.Context(), id)
			if tc.banked != 0 || tc.stretchAge != 0 {
				h.runs.mu.Lock()
				h.runs.bounds.executed = map[string]time.Duration{id: tc.banked}
				h.runs.bounds.armedAt[id] = time.Now().Add(-tc.stretchAge)
				h.runs.mu.Unlock()
			}
			l, _ := h.runs.lease(id)

			h.runs.cancelExpired(id, l.Deadline)

			out := logs.String()
			if !strings.Contains(out, tc.want) {
				t.Errorf("the expiry did not log %q: %s", tc.want, out)
			}
			if strings.Contains(out, tc.unwant) {
				t.Errorf("the expiry also logged %q, which describes the opposite failure: %s",
					tc.unwant, out)
			}
			// captureLogs renders JSON, so the attribute is a quoted key.
			if attr := strconv.Quote(tc.wantKey) + ":"; !strings.Contains(out, attr) {
				t.Errorf("the line carries no %s attribute, so it does not say which bound came "+
					"due or what its size was: %s", attr, out)
			}
			if got := h.runs.endReason(id); got != runEndOverran {
				t.Errorf("endReason = %q, want %q for both bounds", got, runEndOverran)
			}
		})
	}
}

// TestRefillDeadline_ConcurrentRefillsLeaveALiveTimerForTheStoredDeadline is
// TestArmRunDeadline_ConcurrentArmsLeaveALiveTimerForTheStoredDeadline's sibling,
// and the refill is why that invariant got MORE load-bearing rather than less.
//
// Two arms already contended on every launch. Now every tool call a run makes is a
// further concurrent stamper, so the number of contenders is the run's own frame
// rate. Read as three separately-locked steps, two stampers compute deadlines A and
// B, the stores land in one order and the timer swaps in the other, and the lease
// ends up carrying B while only A's timer survives — a run that reads BOUNDED with
// no live callback anywhere.
//
// Same observer as the arm's test: it reads the timer map and the lease under ONE
// hold, so a bounded lease with no timer cannot be caught apart under the
// transaction. The store is DISK-BACKED for the same reason too — the persist
// widens the window from nanoseconds to a file write.
func TestRefillDeadline_ConcurrentRefillsLeaveALiveTimerForTheStoredDeadline(t *testing.T) {
	const rounds, refills = 6, 4
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
		h.runs.armDeadline(t.Context(), id)
		// Near-expiry, so a refill clears the throttle and genuinely lands.
		if sErr := st.SetDeadline(t.Context(), id, time.Now().Add(time.Minute)); sErr != nil {
			t.Fatalf("round %d: stage a near-expiry deadline: %v", round, sErr)
		}

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
		for range refills {
			wg.Go(func() {
				<-release
				h.runs.refillDeadline(t.Context(), id)
			})
		}
		close(release)
		wg.Wait()
		close(halt)
		obs.Wait()

		if len(torn) > 0 {
			t.Fatalf("round %d: the lease was bounded for deadline %v while no timer existed, so "+
				"the eligibility check, the store and the timer install are not one transaction — "+
				"two refills can leave the lease carrying one deadline and the surviving timer "+
				"armed for another", round, <-torn)
		}

		l, ok := h.runs.lease(id)
		if !ok || !l.Bounded() {
			t.Fatalf("round %d: the refills left the run unbounded", round)
		}
		h.runs.mu.Lock()
		timer := h.runs.bounds.timers[id]
		timers := len(h.runs.bounds.timers)
		h.runs.mu.Unlock()
		if timer == nil {
			t.Fatalf("round %d: the refills left no timer, so nothing can stop the run", round)
		}
		if timers != 1 {
			t.Fatalf("round %d: %d refills left %d timers, want 1", round, refills, timers)
		}

		// And the survivor is armed for what the lease holds, which is the half a
		// count cannot see.
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
	}
}

// TestHealProgress_ACompletedNodeRefillsTheIdleWindow is the OTHER progress
// signal, and the only one both run populations share.
//
// The run bridge reuses chatHandlers for every `_kiro/workflow/*` method, so this
// one wrapper covers a chat-parented run and a parentless one alike — which is why
// the tool-call signal needed wiring at two sites and this needed one.
//
// A completed node is the strongest evidence there is, so it spends the same frame
// on all three of the run's budgets: the heal ladder, the cancel ladder, and now
// the idle window.
func TestHealProgress_ACompletedNodeRefillsTheIdleWindow(t *testing.T) {
	h, _, _ := newTestHub()
	const id = "wf_1"
	leased(t, h.runs, id)
	h.runs.armDeadline(t.Context(), id)
	// Near-expiry, so a landed refill is visible past the throttle.
	if err := h.runs.leaseStore().SetDeadline(t.Context(), id, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("stage a near-expiry deadline: %v", err)
	}

	var forwarded bool
	progress := h.runs.healProgress(func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) {
		forwarded = true
	})
	progress(t.Context(), "c1", pausedFrame(t, id, ""))

	l, _ := h.runs.lease(id)
	if budget := time.Until(l.Deadline); budget < runIdleWindow-time.Second {
		t.Errorf("a completed node left %v of budget, want a full idle window %v; a run "+
			"finishing nodes must not be cancelled as stalled", budget.Round(time.Second), runIdleWindow)
	}
	if !forwarded {
		t.Error("the wrapper swallowed the frame instead of passing it to the translator")
	}
}

// TestRunMadeProgress_IsTheDoorTranslateUses pins the exported surface the
// translator calls, because everything above it drives the unexported refill.
//
// It is called once per tool-call frame, so the no-op cases are the common ones and
// the reason for the `bounded` pre-check: a run vibekit is not bounding must cost a
// map read rather than a derived context and a store transaction.
func TestRunMadeProgress_IsTheDoorTranslateUses(t *testing.T) {
	h, _, _ := newTestHub()
	const id = "wf_1"
	leased(t, h.runs, id)
	h.runs.armDeadline(t.Context(), id)
	if err := h.runs.leaseStore().SetDeadline(t.Context(), id, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("stage a near-expiry deadline: %v", err)
	}

	h.runs.RunMadeProgress(id)

	l, _ := h.runs.lease(id)
	if budget := time.Until(l.Deadline); budget < runIdleWindow-time.Second {
		t.Errorf("the progress door left %v of budget, want a full idle window %v",
			budget.Round(time.Second), runIdleWindow)
	}

	// A run this process is not bounding: the TUI's own runs, and every frame that
	// arrives for a run whose lease is already gone.
	h.runs.RunMadeProgress("wf_never_leased")
	if h.runs.bounded("wf_never_leased") {
		t.Error("the progress door bounded a run with no lease")
	}
}
