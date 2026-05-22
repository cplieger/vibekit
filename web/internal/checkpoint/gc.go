// Blob garbage collector.
//
// Blobs are shared across chats; deleting a chat doesn't know which
// blobs were hers alone. A separate sweep walks every chat's event
// log, builds the union of referenced SHAs, and removes blobs NOT
// in that set. Runs at startup (post rebuild) and on a periodic
// ticker so long-lived vibekit instances don't accumulate
// unbounded garbage.
//
// Correctness: we hold a lock on the Store's rebuild path while
// the GC runs so a Snapshot in progress can't be confused for an
// orphan (its event is already on disk before the write hits the
// blob store, because Append happens before the Restore calls
// blobs.Get; but for the snapshot write path, blobs.Put happens
// before log.Append — see comment in the GC sweep about the
// safety window).
//
// Cancellation: runBlobGC accepts a context so Hub.Shutdown can
// interrupt an in-flight sweep. A long-running sweep on a huge
// repo would otherwise stall the shutdown past the container
// grace period and trigger SIGKILL. Every fanout-dir iteration
// and every per-chat event-log read checks ctx.Err before
// continuing.

package checkpoint

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// blobGCMinAge is the minimum age a blob must have before it's
// eligible for collection. Defended by the Snapshot code path: the
// blob is written to disk BEFORE the event is appended, so a fresh
// blob with no event referencing it could exist for a brief window.
// 5 minutes is many orders of magnitude larger than that window.
const blobGCMinAge = 5 * time.Minute

// sweepBlobs walks the blob fanout directories and removes blobs
// not in the referenced set. The gcLock is held per-fanout-dir (not
// for the entire sweep) so concurrent snapshot writes only block if
// they target the same 2-char prefix directory.
func sweepBlobs(ctx context.Context, configDir string, referenced map[string]struct{}, gcLock *sync.RWMutex) (removed, scanned int, err error) {
	blobsDir := blobsRoot(configDir)
	fanout, err := os.ReadDir(blobsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	for _, prefix := range fanout {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return removed, scanned, ctxErr
		}
		if !prefix.IsDir() || len(prefix.Name()) != 2 {
			continue
		}
		prefixPath := filepath.Join(blobsDir, prefix.Name())
		gcLock.Lock()
		pRemoved, pScanned := sweepFanout(ctx, prefixPath, prefix.Name(), referenced)
		gcLock.Unlock()
		removed += pRemoved
		scanned += pScanned
	}
	return removed, scanned, nil
}

// sweepFanout removes orphan blobs under one two-char fanout dir
// and returns (removed, scanned) counts. Best-effort: any per-entry
// failure is logged and skipped so a single bad file doesn't stall
// the whole sweep. Also reaps the fanout dir itself when it's left
// empty after the pass — long-lived instances would otherwise
// accumulate 256 always-empty shards.
func sweepFanout(ctx context.Context, dir, prefix string, referenced map[string]struct{}) (removed, scanned int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.Warn("checkpoint gc: read fanout dir failed",
			"dir", dir, "error", err)
		return 0, 0
	}
	for _, entry := range entries {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return removed, scanned
		}
		if entry.IsDir() {
			continue
		}
		scanned++
		hash := prefix + entry.Name()
		if _, keep := referenced[hash]; keep {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			// entry.Info() can race with legitimate file
			// removal by other code paths (ReadDir captures
			// the directory entry, entry.Info reaches back
			// to the inode which may be gone) — those are
			// expected and silent. Any other failure
			// (EIO, permission) gets a Debug breadcrumb so
			// operators can tell "repeated skip on one
			// entry" from "clean sweep".
			if !os.IsNotExist(err) {
				slog.Debug("checkpoint gc: entry info failed",
					"dir", dir, "name", entry.Name(),
					"error", err)
			}
			continue
		}
		// Defense-in-depth: skip anything that isn't a regular
		// file. A planted symlink at a valid-looking hash path
		// could be followed by a future Get; also the mtime
		// ModTime() we gate on below resolves through symlinks
		// on POSIX, so we'd happily delete an age-gated symlink
		// that points at a fresh file elsewhere. Only real
		// files are considered.
		if !info.Mode().IsRegular() {
			continue
		}
		// Age gate: only delete blobs older than 5 minutes. A
		// brand-new blob might belong to a snapshot whose event
		// hasn't hit disk yet — the Snapshot code path puts the
		// blob first, then appends the event. GCing that blob
		// in the microseconds window between the two calls
		// would corrupt a future Restore. 5 minutes is
		// luxuriously wide; the real window is well under a
		// millisecond.
		if !olderThanGCMinAge(info) {
			continue
		}
		full := filepath.Join(dir, entry.Name())
		if rmErr := os.Remove(full); rmErr != nil {
			slog.Warn("checkpoint gc: remove blob failed",
				"path", full, "error", rmErr)
			continue
		}
		removed++
	}
	// Reap the fanout dir if the sweep emptied it. Races with a
	// concurrent Snapshot's MkdirAll are fine: Rmdir fails with
	// ENOTEMPTY and we ignore the error; the winning MkdirAll
	// will recreate it as needed.
	if removed > 0 {
		if after, rerr := os.ReadDir(dir); rerr == nil && len(after) == 0 {
			_ = os.Remove(dir)
		}
	}
	return removed, scanned
}

