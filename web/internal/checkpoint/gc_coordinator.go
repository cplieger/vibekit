// GC coordination: lifecycle management for the periodic blob GC.
//
// Extracted from Store to separate the GC scheduling concern (interval,
// start/stop, idempotency) from the manager-registry concern. The
// gcCoordinator owns the goroutine lifecycle; Store delegates
// StartBackgroundTasks/Stop to it.

package checkpoint

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// gcCoordinator manages the blob GC goroutine lifecycle. One instance
// per Store; constructed at Store creation time.
type gcCoordinator struct {
	cached    func() map[string]*Manager
	stopCh    chan struct{}
	gcLock    *sync.RWMutex
	configDir string
	done      sync.WaitGroup
	mu        sync.Mutex
	running   bool
}

// newGCCoordinator builds a coordinator. cached is a function that
// returns the current set of cached managers (for the fast-path
// collection phase). gcLock is the shared lock that coordinates
// snapshot writes against GC sweeps.
func newGCCoordinator(configDir string, gcLock *sync.RWMutex, cached func() map[string]*Manager) *gcCoordinator {
	return &gcCoordinator{
		configDir: configDir,
		gcLock:    gcLock,
		cached:    cached,
		stopCh:    make(chan struct{}),
	}
}

// Start kicks off the periodic blob GC. Runs one immediate sweep,
// then every blobGCInterval. Idempotent — a second call while a
// gcLoop is already running is a no-op. The parent context is used
// for cancellation of in-flight I/O; the stop channel remains for
// the ticker loop's select.
func (gc *gcCoordinator) Start(ctx context.Context) {
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
		"interval", blobGCInterval)

	// Immediate sweep using the parent context directly.
	gc.runOnce(ctx)

	gc.done.Add(1)
	go gc.loop(ctx, stop)
}

// Stop halts the background GC goroutine and waits for it to finish.
// Safe to call even if Start was never invoked.
func (gc *gcCoordinator) Stop() {
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

func (gc *gcCoordinator) loop(ctx context.Context, stop <-chan struct{}) {
	defer gc.done.Done()

	t := time.NewTicker(blobGCInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			gc.runOnce(ctx)
		}
	}
}

// runOnce runs one GC sweep with structured start/finish logs.
func (gc *gcCoordinator) runOnce(ctx context.Context) {
	start := time.Now()
	slog.Info("checkpoint: blob GC started")

	// Phase 1: collect referenced blobs WITHOUT holding gcLock.
	cached := gc.cached()
	referenced, err := collectReferencedBlobs(ctx, gc.configDir, cached)
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

	// Phase 2: sweep unreferenced blobs. The lock is taken per-fanout-dir
	// inside sweepBlobs so concurrent snapshot writes only block if they
	// target the same 2-char prefix (1/256 chance). The 5-minute age gate
	// provides the primary safety guarantee; the lock is defense-in-depth.
	removed, scanned, sweepErr := sweepBlobs(ctx, gc.configDir, referenced, gc.gcLock)

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
