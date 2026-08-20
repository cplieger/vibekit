package parallel

// Every assertion about the pool's SHAPE runs in a synctest bubble, because on a
// real clock none of the three properties this package exists for is assertable
// as an equality — only as a bound wide enough to admit the behaviour being
// rejected. Measured on go1.27.0, each conversion strengthened the assertion it
// replaced and the whole file now costs no real time:
//
//   - peak concurrency was `<= maxWorkers`, which a pool that started ONE worker
//     satisfies. Inside a bubble the clock cannot advance until every worker is
//     durably blocked on its sleep, so the peak is exactly the degree and the
//     assertion is an equality. Red-checked: `workers := 1` passes the old bound
//     and fails the new equality naming `peak=1, want exactly 3`.
//   - cancellation was `processed < len(items)`, which a check performed once
//     every ten items also satisfies (measured: that mutant processes 20 of 100
//     and clears the old bound). It is now the exact count, which pins the
//     per-item check the package doc claims.
//   - the slow-item property released its held item from a 1s real-clock poll and
//     asserted a `>= maxWorkers` threshold. synctest.Wait releases it at the
//     instant nothing else in the bubble can progress, which turns the threshold
//     into "every other item finished while one was held" — and that equality
//     rejects a pre-cut-slice batch loop, which the old threshold admitted.
//
// The sleeps inside fn are class (c): the sleep IS the fixture, holding a worker
// so the counter can be observed. They stay, and inside a bubble they are what
// makes the observation exact rather than probabilistic.

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestBoundedParallel_EmptyItems(t *testing.T) {
	var called atomic.Int32
	Bounded(t.Context(), []int{}, 4, func(_, _ int) {
		called.Add(1)
	})
	if called.Load() != 0 {
		t.Errorf("fn called %d times for empty items, want 0", called.Load())
	}
}

// TestBoundedParallel_DegreeIsExactlyTheBound asserts BOTH directions of
// `min(len(items), maxWorkers)`: the pool never exceeds the bound, and it
// actually reaches it.
//
// The second direction is the one a real clock cannot hold. `peak <= maxWorkers`
// is satisfied by a pool that runs everything on one goroutine, so the test
// named for the concurrency bound could not tell a working pool from a serial
// loop.
func TestBoundedParallel_DegreeIsExactlyTheBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const maxWorkers = 3
		items := make([]int, 20)

		var peak, current atomic.Int32
		Bounded(t.Context(), items, maxWorkers, func(_, _ int) {
			cur := current.Add(1)
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			current.Add(-1)
		})

		if got := peak.Load(); got != maxWorkers {
			t.Errorf("peak concurrency = %d, want exactly %d", got, maxWorkers)
		}
	})
}

// TestBoundedParallel_DegreeIsCappedByTheItemCount is the other arm of the min:
// a two-item call must not start eight goroutines.
func TestBoundedParallel_DegreeIsCappedByTheItemCount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		items := make([]int, 2)

		var peak, current atomic.Int32
		Bounded(t.Context(), items, 8, func(_, _ int) {
			cur := current.Add(1)
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			current.Add(-1)
		})

		if got := peak.Load(); got != int32(len(items)) {
			t.Errorf("peak concurrency = %d for %d items, want exactly %d",
				got, len(items), len(items))
		}
	})
}

// TestBoundedParallel_CancellationLandsOnTheNextItem pins the per-item ctx check
// the package doc claims ("cancellation lands mid-drain rather than only between
// batches") as an exact count.
//
// With two workers pulling in order, the item that cancels is index 5, its
// in-flight sibling is index 4, and both workers see the dead context on their
// next pull — so exactly six items run. The old assertion was `processed <
// len(items)`, which a check performed every tenth item also satisfies.
func TestBoundedParallel_CancellationLandsOnTheNextItem(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const cancelAt = 5
		ctx, cancel := context.WithCancel(t.Context())
		items := make([]int, 100)
		var processed atomic.Int32

		Bounded(ctx, items, 2, func(i, _ int) {
			processed.Add(1)
			if i == cancelAt {
				cancel()
			}
			time.Sleep(time.Millisecond)
		})

		// Indices 0..cancelAt: the canceller and every item dispatched before it.
		if got, want := processed.Load(), int32(cancelAt+1); got != want {
			t.Errorf("processed %d items after cancelling at index %d, want exactly %d: "+
				"the ctx check must run per item, not per batch", got, cancelAt, want)
		}
	})
}

// TestBounded_SlowItemDoesNotStallTheRest is the property the pull-based shape
// exists for, and neither of the two copies this package replaced covered it.
//
// A worker pool that pulls indices from a shared channel keeps draining while one
// item is held; any loop that hands each worker a fixed batch does not, because
// the batch containing the slow item stalls for its whole remainder.
//
// The discriminating instant is while the slow item is still HELD — after
// Bounded returns, every item has run under either implementation. synctest.Wait
// is exactly that instant: it returns when every other goroutine in the bubble
// is durably blocked, which is the moment no further progress is possible with
// the slow item outstanding. So the count captured there is the property, and it
// is an EQUALITY: a pull pool finishes all 11 siblings, a barrier-per-batch loop
// finishes 1, and a pre-cut-slice loop finishes 6 (measured, 12 items / 2
// workers). The `>= maxWorkers` threshold this replaced admitted the last of
// those, since 6 clears 2.
func TestBounded_SlowItemDoesNotStallTheRest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			maxWorkers = 2
			slowIdx    = 0
		)
		items := make([]int, 12)
		var fastDone atomic.Int32
		// doneAtRelease is the count captured AT THE MOMENT the slow item is let
		// go. Reading fastDone after Bounded returns would be useless: by then
		// every item has run under either implementation.
		var doneAtRelease atomic.Int32
		slowReleased := make(chan struct{})

		go func() {
			// Everything else in the bubble is now durably blocked: the fast
			// items have all run or the implementation cannot run them, and the
			// slow one is parked on slowReleased.
			synctest.Wait()
			doneAtRelease.Store(fastDone.Load())
			close(slowReleased)
		}()

		Bounded(t.Context(), items, maxWorkers, func(i, _ int) {
			if i == slowIdx {
				<-slowReleased
				return
			}
			fastDone.Add(1)
		})

		if got, want := doneAtRelease.Load(), int32(len(items)-1); got != want {
			t.Errorf("%d of %d fast items finished while one item was held, want all %d: "+
				"a slow item stalled the others, which is the batching behaviour the "+
				"pull-based worker pool exists to avoid", got, want, want)
		}
	})
}
