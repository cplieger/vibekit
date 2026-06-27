// Package sessions manages kiro-cli session lockfiles on disk:
// stale lock detection, startup cleanup, PID-based liveness probes,
// and flock-based staleness checks.
package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/cplieger/vibekit/internal/api"
)

// cleanupMaxEntries bounds CleanupStale's startup scan so a
// pathological sessions dir (thousands of stale locks) does not pin
// the HTTP bind behind a multi-second directory walk.
const cleanupMaxEntries = 1000

// Manager manages session lockfiles in a configured directory.
// Zero value is not usable; construct via New.
type Manager struct {
	dir string
}

// New creates a Manager that operates on the given sessions directory.
// An empty dir disables all operations (safe default for tests).
func New(dir string) *Manager {
	return &Manager{dir: dir}
}

// Dir returns the configured sessions directory.
func (m *Manager) Dir() string { return m.dir }

type lockFile struct {
	PID int `json:"pid"`
}

// parseLockFile reads path and returns the recorded PID. ok=false on
// any read error, JSON parse error, or pid==0.
func parseLockFile(path string) (int, bool) {
	info, err := os.Stat(path)
	if err != nil || info.Size() > 4096 {
		return 0, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	var lock lockFile
	if json.Unmarshal(data, &lock) != nil || lock.PID == 0 {
		return 0, false
	}
	return lock.PID, true
}

// IsKiroCLI reports whether the process identified by pid is still
// a kiro-cli instance. Package-level var so tests can stub it.
var IsKiroCLI = func(pid int) bool {
	name, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(name)) == "kiro-cli"
}

// flockProbeStale attempts a non-blocking exclusive flock on path.
// Returns true if the lock is stale (flock succeeded → no process
// holds the fd open).
func flockProbeStale(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	return err == nil
}

// isLockStale combines Kill(pid,0) + /proc/comm + flock probe.
func isLockStale(path string, pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return true
	}
	if !IsKiroCLI(pid) {
		return true
	}
	return flockProbeStale(path)
}

// RemoveStaleLock removes the lock file for sessionID if the owning PID is
// dead. No-op if the lock is absent, malformed, or the PID is alive.
func (m *Manager) RemoveStaleLock(ctx context.Context, sessionID string) {
	if ctx.Err() != nil {
		return
	}
	if !api.ValidSessionID(sessionID) {
		slog.Warn("ignoring stale-lock cleanup for invalid session id",
			"session", sessionID)
		return
	}
	if m.dir == "" {
		return
	}
	lockPath := filepath.Join(m.dir, sessionID+".lock")
	pid, ok := parseLockFile(lockPath)
	if !ok {
		return
	}
	if ctx.Err() != nil {
		return
	}
	if !isLockStale(lockPath, pid) {
		return
	}
	if err := os.Remove(lockPath); err != nil {
		slog.Warn("remove stale lock failed", "session", sessionID, "pid", pid, "error", err)
		return
	}
	slog.Info("removed stale lock", "session", sessionID, "pid", pid)
}

// cleanupWorkers is the concurrency limit for CleanupStale's parallel
// directory scan. Matches the chat package's PurgeArchived pattern.
const cleanupWorkers = 8

// CleanupStale removes stale kiro-cli lock files and empty session
// files on startup. Bounded to cleanupMaxEntries iterations. Uses
// bounded parallelism (8 workers) to reduce startup latency on
// pathological session directories.
func (m *Manager) CleanupStale(ctx context.Context) {
	if m.dir == "" {
		return
	}
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	names := collectLockNames(entries)
	rl, rs := m.cleanupLockEntries(ctx, names)
	if rl > 0 || rs > 0 {
		slog.Info("startup: cleaned stale sessions",
			"scanned", len(names),
			"locks_removed", rl,
			"empty_sessions_removed", rs)
	}
}

// collectLockNames returns the names of the ".lock" entries in a
// directory listing, bounded to cleanupMaxEntries so a pathological
// sessions dir cannot pin startup behind a multi-thousand-entry walk.
func collectLockNames(entries []os.DirEntry) []string {
	var names []string
	for i := range entries {
		if i >= cleanupMaxEntries {
			slog.Warn("startup: cleanup scan truncated",
				"scanned", cleanupMaxEntries, "total", len(entries))
			break
		}
		if filepath.Ext(entries[i].Name()) == ".lock" {
			names = append(names, entries[i].Name())
		}
	}
	return names
}

// cleanupLockEntries processes lock-file names with bounded parallelism
// and returns the total locks and empty sessions removed.
func (m *Manager) cleanupLockEntries(ctx context.Context, names []string) (removedLocks, removedSessions int32) {
	var accLocks, accSessions atomic.Int32
	var wg sync.WaitGroup
	sem := make(chan struct{}, cleanupWorkers)
	for _, name := range names {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(name string) {
			defer func() { <-sem; wg.Done() }()
			if ctx.Err() != nil {
				return
			}
			locks, sessions, _ := m.cleanupOneEntry(ctx, name)
			addClampedInt32(&accLocks, locks)
			addClampedInt32(&accSessions, sessions)
		}(name)
	}
	wg.Wait()
	return accLocks.Load(), accSessions.Load()
}

// addClampedInt32 adds n to dst when n is positive, clamping at
// math.MaxInt32 to avoid an int32 overflow on a pathological count.
func addClampedInt32(dst *atomic.Int32, n int) {
	if n <= 0 {
		return
	}
	if n > math.MaxInt32 {
		n = math.MaxInt32
	}
	dst.Add(int32(n))
}

func (m *Manager) cleanupOneEntry(ctx context.Context, name string) (removedLocks, removedSessions int, counted bool) {
	if filepath.Ext(name) != ".lock" {
		return 0, 0, false
	}
	id := strings.TrimSuffix(name, ".lock")
	if !api.ValidSessionID(id) {
		slog.Warn("startup: skipping lock with invalid id", "name", name)
		return 0, 0, false
	}
	counted = true
	lockPath := filepath.Join(m.dir, name)
	pid, ok := parseLockFile(lockPath)
	if !ok {
		return 0, 0, counted
	}
	if ctx.Err() != nil {
		return 0, 0, counted
	}
	if !isLockStale(lockPath, pid) {
		return 0, 0, counted
	}
	if ctx.Err() != nil {
		return 0, 0, counted
	}
	if rmErr := os.Remove(lockPath); rmErr != nil {
		slog.Warn("startup: remove stale lock failed",
			"session", id, "pid", pid, "error", rmErr)
		return 0, 0, counted
	}
	removedLocks = 1
	jsonlPath := filepath.Join(m.dir, id+".jsonl")
	info, statErr := os.Stat(jsonlPath)
	if statErr != nil || info.Size() != 0 {
		return removedLocks, 0, counted
	}
	if rmErr := os.Remove(jsonlPath); rmErr != nil {
		slog.Warn("startup: remove empty jsonl failed",
			"session", id, "error", rmErr)
		return removedLocks, 0, counted
	}
	jsonPath := filepath.Join(m.dir, id+".json")
	if rmErr := os.Remove(jsonPath); rmErr != nil && !os.IsNotExist(rmErr) {
		slog.Warn("startup: remove companion json failed",
			"session", id, "error", rmErr)
		return removedLocks, 0, counted
	}
	removedSessions = 1
	return removedLocks, removedSessions, counted
}
