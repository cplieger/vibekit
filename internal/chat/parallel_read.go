package chat

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/parallel"
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
// because readChatHeader is read-only and writes use atomic temp+rename
// (readers always see a complete file). It used to take a `label` for the
// diagnostic prefix, when the archive list was a second caller passing
// "archived chat"; with one caller the parameter only produced the
// doubled "chat chat:" warning.
func readHeadersParallel(
	ctx context.Context,
	valid []chatEntry,
) (headersOut []api.ChatHeader, complete bool) {
	if len(valid) == 0 {
		return nil, true
	}
	const maxWorkers = 8
	type result struct {
		header api.ChatHeader
		ok     bool
		// lost marks a chat that EXISTS but could not be read. Distinct from
		// !ok, which also covers a chat that vanished mid-scan.
		lost bool
	}
	results := make([]result, len(valid))

	parallel.Bounded(ctx, valid, maxWorkers, func(idx int, ce chatEntry) {
		h, err := readChatHeader(ce.path, "chat "+ce.id)
		if err != nil {
			// ENOENT is a concurrent delete: the chat is genuinely gone, so
			// dropping it is correct and the scan is still complete. Anything
			// else means a chat that exists is missing from the result, which
			// callers deriving a keep-list from it must not treat as authority.
			if !errors.Is(err, os.ErrNotExist) {
				slog.Warn("chat: skipping unreadable file",
					"chat_id", ce.id, "error", err)
				results[idx] = result{lost: true}
			}
			return
		}
		results[idx] = result{header: *h, ok: true}
	})

	headers := make([]api.ChatHeader, 0, len(valid))
	complete = true
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
