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
	cutoff := time.Now().Add(-maxAge)

	type purgeEntry struct {
		name string
		path string
	}
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
	if len(valid) == 0 {
		return
	}

	const maxWorkers = 8
	var purgedCount, keptCount, errCount int32
	var mu sync.Mutex

	boundedParallel(ctx, valid, maxWorkers, func(_ int, entry purgeEntry) {
		m := s.store.Lock(api.ChatID(entry.name))
		m.Lock()
		info, err := os.Stat(entry.path)
		if err != nil {
			m.Unlock()
			if !errors.Is(err, os.ErrNotExist) {
				mu.Lock()
				errCount++
				mu.Unlock()
				slog.Warn("chat purge_archived: stat",
					"name", entry.name, "error", err)
			}
			return
		}
		if !info.ModTime().Before(cutoff) {
			m.Unlock()
			mu.Lock()
			keptCount++
			mu.Unlock()
			return
		}
		if err := os.Remove(entry.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			m.Unlock()
			mu.Lock()
			errCount++
			mu.Unlock()
			slog.Warn("chat purge_archived: remove",
				"chat_id", entry.name, "error", err)
			return
		}
		draftPath := filepath.Join(archiveDir, entry.name+planDraftSuffix)
		if err := os.Remove(draftPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("chat purge_archived: remove plan-draft",
				"chat_id", entry.name, "error", err)
		}
		m.Unlock()
		if s.onPurge != nil {
			s.onPurge(api.ChatID(entry.name))
		}
		mu.Lock()
		purgedCount++
		mu.Unlock()
	})

	purged := int(purgedCount)
	kept := int(keptCount)
	errs := int(errCount)
	if errs > 0 {
		slog.Warn("chat purge_archived: pass complete with errors",
			"purged", purged, "kept", kept, "errors", errs,
			"max_age", maxAge)
	} else {
		slog.Info("chat purge_archived: pass complete",
			"purged", purged, "kept", kept,
			"max_age", maxAge)
	}
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
		retention := p.retention()
		if retention > 0 {
			purgeCtx, purgeCancel := context.WithTimeout(p.ctx, 5*time.Minute)
			p.svc.Purge(purgeCtx, retention)
			purgeCancel()
		}
		if timer != nil {
			timer.Stop()
		}
		timer = nil
		timerC = nil
		if retention > 0 {
			if oldest, ok := OldestArchiveMTime(p.ctx, p.svc.store.Dir()); ok {
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
