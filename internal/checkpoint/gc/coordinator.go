// Package gc implements the blob garbage collector for the checkpoint
// subsystem. It periodically sweeps orphaned blobs that are no longer
// referenced by any chat's event log.
package gc

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

// BlobRefer returns the set of blob SHAs referenced by a single chat's
// event log. The checkpoint.Manager type satisfies this interface.
type BlobRefer interface {
	ReferencedBlobs(ctx context.Context) []string
}

// Coordinator manages the blob GC goroutine lifecycle. One instance
// per Store; constructed at Store creation time.
type Coordinator struct {
	cached     func() map[string]BlobRefer
	stopCh     chan struct{}
	configDir  string
	blobsDir   string
	chatsDir   string
	eventsFile string
	interval   time.Duration
	done       sync.WaitGroup
	mu         sync.Mutex
	// lastRefCount is read/written from collectReferencedBlobs which
	// concurrent RunOnce callers can enter in parallel. The value is
	// only used as a map-preallocation hint — staleness across reads
	// is harmless — but the read/write pair is still a data race
	// without explicit synchronisation.
	lastRefCount atomic.Int64
	running      bool
}

// NewCoordinator builds a Coordinator. cached returns the current set
// of cached managers (for the fast-path collection phase). The 5-minute
// blob age gate (blobGCMinAge) provides the primary safety guarantee
// against removing in-flight blobs; no per-sweep lock is needed.
func NewCoordinator(configDir, blobsDir, chatsDir, eventsFile string, interval time.Duration, _ *sync.RWMutex, cached func() map[string]BlobRefer) *Coordinator {
	return &Coordinator{
		configDir:  configDir,
		blobsDir:   blobsDir,
		chatsDir:   chatsDir,
		eventsFile: eventsFile,
		interval:   interval,
		cached:     cached,
		stopCh:     make(chan struct{}),
	}
}

// Start kicks off the periodic blob GC. Runs one immediate sweep,
// then every interval. Idempotent — a second call while a loop is
// already running is a no-op.
func (gc *Coordinator) Start(ctx context.Context) {
	gc.mu.Lock()
	if gc.running {
		gc.mu.Unlock()
		return
	}
	if gc.stopCh == nil {
		gc.stopCh = make(chan struct{})
	}
	gc.running = true
	stop := gc.stopCh
	gc.mu.Unlock()

	slog.Info("checkpoint: started background tasks",
		"interval", gc.interval)

	gc.runOnceInternal(ctx)

	gc.done.Add(1)
	go gc.loop(ctx, stop)
}

// Stop halts the background GC goroutine and waits for it to finish.
// Safe to call even if Start was never invoked.
func (gc *Coordinator) Stop() {
	gc.mu.Lock()
	ch := gc.stopCh
	gc.stopCh = nil
	gc.mu.Unlock()
	if ch == nil {
		return
	}
	close(ch)
	gc.done.Wait()
	gc.mu.Lock()
	gc.running = false
	gc.mu.Unlock()
	slog.Info("checkpoint: stopped background tasks")
}

func (gc *Coordinator) loop(ctx context.Context, stop <-chan struct{}) {
	defer gc.done.Done()

	t := time.NewTicker(gc.interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			gc.runOnceInternal(ctx)
		}
	}
}

// RunOnce runs one GC sweep. Exported for test access.
func (gc *Coordinator) RunOnce(ctx context.Context) {
	gc.runOnceInternal(ctx)
}

// RunOnceWithCounts runs one GC sweep and returns the counts.
// Used by tests that need to verify exact removal counts.
func (gc *Coordinator) RunOnceWithCounts(ctx context.Context) (removed, scanned int, err error) {
	cached := gc.cached()
	referenced, err := gc.collectReferencedBlobs(ctx, cached)
	if err != nil {
		return 0, 0, err
	}
	removed, scanned, err = gc.sweepBlobs(ctx, referenced)
	return removed, scanned, err
}

func (gc *Coordinator) runOnceInternal(ctx context.Context) {
	start := time.Now()
	slog.Info("checkpoint: blob GC started")

	cached := gc.cached()
	referenced, err := gc.collectReferencedBlobs(ctx, cached)
	if err != nil {
		if ctx.Err() != nil {
			slog.Info("checkpoint: blob GC cancelled during collection",
				"duration", time.Since(start))
			return
		}
		slog.Warn("checkpoint: blob GC failed during collection",
			"error", err, "duration", time.Since(start))
		return
	}

	removed, scanned, sweepErr := gc.sweepBlobs(ctx, referenced)

	dur := time.Since(start)
	if sweepErr != nil {
		if ctx.Err() != nil {
			slog.Info("checkpoint: blob GC cancelled during sweep",
				"duration", dur, "removed", removed,
				"scanned", scanned)
			return
		}
		slog.Warn("checkpoint: blob GC failed",
			"error", sweepErr, "duration", dur,
			"removed", removed, "scanned", scanned)
		return
	}
	slog.Info("checkpoint: blob GC finished",
		"removed", removed, "scanned", scanned,
		"duration", dur)
}

