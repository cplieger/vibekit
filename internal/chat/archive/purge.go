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

	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/parallel"
	"github.com/cplieger/vibekit/internal/vibekit"
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

// PurgeResult reports one pass, and is what the scheduler times its next wake-up
// from. NextDeadline is the earliest instant a chat this pass KEPT ON AGE becomes
// purgeable, zero when the pass has nothing to wait for.
//
// Only age-kept chats contribute: an EXEMPT chat (live bridge, open tab, unsent
// draft) is pinned outside the age test while the exemption holds, so a wake-up
// derived from its age has already passed and can never arrive again. An age-kept
// deadline is in the future by construction, so a timer aimed at it cannot spin.
type PurgeResult struct {
	NextDeadline time.Time
	Purged       int
	Kept         int
	Errors       int
}

// Purge deletes chats whose last activity is older than maxAge, and reports the
// pass so the caller can time the next one.
//
// It scans the MAIN chat directory, because chats no longer move: "archived" is
// computed from a chat's age against the retention window rather than stored as
// a state. So the same directory holds live and expired chats, and the age test
// plus the live-chat exemption are what separate them.
func (s *Service) Purge(ctx context.Context, maxAge time.Duration) PurgeResult {
	dir := s.store.Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Error("chat purge: readdir",
				"dir", dir, "error", err)
		}
		return PurgeResult{}
	}
	valid := collectPurgeEntries(entries, dir)
	if len(valid) == 0 {
		return PurgeResult{}
	}

	cutoff := time.Now().Add(-maxAge)
	const maxWorkers = 8
	// Per-index results, reduced serially below: a worker writes only its own slot,
	// so the fan-out needs no lock at all.
	outcomes := make([]purgeOutcome, len(valid))
	deadlines := make([]time.Time, len(valid))
	parallel.Bounded(ctx, valid, maxWorkers, func(i int, entry purgeEntry) {
		outcomes[i], deadlines[i] = s.purgeOne(entry, cutoff, maxAge)
	})

	var res PurgeResult
	for i, outcome := range outcomes {
		switch outcome {
		case purgePurged:
			res.Purged++
		case purgeKept:
			res.Kept++
		case purgeErr:
			res.Errors++
		case purgeSkipped:
		}
		if d := deadlines[i]; !d.IsZero() && (res.NextDeadline.IsZero() || d.Before(res.NextDeadline)) {
			res.NextDeadline = d
		}
	}
	logPurgeResult(res, maxAge)
	return res
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

// purgeOne removes a single chat when its last activity is older than cutoff, and
// reports when it becomes purgeable if it does not: the returned deadline is
// non-zero only for a chat kept by AGE, and an exempt chat contributes none (see
// PurgeResult).
//
// Holds the per-chat mutex across the stat+remove so a concurrent mutate cannot
// race the delete.
func (s *Service) purgeOne(entry purgeEntry, cutoff time.Time, maxAge time.Duration) (purgeOutcome, time.Time) {
	// A chat someone is USING is never purged, regardless of age. This is a
	// hard rule, not a heuristic: a chat open in a tab with a live bridge is
	// active work, and retention is about abandoned work. Without it, a
	// long-running conversation older than the window would be deleted out
	// from under its own tab.
	if s.isLive != nil && s.isLive(vibekit.ChatID(entry.name)) {
		return purgeKept, time.Time{}
	}
	// The second exemption: a chat with an OPEN TAB is never purged either. Same
	// rule, different fact — a reader can have a chat open on the strip with no
	// bridge running at all, and that reader is exactly who the age test cannot
	// see, because reading a chat stamps nothing.
	//
	// Checked BEFORE the record lock, deliberately, and it is what keeps the lock
	// order acyclic: the coordinator's operation lock is taken ahead of a chat
	// record lock everywhere else, so a predicate that reached it from inside one
	// would invert the order. (It reads the tab set under neither.)
	//
	// It makes retention OPT-OUT for a chat left open forever, which is accepted:
	// that is the honest reading of "in use", it is what a reader expects from a
	// tab they deliberately kept, and the alternative is closing a tab under
	// someone to satisfy a timer. The draft exemption below has the same shape and
	// the same answer.
	if s.hasOpenTab != nil && s.hasOpenTab(vibekit.ChatID(entry.name)) {
		return purgeKept, time.Time{}
	}
	m := s.store.Lock(vibekit.ChatID(entry.name))
	m.Lock()
	info, err := os.Stat(entry.path)
	if err != nil {
		m.Unlock()
		if errors.Is(err, os.ErrNotExist) {
			return purgeSkipped, time.Time{}
		}
		slog.Warn("chat purge: stat", "name", entry.name, "error", err)
		return purgeErr, time.Time{}
	}
	// Age from the chat's own UpdatedAt, with mtime only as the unreadable-file
	// fallback (see purgeReferenceTime). Capture the chain BEFORE the remove:
	// onPurge fires afterwards, when the file is gone and the session ids are
	// no longer readable.
	refTime, chain, drafting := s.purgeReferenceTime(entry, info.ModTime())
	// A chat holding an unsent draft is being WORKED IN, and the age test cannot
	// see it: Store.SetDraft deliberately does not stamp UpdatedAt (a 600ms
	// autosave would push the cutoff out a whole window per keystroke), so a
	// paragraph typed into a month-old chat leaves it looking abandoned right up
	// to the moment the reaper deletes it and the words with it. The exemption is
	// the other half of that decision rather than a second rule: authored content
	// nobody has sent is exactly what a retention window does not mean.
	//
	// The design's second predicate — skip a chat with an open TAB — is above,
	// where it needs no chat load and cannot invert the lock order.
	if drafting {
		m.Unlock()
		return purgeKept, time.Time{}
	}
	if !refTime.Before(cutoff) {
		m.Unlock()
		return purgeKept, refTime.Add(maxAge)
	}
	if err := os.Remove(entry.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.Unlock()
		slog.Warn("chat purge: remove", "chat_id", entry.name, "error", err)
		return purgeErr, time.Time{}
	}
	m.Unlock()
	if s.onPurge != nil {
		s.onPurge(vibekit.ChatID(entry.name), chain)
	}
	return purgePurged, time.Time{}
}

