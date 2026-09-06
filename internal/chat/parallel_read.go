package chat

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/cplieger/vibekit/internal/parallel"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// chatEntry is a chat file's (id, full path) pair gathered during a
// directory scan, handed off to the parallel header reader.
type chatEntry struct {
	id   string
	path string
}

// readHeadersParallel reads chat headers for each entry concurrently (bounded at
// 8 workers) and returns the successfully-read headers.
//
// No per-chat lock is needed: readChatHeader is read-only and writes go through
// atomic temp+rename.
func readHeadersParallel(
	ctx context.Context,
	valid []chatEntry,
	fileCap chatFileCap,
) (headersOut []vibekit.ChatHeader, complete bool) {
	if len(valid) == 0 {
		return nil, true
	}
	const maxWorkers = 8
	type result struct {
		header vibekit.ChatHeader
		ok     bool
		// lost: the chat EXISTS but could not be read; !ok also covers a vanished one.
		lost bool
	}
	results := make([]result, len(valid))

	ran := parallel.Bounded(ctx, valid, maxWorkers, func(idx int, ce chatEntry) {
		h, err := readChatHeader(ce.path, "chat "+ce.id, fileCap)
		if err != nil {
			// ENOENT is a concurrent delete: genuinely gone. Anything else leaves an
			// existing chat missing, which a keep-list caller must not read as whole.
			if !errors.Is(err, os.ErrNotExist) {
				slog.Warn("chat: skipping unreadable file",
					"chat_id", ce.id, "error", err)
				results[idx] = result{lost: true}
			}
			return
		}
		results[idx] = result{header: *h, ok: true}
	})

	headers := make([]vibekit.ChatHeader, 0, len(valid))
	// An unvisited slot is zero-valued, so neither ok nor lost: completeness has to
	// come from the item count. A truncated scan marked complete authorises the
	// session reaper to delete the KAS sessions of every chat it missed.
	complete = ran == len(valid)
	for i := range results {
		switch {
		case results[i].ok:
			headers = append(headers, results[i].header)
		case results[i].lost:
			complete = false
		}
	}
	return headers, complete
}
