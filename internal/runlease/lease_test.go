package runlease

import (
	"testing"
	"time"
)

// TestNextDeadline_TakesTheTighterBoundAndNeverGoesBelowTheFloor is the whole
// arithmetic of "one clock, two inputs".
//
// Before the lease these were two independent mechanisms: armRunDeadline (the
// slot, armed only by the scheduler's launch) and the run ceiling (armed by
// everything). So a manual run of a scheduled recipe held that recipe for the
// whole ceiling and refused every slot underneath it, and a slot that fired late
// bounded its run by whatever remained of an interval that had nearly elapsed.
func TestNextDeadline_TakesTheTighterBoundAndNeverGoesBelowTheFloor(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)
	const ceiling = time.Hour
	const floor = 5 * time.Minute

	for name, tc := range map[string]struct {
		slotAt time.Time
		want   time.Time
	}{
		"no slot, so the ceiling is the whole bound": {
			slotAt: time.Time{},
			want:   now.Add(ceiling),
		},
		"a slot inside the ceiling wins": {
			slotAt: now.Add(10 * time.Minute),
			want:   now.Add(10 * time.Minute),
		},
		"a slot beyond the ceiling loses, so a daily schedule is still bounded": {
			slotAt: now.Add(24 * time.Hour),
			want:   now.Add(ceiling),
		},
		"a slot exactly at the ceiling changes nothing": {
			slotAt: now.Add(ceiling),
			want:   now.Add(ceiling),
		},
		"a slot already passed is floored, not honoured": {
			slotAt: now.Add(-time.Minute),
			want:   now.Add(floor),
		},
		"a slot inside the floor is floored: a bound too small to finish in is no bound": {
			slotAt: now.Add(90 * time.Second),
			want:   now.Add(floor),
		},
		"a slot exactly at the floor is honoured": {
			slotAt: now.Add(floor),
			want:   now.Add(floor),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := NextDeadline(now, ceiling, floor, tc.slotAt)
			if !got.Equal(tc.want) {
				t.Errorf("NextDeadline = %v, want %v (in %v / %v from now)",
					got, tc.want, got.Sub(now), tc.want.Sub(now))
			}
			if got.Before(now.Add(floor)) {
				t.Errorf("NextDeadline = %v, below the floor %v", got.Sub(now), floor)
			}
		})
	}
}

// TestNextDeadline_FloorOutranksTheSlot states the ordering as its own property,
// because it is the one place the two inputs genuinely disagree and the answer is
// not "the tighter one".
//
// A schedule whose interval was edited below the run floor, or a slot that fired
// at the very end of its window, would otherwise hand every run a budget it
// cannot finish inside — which writes "failed" into the row on every slot while
// nothing is actually wrong.
func TestNextDeadline_FloorOutranksTheSlot(t *testing.T) {
	t.Parallel()
	now := time.Now()
	got := NextDeadline(now, time.Hour, 5*time.Minute, now.Add(time.Second))
	if want := now.Add(5 * time.Minute); !got.Equal(want) {
		t.Errorf("NextDeadline = %v after now, want the floor %v", got.Sub(now), want.Sub(now))
	}
}

// TestLeaseBounded_IsTheSuccessorOfTheArmMap pins the meaning of a zero
// deadline, which three readers depend on: the timer callback's own liveness
// test, the step cap's authority to act, and the re-arm.
func TestLeaseBounded_IsTheSuccessorOfTheArmMap(t *testing.T) {
	t.Parallel()
	now := time.Now()

	parked := Lease{WorkflowID: "wf_1", Origin: OriginManual}
	if parked.Bounded() {
		t.Error("a lease with no deadline reported bounded; a parked run must not be cancellable")
	}
	if parked.Expired(now.Add(time.Hour)) {
		t.Error("a parked lease reported expired; a run parked for a week must not be cancelled for it")
	}

	live := Lease{WorkflowID: "wf_1", Origin: OriginManual, Deadline: now.Add(time.Minute)}
	if !live.Bounded() {
		t.Error("a lease with a deadline reported unbounded")
	}
	if live.Expired(now) {
		t.Error("a lease expired before its deadline")
	}
	if !live.Expired(now.Add(time.Minute)) {
		t.Error("a lease was not expired AT its deadline; the timer fires then")
	}
}

// TestOriginValid pins the closed set. An unknown origin cannot be reasoned
// about — it says neither whether the run is sweepable nor whether it is
// unattended — so it is refused on the way in and dropped on the way back off
// disk.
func TestOriginValid(t *testing.T) {
	t.Parallel()
	for _, o := range []Origin{OriginScheduled, OriginManual, OriginAgent} {
		if !o.Valid() {
			t.Errorf("%q rejected", o)
		}
	}
	for _, o := range []Origin{"", "tui", "Scheduled", "manual "} {
		if o.Valid() {
			t.Errorf("%q accepted", o)
		}
	}
}