// purgeReferenceTime returns the time a purge decision ages from, the chat's
// session chain, and whether it holds an unsent draft — all three from ONE projected
// read. Caller holds the per-chat mutex.
//
// The reference time is the chat's own UpdatedAt, falling back to the file mtime when
// the chat cannot be read: mtime moves for reasons that are not activity, so a purge
// aging from it would keep resetting its own clock. An unreadable chat reports no
// draft, the safe direction — nobody could recover a draft the store cannot decode.
func (s *Service) purgeReferenceTime(entry purgeEntry, mtime time.Time) (refTime time.Time, sessionChain []string, drafting bool) {
	h, err := s.store.LoadRetentionHeader(vibekit.ChatID(entry.name))
	if err != nil {
		return mtime, nil, false
	}
	if h.UpdatedAt <= 0 {
		return mtime, h.SessionChain, h.Drafting
	}
	return time.UnixMilli(h.UpdatedAt), h.SessionChain, h.Drafting
}

// logPurgeResult emits the end-of-pass summary at Warn when any entry
// errored, otherwise at Info.
func logPurgeResult(res PurgeResult, maxAge time.Duration) {
	if res.Errors > 0 {
		slog.Warn("chat purge: pass complete with errors",
			"purged", res.Purged, "kept", res.Kept, "errors", res.Errors,
			"max_age", maxAge)
		return
	}
	slog.Info("chat purge: pass complete",
		"purged", res.Purged, "kept", res.Kept,
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
	// idleWait is the current back-off for a pass with nothing to wait for. Owned
	// by the loop goroutine (and by a test calling purgeAndReschedule directly),
	// never read from anywhere else, so it needs no lock.
	idleWait time.Duration
	once     sync.Once
	started  bool
	mu       sync.Mutex
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

// purgeBudget bounds one pass. A pass is bounded work — one projected read and
// at most one unlink per chat — so overrunning this means the filesystem is
// wedged, and the loop is better off re-evaluating than waiting.
const purgeBudget = 5 * time.Minute

// purgeAndReschedule runs one purge pass (when retention is positive)
// and always returns an armed timer, so the loop can never go dark.
//
// It used to return (nil, nil) whenever it had nothing to schedule, which is
// reachable two ways: retention <= 0, and an EMPTY chat directory. Both are
// ordinary states, and both left the loop with a nil timer channel whose only
// remaining wake-up was Trigger() — which has exactly one production caller,
// Start. So a fresh container booted with no chats, armed nothing, and never
// purged again for the life of the process; and toggling retention through 0
// and back killed purging permanently, because the toggle path does not
// Trigger. Neither failure was observable: no log, no metric, just a chat
// directory that grows forever while the setting says otherwise.
func (p *PurgeScheduler) purgeAndReschedule(ctx context.Context) (timer *time.Timer, timerC <-chan time.Time) {
	retention := p.retention()
	var res PurgeResult
	if retention > 0 {
		purgeCtx, purgeCancel := context.WithTimeout(ctx, purgeBudget)
		res = p.svc.Purge(purgeCtx, retention)
		purgeCancel()
	}
	wait := p.armWait(retention, res)
	slog.Debug("chat purge scheduled", "in", wait, "retention", retention,
		"purged", res.Purged, "kept", res.Kept, "has_deadline", !res.NextDeadline.IsZero())
	t := time.NewTimer(wait)
	return t, t.C
}

// Wait bounds. minWait floors a deadline that is nearly upon us, maxWait ceilings
// everything: it is how stale an armed wake-up can be after a retention change
// (the settings path does not Trigger), and the re-check interval when retention
// is off. idleBase is the first wait after a pass with nothing to wait for, and
// it doubles per consecutive such pass up to maxWait.
const (
	minWait  = 5 * time.Second
	maxWait  = 1 * time.Hour
	idleBase = 1 * time.Minute
)

// armWait is how long the loop sleeps before its next pass, and where the spin
// lived. Two rules prevent one. The wake-up comes from the PASS, never from the
// directory: it used to be the oldest chat FILE's mtime plus the window floored at
// 5s, and an exempt chat holds that floor in the past permanently, so the loop
// re-scanned every 5 seconds forever. And a pass with nothing to wait for BACKS OFF
// rather than re-asking a question only an unobserved change can answer
// differently; a purge resets it, being evidence the store is in use.
func (p *PurgeScheduler) armWait(retention time.Duration, res PurgeResult) time.Duration {
	if retention <= 0 {
		// Keep-forever. Re-check on the ceiling rather than going dark, so turning
		// retention back on takes effect within one interval.
		p.idleWait = 0
		return maxWait
	}
	if !res.NextDeadline.IsZero() {
		p.idleWait = 0
		return min(max(time.Until(res.NextDeadline), minWait), maxWait)
	}
	if res.Purged > 0 {
		p.idleWait = 0
	}
	p.idleWait = nextIdleWait(p.idleWait)
	return p.idleWait
}

// nextIdleWait doubles an idle wait, starting at idleBase and capped at maxWait.
func nextIdleWait(current time.Duration) time.Duration {
	if current <= 0 {
		return idleBase
	}
	return min(current*2, maxWait)
}
