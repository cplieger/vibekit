package archive

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// purgeEntry is an archived chat file's (id, full path) pair gathered
// during a purge scan.
type purgeEntry struct {
	name string
	path string
}

// purgeOutcome is the per-entry result of a purge attempt, aggregated
// into the pass counts.
type purgeOutcome int

const (
	purgeSkipped purgeOutcome = iota // already gone; counted as neither
	purgeKept                        // newer than the cutoff
	purgePurged                      // removed
	purgeErr                         // stat/remove failed
)

// Purge deletes archived chats older than maxAge.
func (s *Service) Purge(ctx context.Context, maxAge time.Duration) {
	archiveDir := s.archivePath()
	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Error("chat purge_archived: readdir",
				"dir", archiveDir, "error", err)
		}
		return
	}
	valid := collectPurgeEntries(entries, archiveDir)
	if len(valid) == 0 {
		return
	}

	cutoff := time.Now().Add(-maxAge)
	const maxWorkers = 8
	var purgedCount, keptCount, errCount int32
	var mu sync.Mutex
	boundedParallel(ctx, valid, maxWorkers, func(_ int, entry purgeEntry) {
		var counter *int32
		switch s.purgeOne(entry, cutoff) {
		case purgePurged:
			counter = &purgedCount
		case purgeKept:
			counter = &keptCount
		case purgeErr:
			counter = &errCount
		case purgeSkipped:
		}
		if counter != nil {
			mu.Lock()
			*counter++
			mu.Unlock()
		}
	})

	logPurgeResult(int(purgedCount), int(keptCount), int(errCount), maxAge)
}

// collectPurgeEntries filters a directory listing down to valid chat
// files eligible for purging (skips dirs, non-.json files, and files
// whose trimmed name is not a valid chat id).
func collectPurgeEntries(entries []os.DirEntry, archiveDir string) []purgeEntry {
	var valid []purgeEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), chatFileSuffix) {
			continue
		}
		name := strings.TrimSuffix(e.Name(), chatFileSuffix)
		if !api.ValidChatID(name) {
			continue
		}
		valid = append(valid, purgeEntry{name: name, path: filepath.Join(archiveDir, e.Name())})
	}
	return valid
}

// purgeOne removes a single archived chat (and its plan draft) when its
// mtime is older than cutoff. Holds the per-chat mutex across the
// stat+remove so a concurrent restore/mutate can't race the delete.
func (s *Service) purgeOne(entry purgeEntry, cutoff time.Time) purgeOutcome {
	m := s.store.Lock(api.ChatID(entry.name))
	m.Lock()
	info, err := os.Stat(entry.path)
	if err != nil {
		m.Unlock()
		if errors.Is(err, os.ErrNotExist) {
			return purgeSkipped
		}
		slog.Warn("chat purge_archived: stat", "name", entry.name, "error", err)
		return purgeErr
	}
	// Age from the explicit ArchivedAt stamp, not the file mtime: a
	// skipped/failed post-archive summary write leaves mtime at the chat's
	// last-activity time, which would purge an old-but-just-archived chat
	// almost immediately. mtime is only the fallback for legacy archives
	// (stamped before ArchivedAt existed) and the rare crash between the
	// stamp and the rename.
	if !s.purgeReferenceTime(entry, info.ModTime()).Before(cutoff) {
		m.Unlock()
		return purgeKept
	}
	if err := os.Remove(entry.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.Unlock()
		slog.Warn("chat purge_archived: remove", "chat_id", entry.name, "error", err)
		return purgeErr
	}
	removePurgedPlanDraft(entry)
	m.Unlock()
	if s.onPurge != nil {
		s.onPurge(api.ChatID(entry.name))
	}
	return purgePurged
}

// purgeReferenceTime returns the time a purge decision ages from: the
// chat's explicit ArchivedAt stamp when present, else the file mtime
// (legacy archives written before the stamp existed, an unreadable file,
// or the rare crash between the stamp write and the rename). Caller holds
// the per-chat mutex.
func (s *Service) purgeReferenceTime(entry purgeEntry, mtime time.Time) time.Time {
	c, err := s.loadArchived(api.ChatID(entry.name))
	if err != nil || c.ArchivedAt <= 0 {
		return mtime
	}
	return time.UnixMilli(c.ArchivedAt)
}

// removePurgedPlanDraft removes the companion plan-draft of a purged
// chat. A missing draft is the normal case and not an error.
func removePurgedPlanDraft(entry purgeEntry) {
	draftPath := strings.TrimSuffix(entry.path, chatFileSuffix) + planDraftSuffix
	if err := os.Remove(draftPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("chat purge_archived: remove plan-draft",
			"chat_id", entry.name, "error", err)
	}
}

