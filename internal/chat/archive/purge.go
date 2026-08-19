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
	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/parallel"
)

// purgeEntry is a chat file's (id, full path) pair gathered during a
// purge scan.
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

// Purge deletes chats whose last activity is older than maxAge.
//
// It scans the MAIN chat directory, because chats no longer move: "archived" is
// computed from a chat's age against the retention window rather than stored as
// a state. So the same directory holds live and expired chats, and the age test
// plus the live-chat exemption are what separate them.
func (s *Service) Purge(ctx context.Context, maxAge time.Duration) {
	dir := s.store.Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Error("chat purge: readdir",
				"dir", dir, "error", err)
		}
		return
	}
	valid := collectPurgeEntries(entries, dir)
	if len(valid) == 0 {
		return
	}

	cutoff := time.Now().Add(-maxAge)
	const maxWorkers = 8
	var purgedCount, keptCount, errCount int32
	var mu sync.Mutex
	parallel.Bounded(ctx, valid, maxWorkers, func(_ int, entry purgeEntry) {
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
func collectPurgeEntries(entries []os.DirEntry, dir string) []purgeEntry {
	var valid []purgeEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), chatFileSuffix) {
			continue
		}
		name := strings.TrimSuffix(e.Name(), chatFileSuffix)
		if !ids.ValidChatID(name) {
			continue
		}
		valid = append(valid, purgeEntry{name: name, path: filepath.Join(dir, e.Name())})
	}
	return valid
}

// purgeOne removes a single chat when its last activity is older than
// cutoff. Holds the per-chat mutex across the stat+remove so a concurrent
// mutate can't race the delete.
func (s *Service) purgeOne(entry purgeEntry, cutoff time.Time) purgeOutcome {
	// A chat someone is USING is never purged, regardless of age. This is a
	// hard rule, not a heuristic: a chat open in a tab with a live bridge is
	// active work, and retention is about abandoned work. Without it, a
	// long-running conversation older than the window would be deleted out
	// from under its own tab.
	if s.isLive != nil && s.isLive(api.ChatID(entry.name)) {
		return purgeKept
	}
	m := s.store.Lock(api.ChatID(entry.name))
	m.Lock()
	info, err := os.Stat(entry.path)
	if err != nil {
		m.Unlock()
		if errors.Is(err, os.ErrNotExist) {
			return purgeSkipped
		}
		slog.Warn("chat purge: stat", "name", entry.name, "error", err)
		return purgeErr
	}
	// Age from the chat's own UpdatedAt, with mtime only as the unreadable-file
	// fallback (see purgeReferenceTime). Capture the chain BEFORE the remove:
	// onPurge fires afterwards, when the file is gone and the session ids are
	// no longer readable.
	refTime, chain := s.purgeReferenceTime(entry, info.ModTime())
	if !refTime.Before(cutoff) {
		m.Unlock()
		return purgeKept
	}
	if err := os.Remove(entry.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.Unlock()
		slog.Warn("chat purge: remove", "chat_id", entry.name, "error", err)
		return purgeErr
	}
	m.Unlock()
	if s.onPurge != nil {
		s.onPurge(api.ChatID(entry.name), chain)
	}
	return purgePurged
}

// purgeReferenceTime returns the time a purge decision ages from, plus the
// chat's session chain.
//
// The reference time is the chat's own UpdatedAt — its last activity — falling
// back to the file mtime when the chat cannot be read. UpdatedAt rather than
// mtime because mtime moves for reasons that are not activity (a metadata
// rewrite, a settings-driven field change), and a purge that ages from those
// would keep resetting its own clock.
//
// The chain rides along because this is the ONE place that already loads the
// chat, and the purge needs it: `onPurge` fires after os.Remove(entry.path), so
// by then the file is gone and the session ids are unreadable. Widening this
// read costs no extra I/O and needs no second hook. Caller holds the per-chat
// mutex.
func (s *Service) purgeReferenceTime(entry purgeEntry, mtime time.Time) (refTime time.Time, sessionChain []string) {
	c, err := s.store.Load(api.ChatID(entry.name))
	if err != nil {
		return mtime, nil
	}
	chain := c.SessionChain()
	if c.UpdatedAt <= 0 {
		return mtime, chain
	}
	return time.UnixMilli(c.UpdatedAt), chain
}

// logPurgeResult emits the end-of-pass summary at Warn when any entry
// errored, otherwise at Info.
func logPurgeResult(purged, kept, errs int, maxAge time.Duration) {
	if errs > 0 {
		slog.Warn("chat purge: pass complete with errors",
			"purged", purged, "kept", kept, "errors", errs,
			"max_age", maxAge)
		return
	}
	slog.Info("chat purge: pass complete",
		"purged", purged, "kept", kept,
		"max_age", maxAge)
}

