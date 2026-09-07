package runlease

import (
	"testing"
	"time"
)

// The whole arithmetic of one clock over three inputs. Two mechanisms would let a manual
// run of a scheduled recipe hold that recipe for the whole ceiling and refuse every slot
// under it, and a late slot bound its run by whatever remained of the interval.
func TestNextDeadline_TakesTheTighterBoundAndNeverGoesBelowTheFloor(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)
	const idle = 15 * time.Minute
	const floor = 5 * time.Minute

	for name, tc := range map[string]struct {
		in   Bounds
		want time.Time
	}{
		"no slot and no backstop, so the idle window is the whole bound": {
			in:   Bounds{Idle: idle, Floor: floor},
			want: now.Add(idle),
		},
		"a slot inside the idle window wins": {
			in:   Bounds{SlotAt: now.Add(10 * time.Minute), Idle: idle, Floor: floor},
			want: now.Add(10 * time.Minute),
		},
		"a slot beyond the window loses, so a daily schedule is still bounded": {
			in:   Bounds{SlotAt: now.Add(24 * time.Hour), Idle: idle, Floor: floor},
			want: now.Add(idle),
		},
		"a slot exactly at the window's end changes nothing": {
			in:   Bounds{SlotAt: now.Add(idle), Idle: idle, Floor: floor},
			want: now.Add(idle),
		},
		"a slot already passed is floored, not honoured": {
			in:   Bounds{SlotAt: now.Add(-time.Minute), Idle: idle, Floor: floor},
			want: now.Add(floor),
		},
		"a slot inside the floor is floored: a bound too small to finish in is no bound": {
			in:   Bounds{SlotAt: now.Add(90 * time.Second), Idle: idle, Floor: floor},
			want: now.Add(floor),
		},
		"a slot exactly at the floor is honoured": {
			in:   Bounds{SlotAt: now.Add(floor), Idle: idle, Floor: floor},
			want: now.Add(floor),
		},
		"a backstop inside the idle window wins": {
			in:   Bounds{BackstopAt: now.Add(7 * time.Minute), Idle: idle, Floor: floor},
			want: now.Add(7 * time.Minute),
		},
		"a backstop beyond the window loses, so an ordinary refill is unaffected": {
			in:   Bounds{BackstopAt: now.Add(100 * idle), Idle: idle, Floor: floor},
			want: now.Add(idle),
		},
		// Deliberately in the PAST, which is what makes the backstop absolute: floored
		// instead, a run refilling on its own progress rolls the bound forward forever.
		"a spent backstop is honoured, not floored": {
			in:   Bounds{BackstopAt: now.Add(-time.Hour), Idle: idle, Floor: floor},
			want: now.Add(-time.Hour),
		},
		"a spent backstop outranks a slot inside the floor too": {
			in: Bounds{
				SlotAt: now.Add(90 * time.Second), BackstopAt: now.Add(-time.Hour),
				Idle: idle, Floor: floor,
			},
			want: now.Add(-time.Hour),
		},
		"a zero backstop does not bound the run": {
			in:   Bounds{Idle: idle, Floor: floor},
			want: now.Add(idle),
		},
		"slot and backstop both set: the tighter one wins": {
			in: Bounds{
				SlotAt: now.Add(12 * time.Minute), BackstopAt: now.Add(9 * time.Minute),
				Idle: idle, Floor: floor,
			},
			want: now.Add(9 * time.Minute),
		},
		"slot and backstop both set: the slot can be the tighter one": {
			in: Bounds{
				SlotAt: now.Add(9 * time.Minute), BackstopAt: now.Add(12 * time.Minute),
				Idle: idle, Floor: floor,
			},
			want: now.Add(9 * time.Minute),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := NextDeadline(now, tc.in)
			if !got.Equal(tc.want) {
				t.Errorf("NextDeadline = %v, want %v (in %v / %v from now)",
					got, tc.want, got.Sub(now), tc.want.Sub(now))
			}
			// The floor holds for every case a SPENT backstop does not answer,
			// which is the one input allowed below it.
			spent := !tc.in.BackstopAt.IsZero() && tc.in.BackstopAt.Before(now.Add(floor))
			if !spent && got.Before(now.Add(floor)) {
				t.Errorf("NextDeadline = %v, below the floor %v", got.Sub(now), floor)
			}
			if spent && !got.Equal(tc.in.BackstopAt) {
				t.Errorf("NextDeadline = %v with a spent backstop at %v; the backstop instant is "+
					"the answer, or a floor rolls the absolute bound forward on every stamp",
					got.Sub(now), tc.in.BackstopAt.Sub(now))
			}
		})
	}
}

