package parallel

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestBoundedParallel_EmptyItems(t *testing.T) {
	ctx := t.Context()
	var called atomic.Int32
	Bounded(ctx, []int{}, 4, func(_, _ int) {
		called.Add(1)
	})
	if called.Load() != 0 {
		t.Errorf("fn called %d times for empty items, want 0", called.Load())
	}
}

func TestBoundedParallel_ConcurrencyBound(t *testing.T) {
	ctx := t.Context()
	const maxWorkers = 3
	items := make([]int, 20)

	var peak atomic.Int32
	var current atomic.Int32

	Bounded(ctx, items, maxWorkers, func(_, _ int) {
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

	if peak.Load() > int32(maxWorkers) {
		t.Errorf("peak concurrency = %d, want <= %d", peak.Load(), maxWorkers)
	}
}

func TestBoundedParallel_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	items := make([]int, 100)
	var processed atomic.Int32

	Bounded(ctx, items, 2, func(i, _ int) {
		processed.Add(1)
		if i == 5 {
			cancel()
		}
		time.Sleep(time.Millisecond)
	})

	got := processed.Load()
	if got >= int32(len(items)) {
		t.Errorf("processed all %d items despite cancellation", got)
	}
}

// TestBounded_SlowItemDoesNotStallTheRest is the property the pull-based shape
// exists for, and neither of the two copies this package replaced covered it.
//
// A fixed-size BATCH loop blocks every slot until the slowest member of the
// current batch returns. Workers pulling from a shared channel do not: one slow
// item occupies one worker while the others keep draining.
//
// The assertion is about how many fast items finish WHILE the slow one is still
// held, not about eventual completion, and that distinction is the whole test. An
// eventual-completion assertion is satisfied by a batch loop as soon as the slow
// item is released, so it reports success for the behaviour it exists to reject.
// With maxWorkers=2, a batch loop can only ever finish the slow item's ONE
// batch-mate while it is held; the pool finishes all of them. So the threshold is
// maxWorkers, which no batch loop can reach and any pool clears immediately.
func TestBounded_SlowItemDoesNotStallTheRest(t *testing.T) {
	const (
		maxWorkers = 2
		slowIdx    = 0
	)
	items := make([]int, 12)
	var fastDone atomic.Int32
	// doneAtRelease is the count captured AT THE MOMENT the slow item is let go.
	// Reading fastDone after Bounded returns would be useless: by then every item
	// has run under either implementation, so the only discriminating instant is
	// while the slow one is still held.
	var doneAtRelease atomic.Int32
	slowReleased := make(chan struct{})

	// Released either when enough fast items have proven concurrency, or on a
	// deadline so a regression FAILS rather than deadlocking the suite: a hang
	// burns the whole test timeout and names nothing.
	go func() {
		deadline := time.Now().Add(time.Second)
		for fastDone.Load() < maxWorkers && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
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

	if got := doneAtRelease.Load(); got < maxWorkers {
		t.Errorf("only %d fast items finished while one item was held, want >= %d: "+
			"a slow item stalled the others, which is the batching behaviour the "+
			"pull-based worker pool exists to avoid", got, maxWorkers)
	}
}
