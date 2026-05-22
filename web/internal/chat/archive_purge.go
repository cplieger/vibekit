// Event-driven archive purge scheduler.
//
// Uses a buffered(1) trigger channel so concurrent archive events
// collapse into a single pending evaluation rather than queuing
// behind a mutex held across disk I/O.
//
//  1. Runs purge every time a chat is archived (so retention=0 means
//     the "archive" file exists for a microsecond before it's deleted).
//  2. Schedules the next purge for `oldest_archive_time + retention`
//     — if the oldest file is 6 hours old and retention is 1 day,
//     the next tick runs in 18 hours.
//  3. Re-evaluates after every purge (newest survivor becomes oldest;
//     oldest+retention is the new deadline).
//  4. Can be re-triggered any time (retention setting change, manual
//     purge, archive event) and recomputes the schedule.
//
// The scheduler lives alongside the chat store but takes the retention
// value as a function so main.go can plumb in whatever source it likes
// (kiro-cli settings, static config, tests).

package chat

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PurgeScheduler owns the archive-purge lifecycle. Uses a dedicated
// goroutine with a trigger channel for true collapse semantics.
type PurgeScheduler struct {
	ctx       context.Context
	store     *Store
	retention func() time.Duration
	triggerCh chan struct{}
	stopCh    chan struct{}
	done      chan struct{}
	once      sync.Once
	started   bool
	mu        sync.Mutex
}

// NewPurgeScheduler builds a scheduler that runs purges based on the
// retention value returned by `retention`. The callback is invoked on
// every scheduling decision so changes to the underlying setting are
// picked up without a restart. The context enables graceful shutdown:
// when ctx is cancelled, the scheduler goroutine exits.
func NewPurgeScheduler(ctx context.Context, store *Store, retention func() time.Duration) *PurgeScheduler {
	return &PurgeScheduler{
		ctx:       ctx,
		store:     store,
		retention: retention,
		triggerCh: make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// Start launches the scheduler goroutine and runs an initial
// evaluation. Subsequent runs are driven by timer callbacks or by
// explicit Trigger() calls from the archive path / settings-change
// path.
func (p *PurgeScheduler) Start() {
	p.mu.Lock()
	p.started = true
	p.mu.Unlock()
	go p.loop()
	p.Trigger()
}

// Stop signals the scheduler goroutine to exit and waits for it to
// finish. Safe to call even if Start was never called. Called at
// shutdown.
func (p *PurgeScheduler) Stop() {
	p.once.Do(func() { close(p.stopCh) })
	p.mu.Lock()
	started := p.started
	p.mu.Unlock()
	if started {
		<-p.done
	}
}

// Trigger requests a purge evaluation. Safe to call from any
// goroutine; concurrent calls collapse into a single pending
// evaluation via the buffered channel. No-op after Stop.
func (p *PurgeScheduler) Trigger() {
	select {
	case <-p.stopCh:
		return
	default:
	}
	select {
	case p.triggerCh <- struct{}{}:
	default:
		// Already a pending trigger — collapse.
	}
}

// loop is the scheduler goroutine. It waits for triggers or timer
// expirations and runs purge + reschedule without holding a mutex
// across I/O.
func (p *PurgeScheduler) loop() {
	defer close(p.done)
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-p.ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-p.stopCh:
			if timer != nil {
				timer.Stop()
			}
			return
		case <-p.triggerCh:
		case <-timerC:
		}
		// Run purge pass.
		retention := p.retention()
		if retention > 0 {
			purgeCtx, purgeCancel := context.WithTimeout(p.ctx, 5*time.Minute)
			p.store.PurgeArchived(purgeCtx, retention)
			purgeCancel()
		}
		// Schedule next run.
		if timer != nil {
			timer.Stop()
		}
		timer = nil
		timerC = nil
		if retention > 0 {
			if oldest, ok := oldestArchiveMTime(p.ctx, p.store.dir); ok {
				const minWait = 5 * time.Second
				deadline := oldest.Add(retention)
				wait := max(time.Until(deadline), minWait)
				slog.Debug("archive purge scheduled", "in", wait, "retention", retention)
				timer = time.NewTimer(wait)
				timerC = timer.C
			}
		}
	}
}

// oldestArchiveMTime returns the mtime of the oldest file in the
// archive directory and true, or the zero time and false if the
// directory is empty or unreadable. Unreadable (non-ENOENT) errors
// are logged so a broken archive dir surfaces in Loki instead of
// silently disabling the scheduler.
func oldestArchiveMTime(ctx context.Context, storeDir string) (time.Time, bool) {
	if ctx.Err() != nil {
		return time.Time{}, false
	}
	archiveDir := filepath.Join(storeDir, archiveSubdir)
	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("purge scheduler: readdir",
				"dir", archiveDir, "error", err)
		}
		return time.Time{}, false
	}
	if len(entries) == 0 {
		return time.Time{}, false
	}
	var oldest time.Time
	found := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), chatFileSuffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			slog.Warn("purge scheduler: stat",
				"name", e.Name(), "error", err)
			continue
		}
		if !found || info.ModTime().Before(oldest) {
			oldest = info.ModTime()
			found = true
		}
	}
	return oldest, found
}
