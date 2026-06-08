package chat

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestBoundedParallel_EmptyItems(t *testing.T) {
	ctx := context.Background()
	var called atomic.Int32
	boundedParallel(ctx, []int{}, 4, func(_, _ int) {
		called.Add(1)
	})
	if called.Load() != 0 {
		t.Errorf("fn called %d times for empty items, want 0", called.Load())
	}
}

func TestBoundedParallel_ConcurrencyBound(t *testing.T) {
	ctx := context.Background()
	const maxWorkers = 3
	items := make([]int, 20)

	var peak atomic.Int32
	var current atomic.Int32

	boundedParallel(ctx, items, maxWorkers, func(_, _ int) {
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
	ctx, cancel := context.WithCancel(context.Background())
	items := make([]int, 100)
	var processed atomic.Int32

	boundedParallel(ctx, items, 2, func(i, _ int) {
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
