package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/cplieger/jsonx/bounded"
	"github.com/cplieger/vibekit/internal/api"
	"golang.org/x/sync/singleflight"
)

// chatEntry is a chat file's (id, full path) pair gathered during a
// directory scan.
type chatEntry struct {
	id   string
	path string
}

// sfDo is a typed wrapper around singleflight.Group.Do.
func sfDo[T any](sf *singleflight.Group, key string, fn func() T) T {
	v, _, _ := sf.Do(key, func() (any, error) { return fn(), nil })
	t, _ := v.(T)
	return t
}

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

// readHeadersParallel reads chat headers for each entry concurrently
// (bounded at 8 workers) and returns the successfully-read headers plus
// whether it read every chat that exists.
//
// `complete` is false only when a chat file that EXISTS could not be read. A
// chat that vanished mid-scan (ENOENT, a concurrent delete) leaves the scan
// complete: it genuinely has nothing left to account for. The flag matters to
// callers deriving a retention keep-list, where a silently-dropped chat means
// deleting its data.
func readHeadersParallel(
	ctx context.Context,
	valid []chatEntry,
	label string,
) (headersOut []api.ChatHeader, complete bool) {
	if len(valid) == 0 {
		return nil, true
	}
	const maxWorkers = 8
	type result struct {
		header api.ChatHeader
		ok     bool
		lost   bool // exists but unreadable
	}
	results := make([]result, len(valid))

	boundedParallel(ctx, valid, maxWorkers, func(idx int, ce chatEntry) {
		h, err := readChatHeader(ce.path, label+" "+ce.id)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				slog.Warn("chat "+label+": skipping unreadable file",
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

// readCappedFile reads a file at path, enforcing the maxChatFileBytes size cap.
func readCappedFile(path, label string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > maxChatFileBytes {
		return nil, fmt.Errorf("%s file too large: %d bytes (max %d)",
			label, st.Size(), maxChatFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxChatFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxChatFileBytes {
		return nil, fmt.Errorf("%s grew during load: read %d bytes (max %d)",
			label, len(data), maxChatFileBytes)
	}
	return data, nil
}

// readChatFile reads a chat JSON file at path.
func readChatFile(path, label string) (*api.Chat, error) {
	data, err := readCappedFile(path, label)
	if err != nil {
		return nil, err
	}
	var c api.Chat
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &c, nil
}

// chatHeaderOnDisk is a partial-unmarshal struct that skips the Messages array.
type chatHeaderOnDisk struct {
	Messages json.RawMessage `json:"messages"`
	api.ChatHeader
}

// readChatHeader reads a chat JSON file and returns only the header fields.
func readChatHeader(path, label string) (*api.ChatHeader, error) {
	data, err := readCappedFile(path, label)
	if err != nil {
		return nil, err
	}
	var h chatHeaderOnDisk
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	h.MessageCount = countJSONArrayElements(h.Messages)
	return &h.ChatHeader, nil
}

// countJSONArrayElements counts top-level elements in a JSON array without
// materializing them: each element is token-skipped via jsonx/bounded, so
// counting never allocates per-element buffers. Returns 0 for
// nil/empty/invalid input (count-so-far when an element mid-array is
// malformed).
//
// NOTE: internal/chat/io.go carries an aligned copy (archive cannot import
// chat, and exporting a generic JSON utility from archive just for this
// would warp its surface); keep the two in sync.
func countJSONArrayElements(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	dec := bounded.NewDecoder(bytes.NewReader(raw), 0)
	if ok, err := dec.Open('['); err != nil || !ok {
		return 0
	}
	count := 0
	for dec.More() {
		if err := dec.Skip(); err != nil {
			break
		}
		count++
	}
	return count
}