// PurgeScheduler owns the retention-purge lifecycle. Uses a dedicated
// goroutine with a trigger channel for true collapse semantics.
//
// It holds NO context. The scheduler's context arrives at Start, the method that
// runs the loop, and is threaded down as a parameter from there — which is the
// shape the fleet's rule asks for wherever a component has a run method, and it
// is what makes the loop's two exit conditions (ctx cancelled, Stop called)
// readable at the one place both are selected on.
type PurgeScheduler struct {
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
func NewPurgeScheduler(svc *Service, retention func() time.Duration) *PurgeScheduler {
	return &PurgeScheduler{
		svc:       svc,
		retention: retention,
		triggerCh: make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// Start launches the scheduler goroutine under ctx and runs an initial
// evaluation. The loop returns when ctx is cancelled or Stop is called.
func (p *PurgeScheduler) Start(ctx context.Context) {
	p.mu.Lock()
	p.started = true
	p.mu.Unlock()
	go p.loop(ctx)
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
func (p *PurgeScheduler) loop(ctx context.Context) {
	defer close(p.done)
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return
		case <-p.stopCh:
			stopTimer(timer)
			return
		case <-p.triggerCh:
		case <-timerC:
		}
		stopTimer(timer)
		timer, timerC = p.purgeAndReschedule(ctx)
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
// and always returns an armed timer, so the loop can never go dark.
//
// It used to return (nil, nil) whenever nextWait reported not-ok, which is
// reachable two ways: retention <= 0, and an EMPTY chat directory. Both are
// ordinary states, and both left the loop with a nil timer channel whose only
// remaining wake-up was Trigger() — which has exactly one production caller,
// Start. So a fresh container booted with no chats, armed nothing, and never
// purged again for the life of the process; and toggling retention through 0
// and back killed purging permanently, because the toggle path does not
// Trigger. Neither failure was observable: no log, no metric, just a chat
// directory that grows forever while the setting says otherwise.
//
// A poll ceiling fixes both, and also fixes a third, quieter problem: the
// armed wait was uncapped, so a 30-day retention slept ~29 days and no
// setting change could shorten it. Re-checking at most maxWait later costs one
// directory stat per interval and makes every retention change take effect
// within one interval regardless of what was armed when it happened.
func (p *PurgeScheduler) purgeAndReschedule(ctx context.Context) (timer *time.Timer, timerC <-chan time.Time) {
	retention := p.retention()
	if retention > 0 {
		purgeCtx, purgeCancel := context.WithTimeout(ctx, 5*time.Minute)
		p.svc.Purge(purgeCtx, retention)
		purgeCancel()
	}
	wait, hadWork := p.armWait(ctx, retention)
	slog.Debug("chat purge scheduled", "in", wait, "retention", retention, "had_work", hadWork)
	t := time.NewTimer(wait)
	return t, t.C
}

// armWait is how long the loop sleeps before its next pass: the natural deadline
// when there is one, the poll interval when there is not, capped either way.
//
// Extracted from purgeAndReschedule so a test can assert on the value the loop
// actually arms. It was inline, and the test asserted `min(natural, cap) == cap`
// by recomputing the clamp itself — which stayed green when the clamp was deleted
// from production, because the test was proving arithmetic rather than behaviour.
func (p *PurgeScheduler) armWait(ctx context.Context, retention time.Duration) (wait time.Duration, hadWork bool) {
	natural, ok := p.nextWait(ctx, retention)
	if !ok {
		// Nothing to purge right now (retention off, or no chats yet). Re-check
		// on the poll interval rather than going dark. hadWork is returned so the
		// log can tell this apart from a real deadline that happened to be capped
		// at the same value; without it the two states logged identically.
		return maxWait, false
	}
	return min(natural, maxWait), true
}

// maxWait bounds how long the purge loop may sleep between passes. It is the
// ceiling on how stale an armed wake-up can be after a retention change, and
// the re-check interval when there is nothing scheduled at all.
const maxWait = 1 * time.Hour

// nextWait computes how long to sleep before the next purge: the oldest
// chat file's age plus the retention window, floored at minWait.
// Returns ok=false when retention is disabled or the directory is empty.
func (p *PurgeScheduler) nextWait(ctx context.Context, retention time.Duration) (time.Duration, bool) {
	if retention <= 0 {
		return 0, false
	}
	oldest, ok := OldestChatMTime(ctx, p.svc.store.Dir())
	if !ok {
		return 0, false
	}
	const minWait = 5 * time.Second
	deadline := oldest.Add(retention)
	return max(time.Until(deadline), minWait), true
}

// OldestChatMTime returns the mtime of the oldest chat file and true, or the
// zero time and false if the directory is empty or unreadable.
//
// Only a wake-up heuristic for the scheduler, never a purge decision: purgeOne
// ages from the chat's UpdatedAt and exempts live chats. Waking too early costs
// one no-op pass.
func OldestChatMTime(ctx context.Context, storeDir string) (time.Time, bool) {
	if ctx.Err() != nil {
		return time.Time{}, false
	}
	entries, err := os.ReadDir(storeDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("purge scheduler: readdir",
				"dir", storeDir, "error", err)
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
