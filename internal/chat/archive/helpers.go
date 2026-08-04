package archive

import (
	"context"
	"sync"
)

// boundedParallel dispatches fn over items with up to maxWorkers concurrent
// goroutines.
func boundedParallel[T any](ctx context.Context, items []T, maxWorkers int, fn func(i int, item T)) {
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
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for idx := range work {
				if ctx.Err() != nil {
					return
				}
				fn(idx, items[idx])
			}
		}()
	}
	wg.Wait()
}