func (gc *Coordinator) sweepBlobs(ctx context.Context, referenced map[string]struct{}) (removed, scanned int, err error) {
	fanout, err := os.ReadDir(gc.blobsDir)
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
		prefixPath := filepath.Join(gc.blobsDir, prefix.Name())
		pRemoved, pScanned := sweepFanout(ctx, prefixPath, prefix.Name(), referenced)
		removed += pRemoved
		scanned += pScanned
	}
	return removed, scanned, nil
}

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
		if !blobIsOrphan(dir, prefix, entry, referenced) {
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
	if removed > 0 {
		removeDirIfEmpty(dir)
	}
	return removed, scanned
}

// blobIsOrphan reports whether the entry is an unreferenced, regular blob old
// enough to collect. Referenced blobs, unreadable entries, non-regular files,
// and blobs younger than BlobGCMinAge are all kept (reported false).
func blobIsOrphan(dir, prefix string, entry os.DirEntry, referenced map[string]struct{}) bool {
	hash := prefix + entry.Name()
	if _, keep := referenced[hash]; keep {
		return false
	}
	info, err := entry.Info()
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Debug("checkpoint gc: entry info failed",
				"dir", dir, "name", entry.Name(),
				"error", err)
		}
		return false
	}
	if !info.Mode().IsRegular() {
		return false
	}
	return olderThanGCMinAge(info)
}

// removeDirIfEmpty removes dir if a re-read shows it now holds no entries.
// Best-effort: a read error or a non-empty dir leaves it in place.
func removeDirIfEmpty(dir string) {
	if after, rerr := os.ReadDir(dir); rerr == nil && len(after) == 0 {
		_ = os.Remove(dir)
	}
}

// BlobGCMinAge is the minimum age a blob must have before it's
// eligible for collection.
const BlobGCMinAge = 5 * time.Minute

func olderThanGCMinAge(info os.FileInfo) bool {
	return time.Since(info.ModTime()) >= BlobGCMinAge
}

func (gc *Coordinator) collectReferencedBlobs(ctx context.Context, cached map[string]BlobRefer) (map[string]struct{}, error) {
	entries, err := os.ReadDir(gc.chatsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]struct{}{}, nil
		}
		return nil, err
	}
	referenced := make(map[string]struct{}, max(int(gc.lastRefCount.Load()), 1024))

	uncached, err := gc.mergeCachedRefs(ctx, entries, cached, referenced)
	if err != nil {
		return nil, err
	}

	if len(uncached) > 0 {
		shas, uncachedErr := gc.collectUncachedBlobs(ctx, uncached)
		if uncachedErr != nil {
			return nil, uncachedErr
		}
		for _, sha := range shas {
			referenced[sha] = struct{}{}
		}
	}

	gc.lastRefCount.Store(int64(len(referenced)))
	return referenced, nil
}

// mergeCachedRefs walks the chat directory entries: for chats present in the
// cached-manager map it merges their referenced SHAs into referenced directly,
// and it returns the IDs of chats with no cached manager (which the caller
// resolves by reading their event logs). Honours context cancellation.
func (gc *Coordinator) mergeCachedRefs(ctx context.Context, entries []os.DirEntry, cached map[string]BlobRefer, referenced map[string]struct{}) ([]string, error) {
	var uncached []string
	for _, e := range entries {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if !e.IsDir() {
			continue
		}
		chatID := e.Name()
		m, ok := cached[chatID]
		if !ok {
			uncached = append(uncached, chatID)
			continue
		}
		for _, sha := range m.ReferencedBlobs(ctx) {
			referenced[sha] = struct{}{}
		}
	}
	return uncached, nil
}

func (gc *Coordinator) collectUncachedBlobs(ctx context.Context, chatIDs []string) ([]string, error) {
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
			eventsPath := filepath.Join(gc.chatsDir, chatID, gc.eventsFile)
			shas, readErr := streamEventSHAs(eventsPath)
			if readErr != nil {
				slog.Error("checkpoint gc: blocked by unreadable chat log",
					"chat_id", chatID, "error", readErr,
					"action", "sweep aborted to avoid mass blob deletion")
				return fmt.Errorf("read event log for %s: %w", chatID, readErr)
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
