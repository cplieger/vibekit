// Package parallel holds the one bounded fan-out helper two packages need.
//
// It exists because `internal/chat` and `internal/chat/archive` each carried a
// byte-identical copy, and neither could import the other's: `chat` imports
// `archive`, so the reverse is a cycle, and exporting a generic worker pool FROM
// `archive` would warp a package about purging chats into a concurrency utility.
// `internal/chat/io.go` records that same reasoning for a JSON helper it chose to
// keep duplicated; the difference here is that only one of the two copies was
// tested, so a divergence in the untested one had nothing to catch it.
//
// Nothing in here knows about chats, files or storage. A caller that needs one
// should be able to read this package in full before using it.
package parallel

import (
	"context"
	"sync"
)

// Bounded dispatches fn over items with up to maxWorkers concurrent goroutines,
// stopping early when ctx is cancelled.
//
// Workers PULL indices from a shared channel rather than each owning a
// pre-cut slice, which is what keeps a slow item from stalling the rest: a
// finished worker takes the next index immediately instead of waiting for the
// slowest member of its own batch. Degree is min(len(items), maxWorkers), so a
// two-item call does not start eight goroutines.
//
// Storage-agnostic: fn receives the item's INDEX as well as the item, so a
// caller collects results into a pre-sized slice by index and needs no mutex.
// The ctx check is per item, so cancellation lands mid-drain rather than only
// between batches.
func Bounded[T any](ctx context.Context, items []T, maxWorkers int, fn func(i int, item T)) {
	if len(items) == 0 {
		return
	}
	workers := min(len(items), maxWorkers)
	work := make(chan int, len(items))
	for i := range items {
		work <- i
	}
	close(work)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for idx := range work {
				if ctx.Err() != nil {
					return
				}
				fn(idx, items[idx])
			}
		})
	}
	wg.Wait()
}