// olderThanGCMinAge returns true iff the blob's mtime is far
// enough in the past that we can safely consider it an orphan.
// Separated for testability.
func olderThanGCMinAge(info os.FileInfo) bool {
	return time.Since(info.ModTime()) >= blobGCMinAge
}

// collectReferencedBlobs walks every chat's event log and returns
// the set of SHAs referenced by any snapshot event (before or
// after). For chats with a cached Manager, reads from in-memory
// state (O(1) per blob ref) instead of re-reading disk. Falls back
// to disk for chats not yet loaded with bounded concurrency (8
// workers) to reduce GC collection time on cold instances. Returns
// an error on any non-"not exist" read failure so callers don't
// mass-delete blobs owned by the unreadable chat.
func collectReferencedBlobs(ctx context.Context, configDir string, cached map[string]*Manager) (map[string]struct{}, error) {
	chatsDir := chatsRoot(configDir)
	entries, err := os.ReadDir(chatsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]struct{}{}, nil
		}
		return nil, err
	}
	referenced := map[string]struct{}{}

	// Fast path: collect from cached managers sequentially (in-memory, O(1)).
	var uncached []string
	for _, e := range entries {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if !e.IsDir() {
			continue
		}
		chatID := e.Name()
		if m, ok := cached[chatID]; ok {
			for _, sha := range m.ReferencedBlobs(ctx) {
				referenced[sha] = struct{}{}
			}
			continue
		}
		uncached = append(uncached, chatID)
	}

	// Slow path: read uncached chat logs concurrently with bounded workers.
	if len(uncached) > 0 {
		shas, uncachedErr := collectUncachedBlobs(ctx, configDir, uncached)
		if uncachedErr != nil {
			return nil, uncachedErr
		}
		for _, sha := range shas {
			referenced[sha] = struct{}{}
		}
	}

	return referenced, nil
}

// collectUncachedBlobs reads event logs for the given chat IDs
// concurrently (bounded to 8 workers) and returns all blob SHAs
// referenced by snapshot events. Returns an error on any non-"not
// exist" read failure so callers don't mass-delete blobs owned by
// the unreadable chat.
func collectUncachedBlobs(ctx context.Context, configDir string, chatIDs []string) ([]string, error) {
	type chatRefs struct {
		shas []string
	}
	results := make([]chatRefs, len(chatIDs))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for i, chatID := range chatIDs {
		g.Go(func() error {
			if gctx.Err() != nil {
				return gctx.Err()
			}
			log := newEventLog(configDir, chatID)
			events, readErr := log.Read()
			if readErr != nil {
				slog.Error("checkpoint gc: blocked by unreadable chat log",
					"chat_id", chatID, "error", readErr,
					"action", "sweep aborted to avoid mass blob deletion")
				return fmt.Errorf("read event log for %s: %w", chatID, readErr)
			}
			var shas []string
			for j := range events {
				ev := &events[j]
				if ev.BeforeSHA != "" {
					shas = append(shas, ev.BeforeSHA)
				}
				if ev.AfterSHA != "" {
					shas = append(shas, ev.AfterSHA)
				}
			}
			results[i] = chatRefs{shas: shas}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	var all []string
	for _, r := range results {
		all = append(all, r.shas...)
	}
	return all, nil
}
