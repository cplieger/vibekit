package schedule

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

// Runner.Run had no test at all: every case drives r.sweep directly, so the loop
// that decides WHEN a sweep happens — the once-a-minute cadence, and the
// deliberate absence of a sweep on entry — was carried by a comment alone.
//
// A bubble is what makes those assertions worth writing. On a real clock the
// cadence could only be probed by shortening r.tick and allowing a tolerance,
// which asserts an injected number and passes for a ticker of almost any period;
// in synthetic time the PRODUCTION TickInterval is used unchanged and the counts
// are exact. Run's own goroutine parks in a select on ctx.Done and the ticker, so
// it reaches a durably blocked state, and a sweep's file I/O is transient (a
// store rewrite), which is inside the boundary a bubble tolerates. Nothing here
// holds a process, a socket or a PTY.

// bubbleFixture builds a store with one 5-minute schedule plus a runner on the
// bubble's clock. anchorOffset positions the entry's anchor relative to the
// bubble's start, which is what selects whether a slot is already due at t=0.
func bubbleFixture(t *testing.T, anchorOffset time.Duration) (*Store, *fakeLauncher, *Runner) {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	e := Entry{
		ID:     "s1",
		Source: "bundled://demo",
		// The floor interval, so the cadence under test (one tick a minute) is
		// finer than the schedule's own step and the two cannot be confused.
		Spec:    Spec{Freq: FreqMinutely, Interval: minMinuteInterval},
		Enabled: true,
		Anchor:  time.Now().Add(anchorOffset),
	}
	if err := st.Put(t.Context(), &e); err != nil {
		t.Fatalf("Put: %v", err)
	}
	l := &fakeLauncher{}
	r := NewRunner(st, l)
	return st, l, r
}

// TestRun_DoesNotSweepOnEntry pins the documented decision that a slot which
// came due while the container was down is NOT fired at boot.
//
// The anchor is placed so the schedule's slot lands exactly on the bubble's start
// instant and inside MissGrace, which is the one arrangement where an
// entry-time sweep would fire and a first-tick sweep would too — so the only
// thing the assertion can be measuring is WHEN the first sweep happened.
func TestRun_DoesNotSweepOnEntry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, l, r := bubbleFixture(t, -2*time.Minute)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go r.Run(ctx)

		// Half a tick in: a sweep on entry would already have launched.
		synctest.Sleep(TickInterval / 2)
		if srcs, _, _ := l.snap(); len(srcs) != 0 {
			t.Errorf("launches after %v = %v, want none: Run must not sweep on entry",
				TickInterval/2, srcs)
		}
		// The first tick is what may fire it, and the slot is still inside the
		// grace window at that point.
		synctest.Sleep(TickInterval)
		if got := l.launched(); got != 1 {
			t.Errorf("launches after the first tick = %d, want exactly 1", got)
		}
		cancel()
	})
}

// TestRun_FiresOncePerSlotAtTheTickCadence asserts the steady state at exact
// equality: a 5-minute schedule observed by a 1-minute ticker fires exactly once
// per slot, four times in twenty minutes, with no double-fire from the four
// intervening ticks.
//
// On a real clock this is the assertion that cannot be written — twenty minutes
// of wall time, or an injected tick and a tolerance. Measured here in 0.00s.
func TestRun_FiresOncePerSlotAtTheTickCadence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st, l, r := bubbleFixture(t, 0)
		start := time.Now()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go r.Run(ctx)

		const slots = 4
		synctest.Sleep(slots * minMinuteInterval * time.Minute)
		if got := l.launched(); got != slots {
			t.Errorf("launches in %d minutes = %d, want %d (one per 5-minute slot, not one per tick)",
				slots*minMinuteInterval, got, slots)
		}
		// The anchor must sit on the last slot rather than on the tick that
		// observed it, or the schedule drifts by the sweep's own latency.
		wantAnchor := start.Add(slots * minMinuteInterval * time.Minute)
		if got := st.List()[0].Anchor; !got.Equal(wantAnchor) {
			t.Errorf("anchor = %v, want the last slot %v", got, wantAnchor)
		}
		cancel()
	})
}

// TestRun_ReturnsOnCancel checks the exit path. The bubble is the assertion: a
// Run that ignored cancellation would leave a goroutine blocked on its ticker
// when the bubble's main goroutine exits, which synctest reports as a deadlock
// panic rather than a timeout.
func TestRun_ReturnsOnCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, _, r := bubbleFixture(t, 0)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			r.Run(ctx)
		}()
		synctest.Sleep(TickInterval / 2)
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Error("Run did not return after its context was cancelled")
		}
	})
}
