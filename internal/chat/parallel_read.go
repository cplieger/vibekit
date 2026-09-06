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

// readHeadersParallel reads chat headers for each entry concurrently
// (bounded at 8 workers) and returns the successfully-read headers.
//
// Workers read from a shared index channel; no per-chat lock is needed
// because readChatHeader is read-only and writes use atomic temp+rename.
//
// This is the 8x multiplier readChatHeader streams for: whatever one header
// costs, a sidebar refresh pays it eight times at once.
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
		// lost marks a chat that EXISTS but could not be read. Distinct from
		// !ok, which also covers a chat that vanished mid-scan.
		lost bool
	}
	results := make([]result, len(valid))

	ran := parallel.Bounded(ctx, valid, maxWorkers, func(idx int, ce chatEntry) {
		h, err := readChatHeader(ce.path, "chat "+ce.id, fileCap)
		if err != nil {
			// ENOENT is a concurrent delete: the chat is genuinely gone.
			// Anything else means a chat that exists is missing from the
			// result, which callers deriving a keep-list from it must not
			// treat as authority.
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
	// A cancelled fan-out ran a PREFIX of the items, so the answer is short
	// whatever the visited slots say. That is the half this used to get wrong: the
	// unvisited slots are zero-valued, which is neither ok nor lost, so a
	// truncated scan reported itself COMPLETE and published a subset of the
	// reader's chats as the whole set — and ReferencedSessionIDs derives the
	// session reaper's keep-list from the same scan, where a partial list marked
	// complete authorises deleting the KAS sessions of every chat that was missed.
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