// The one place the inputs genuinely disagree, and the two halves disagree in OPPOSITE
// directions, so no single composition step serves both. The floor answers how much budget
// a run should GET — without it an interval edited below it, or a slot fired at the end of
// its window, writes "failed" into the row while nothing is wrong. The backstop answers how
// much a run has LEFT, so the floor cannot reach it: any remainder tighter than the floor
// wins, and a spent one is an instant in the past the timer fires on at once.
func TestNextDeadline_FloorOutranksTheSlotButNotTheBackstop(t *testing.T) {
	t.Parallel()
	now := time.Now()
	const floor = 5 * time.Minute

	slot := NextDeadline(now, Bounds{SlotAt: now.Add(time.Second), Idle: 15 * time.Minute, Floor: floor})
	if want := now.Add(floor); !slot.Equal(want) {
		t.Errorf("with a one-second slot NextDeadline = %v after now, want the floor %v",
			slot.Sub(now), want.Sub(now))
	}

	backstop := now.Add(-time.Hour)
	spent := NextDeadline(now, Bounds{BackstopAt: backstop, Idle: 15 * time.Minute, Floor: floor})
	if !spent.Equal(backstop) {
		t.Errorf("with a spent backstop NextDeadline = %v after now, want the backstop instant "+
			"%v; a floor here is a fresh budget on every stamp, so the absolute bound becomes a "+
			"rolling window a productive-looking runaway never reaches",
			spent.Sub(now), backstop.Sub(now))
	}

	// A REFILL a moment later computes the same instant, which is what makes it terminal:
	// the anchor is fixed for the whole stretch, so the armed timer stands.
	later := NextDeadline(now.Add(2*time.Minute), Bounds{BackstopAt: backstop, Idle: 15 * time.Minute, Floor: floor})
	if !later.Equal(spent) {
		t.Errorf("a stamp two minutes later moved the spent backstop from %v to %v", spent, later)
	}
}

// Three readers depend on the meaning of a zero deadline: the timer callback's liveness
// test, the step cap's authority to act, and the re-arm.
func TestLeaseBounded_IsTheSuccessorOfTheArmMap(t *testing.T) {
	t.Parallel()
	now := time.Now()

	parked := Lease{WorkflowID: "wf_1", Origin: OriginManual}
	if parked.Bounded() {
		t.Error("a lease with no deadline reported bounded; a parked run must not be cancellable")
	}
	if parked.expired(now.Add(time.Hour)) {
		t.Error("a parked lease reported expired; a run parked for a week must not be cancelled for it")
	}

	live := Lease{WorkflowID: "wf_1", Origin: OriginManual, Deadline: now.Add(time.Minute)}
	if !live.Bounded() {
		t.Error("a lease with a deadline reported unbounded")
	}
	if live.expired(now) {
		t.Error("a lease expired before its deadline")
	}
	if !live.expired(now.Add(time.Minute)) {
		t.Error("a lease was not expired AT its deadline; the timer fires then")
	}
}

// An unknown origin says neither whether the run is sweepable nor whether it is
// unattended, so it is refused on the way in and dropped on the way back off disk.
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
