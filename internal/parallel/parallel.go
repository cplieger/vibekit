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
	"sync/atomic"
)

// Bounded dispatches fn over items with up to maxWorkers concurrent goroutines,
// stopping early when ctx is cancelled, and REPORTS how many items it ran fn for.
//
// The count is the whole point of the return value: a cancelled fan-out runs a
// PREFIX of the work and the caller is the only thing that knows what a short
// answer means. Answering `done < len(items)` is what lets a caller mark its
// result incomplete instead of publishing a subset as if it were the whole set —
// which is exactly what `internal/chat`'s header scan did, silently, because the
// unvisited result slots were indistinguishable from items that ran and produced
// nothing. Compare against `len(items)`; a caller that genuinely does not care
// (a purge pass, where an unvisited chat simply is not purged) ignores it.
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
func Bounded[T any](ctx context.Context, items []T, maxWorkers int, fn func(i int, item T)) (done int) {
	if len(items) == 0 {
		return 0
	}
	workers := min(len(items), maxWorkers)
	work := make(chan int, len(items))
	for i := range items {
		work <- i
	}
	close(work)
	var ran atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			// Counted locally and published once, so the common path costs no
			// contention: an atomic add per item would serialize eight workers on
			// one cache line for work whose real bound is disk.
			local := int64(0)
			for idx := range work {
				if ctx.Err() != nil {
					// BREAK, never return: a worker that returns here skips the
					// publish below and its completed items vanish from the count,
					// which would report a cancelled scan as emptier than it was.
					break
				}
				fn(idx, items[idx])
				local++
			}
			ran.Add(local)
		})
	}
	wg.Wait()
	return int(ran.Load())
}