// logPurgeResult emits the end-of-pass summary at Warn when any entry
// errored, otherwise at Info.
func logPurgeResult(purged, kept, errs int, maxAge time.Duration) {
	if errs > 0 {
		slog.Warn("chat purge_archived: pass complete with errors",
			"purged", purged, "kept", kept, "errors", errs,
			"max_age", maxAge)
		return
	}
	slog.Info("chat purge_archived: pass complete",
		"purged", purged, "kept", kept,
		"max_age", maxAge)
}

// PurgeScheduler owns the archive-purge lifecycle. Uses a dedicated
// goroutine with a trigger channel for true collapse semantics.
type PurgeScheduler struct {
	ctx       context.Context
	svc       *Service
	retention func() time.Duration
	triggerCh chan struct{}
	stopCh    chan struct{}
	done      chan struct{}
	once      sync.Once
	started   bool
	mu        sync.Mutex
}

// NewPurgeScheduler builds a scheduler that runs purges based on the
// retention value returned by `retention`.
func NewPurgeScheduler(ctx context.Context, svc *Service, retention func() time.Duration) *PurgeScheduler {
	return &PurgeScheduler{
		ctx:       ctx,
		svc:       svc,
		retention: retention,
		triggerCh: make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// Start launches the scheduler goroutine and runs an initial evaluation.
func (p *PurgeScheduler) Start() {
	p.mu.Lock()
	p.started = true
	p.mu.Unlock()
	go p.loop()
	p.Trigger()
}

// Stop signals the scheduler goroutine to exit and waits for it to finish.
func (p *PurgeScheduler) Stop() {
	p.once.Do(func() { close(p.stopCh) })
	p.mu.Lock()
	started := p.started
	p.mu.Unlock()
	if started {
		<-p.done
	}
}

// Done returns a channel that is closed when the scheduler goroutine exits.
func (p *PurgeScheduler) Done() <-chan struct{} { return p.done }

// Trigger requests a purge evaluation. Safe to call from any goroutine;
// concurrent calls collapse into a single pending evaluation.
func (p *PurgeScheduler) Trigger() {
	select {
	case <-p.stopCh:
		return
	default:
	}
	select {
	case p.triggerCh <- struct{}{}:
	default:
	}
}

// loop is the scheduler goroutine.
func (p *PurgeScheduler) loop() {
	defer close(p.done)
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-p.ctx.Done():
			stopTimer(timer)
			return
		case <-p.stopCh:
			stopTimer(timer)
			return
		case <-p.triggerCh:
		case <-timerC:
		}
		stopTimer(timer)
		timer, timerC = p.purgeAndReschedule()
	}
}

// stopTimer stops t if it is non-nil. A no-op for the nil timer the loop
// starts with.
func stopTimer(t *time.Timer) {
	if t != nil {
		t.Stop()
	}
}

// purgeAndReschedule runs one purge pass (when retention is positive)
// and returns a freshly-armed timer for the next wake-up, or (nil, nil)
// when nothing remains to schedule.
func (p *PurgeScheduler) purgeAndReschedule() (timer *time.Timer, timerC <-chan time.Time) {
	retention := p.retention()
	if retention > 0 {
		purgeCtx, purgeCancel := context.WithTimeout(p.ctx, 5*time.Minute)
		p.svc.Purge(purgeCtx, retention)
		purgeCancel()
	}
	wait, ok := p.nextWait(retention)
	if !ok {
		return nil, nil
	}
	slog.Debug("archive purge scheduled", "in", wait, "retention", retention)
	t := time.NewTimer(wait)
	return t, t.C
}

// nextWait computes how long to sleep before the next purge: the oldest
// archived file's age plus the retention window, floored at minWait.
// Returns ok=false when retention is disabled or the archive is empty.
func (p *PurgeScheduler) nextWait(retention time.Duration) (time.Duration, bool) {
	if retention <= 0 {
		return 0, false
	}
	oldest, ok := OldestArchiveMTime(p.ctx, p.svc.store.Dir())
	if !ok {
		return 0, false
	}
	const minWait = 5 * time.Second
	deadline := oldest.Add(retention)
	return max(time.Until(deadline), minWait), true
}

// OldestArchiveMTime returns the mtime of the oldest file in the
// archive directory and true, or the zero time and false if the
// directory is empty or unreadable.
func OldestArchiveMTime(ctx context.Context, storeDir string) (time.Time, bool) {
	if ctx.Err() != nil {
		return time.Time{}, false
	}
	archiveDir := filepath.Join(storeDir, Subdir)
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
