package chat

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"

	"github.com/cplieger/vibekit/internal/api"
)

// chatEntry is a chat file's (id, full path) pair gathered during a
// directory scan. Used by both List and ListArchived to hand off work
// to the parallel header reader.
type chatEntry struct {
	id   string
	path string
}

// boundedParallel dispatches fn over items with up to maxWorkers concurrent
// goroutines. Workers stop early when ctx is cancelled. Storage-agnostic:
// callers manage per-item results / counters via closure.
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

// readHeadersParallel reads chat headers for each entry concurrently
// (bounded at 8 workers) and returns the successfully-read headers.
// `label` is used as both the readChatHeader diagnostic prefix and
// the slog warn message; callers pass "chat" for the active list and
// "archived chat" for the archive list. The oldestCheckpoint hook, if
// non-nil, is called per-chat to enrich OldestCheckpointTag.
//
// This replaces two near-identical worker-pool blocks in archive.go
// (ListArchived) and store_list.go (List). Workers read from a shared
// index channel; no per-chat lock needed because readChatHeader is
// read-only and writes use atomic temp+rename (readers always see a
// complete file).
func readHeadersParallel(
	ctx context.Context,
	valid []chatEntry,
	label string,
	oldestCheckpoint func(context.Context, api.ChatID) string,
) []api.ChatHeader {
	if len(valid) == 0 {
		return nil
	}
	const maxWorkers = 8
	type result struct {
		header api.ChatHeader
		ok     bool
	}
	results := make([]result, len(valid))

	boundedParallel(ctx, valid, maxWorkers, func(idx int, ce chatEntry) {
		h, err := readChatHeader(ce.path, label+" "+ce.id)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				slog.Warn("chat "+label+": skipping unreadable file",
					"chat_id", ce.id, "error", err)
			}
			return
		}
		if oldestCheckpoint != nil {
			h.OldestCheckpointTag = oldestCheckpoint(ctx, api.ChatID(ce.id))
		}
		results[idx] = result{header: *h, ok: true}
	})

	headers := make([]api.ChatHeader, 0, len(valid))
	for i := range results {
		if results[i].ok {
			headers = append(headers, results[i].header)
		}
	}
	return headers
}
