package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"vibekit/internal/api"
)

// sessionsPath holds the kiro-cli sessions directory path. Set by
// the composition root via SetSessionsDir at startup. Empty string
// disables lock cleanup (safe default for tests and non-standard
// deployments).
var sessionsPath string

// SetSessionsDir configures the kiro-cli sessions directory used by
// RemoveStaleLock and CleanupStaleSessions. Call once from the
// composition root (main.go) at startup before any bridge operations.
func SetSessionsDir(dir string) { sessionsPath = dir }

// sessionsDir returns the configured sessions directory.
func sessionsDir() string { return sessionsPath }

// cleanupMaxEntries bounds CleanupStaleSessions' startup scan so a
// pathological sessions dir (thousands of stale locks) does not pin
// the HTTP bind behind a multi-second directory walk.
const cleanupMaxEntries = 1000

type lockFile struct {
	PID int `json:"pid"`
}

// parseLockFile reads path and returns the recorded PID. ok=false on
// any read error, JSON parse error, or pid==0, so callers can early-
// continue without distinguishing failure modes (each of which is a
// "skip this lock" signal).
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

// validSessionID delegates to api.ValidSessionID — the single source of
// truth for ACP session-id path-safety validation.
func validSessionID(s string) bool {
	return api.ValidSessionID(s)
}

// isKiroCLI reports whether the process identified by pid is still
// a kiro-cli instance. Linux-only belt-and-braces against PID reuse:
// syscall.Kill(pid, 0) cannot distinguish the original kiro-cli from
// an unrelated child (shell spawned via fs/write, agent_terminal,
// another kiro-cli on a different chat) that happened to inherit the
// same recycled PID. Falls open on /proc read error so a transient
// filesystem hiccup does not prematurely delete a live lock.
//
// Package-level var so tests can stub this out for PIDs they plant
// as "live" (the test runner binary is not kiro-cli).
var isKiroCLI = func(pid int) bool {
	name, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		// /proc missing (non-Linux) or race (process exited between
		// Kill(0) and ReadFile): fall open — the caller's Kill(0)
		// already said "process alive", so the safer default is
		// to keep the lock.
		return true
	}
	return strings.TrimSpace(string(name)) == "kiro-cli"
}

// flockProbeStale attempts a non-blocking exclusive flock on path.
// Returns true if the lock is stale (flock succeeded → no process
// holds the fd open). Returns false if the file is actively held
// (EWOULDBLOCK) or on any error (fall open — keep the lock).
// The acquired flock is immediately released by closing the fd.
func flockProbeStale(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false // can't open → fall open, keep lock
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		return false // EWOULDBLOCK or other → held, not stale
	}
	// Flock succeeded → no process holds this file. Release by closing.
	return true
}

// isLockStale combines Kill(pid,0) + /proc/comm + flock probe to
// determine if a lockfile is stale. Returns true if the lock should
// be removed. Falls open (returns false) on ambiguity.
func isLockStale(path string, pid int) bool {
	// Fast path: process is dead.
	if syscall.Kill(pid, 0) != nil {
		return true
	}
	// Process exists — is it kiro-cli?
	if !isKiroCLI(pid) {
		return true // PID recycled into something else
	}
	// Process is alive and named kiro-cli. Final check: does it
	// actually hold this specific lockfile fd open? Closes the
	// TOCTOU window where PID recycled into *another* kiro-cli
	// instance (different session) that doesn't hold this file.
	return flockProbeStale(path)
}

// RemoveStaleLock removes the lock file for sessionID if the owning PID is
// dead. No-op if the lock is absent, malformed, or the PID is alive.
// Rejects session ids that contain path separators or `..` so a
// compromised kiro-cli cannot drive os.Remove outside sessionsDir.
// The context enables graceful cancellation during shutdown.
func RemoveStaleLock(ctx context.Context, sessionID string) {
	if ctx.Err() != nil {
		return
	}
	if !validSessionID(sessionID) {
		slog.Warn("ignoring stale-lock cleanup for invalid session id",
			"session", sessionID)
		return
	}
	dir := sessionsDir()
	if dir == "" {
		return
	}
	lockPath := filepath.Join(dir, sessionID+".lock")
	pid, ok := parseLockFile(lockPath)
	if !ok {
		return
	}
	if ctx.Err() != nil {
		return
	}
	if !isLockStale(lockPath, pid) {
		return // kiro-cli still holds it; keep the lock
	}
	if err := os.Remove(lockPath); err != nil {
		slog.Warn("remove stale lock failed", "session", sessionID, "pid", pid, "error", err)
		return
	}
	slog.Info("removed stale lock", "session", sessionID, "pid", pid)
}

// CleanupStaleSessions removes stale kiro-cli lock files and empty session
// files on startup. Bounded to cleanupMaxEntries iterations and gated
// on validSessionID so a pathological directory cannot delay the HTTP
// bind or drive os.Remove on filenames outside the sessions dir.
// Emits a single summary log line rather than one Info per removal so
// boot after a crash with N stale locks does not flood Loki.
// The context enables graceful cancellation during shutdown.
func CleanupStaleSessions(ctx context.Context) {
	dir := sessionsDir()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var scanned, removedLocks, removedSessions int
	for i, e := range entries {
		if ctx.Err() != nil {
			slog.Info("startup: cleanup scan cancelled", "scanned", scanned)
			break
		}
		if i >= cleanupMaxEntries {
			slog.Warn("startup: cleanup scan truncated",
				"scanned", cleanupMaxEntries, "total", len(entries))
			break
		}
		locks, sessions, counted := cleanupOneEntry(ctx, dir, e.Name())
		if counted {
			scanned++
		}
		removedLocks += locks
		removedSessions += sessions
	}
	if removedLocks > 0 || removedSessions > 0 {
		slog.Info("startup: cleaned stale sessions",
			"scanned", scanned,
			"locks_removed", removedLocks,
			"empty_sessions_removed", removedSessions)
	}
}

// cleanupOneEntry processes a single directory entry during CleanupStaleSessions.
// Returns (removedLocks, removedSessions, counted) where `counted` indicates
// whether the entry made it past the validSessionID gate and should count
// toward the scanned total. Context cancellation is checked between I/O
// operations so a slow filesystem cannot block shutdown.
func cleanupOneEntry(ctx context.Context, dir, name string) (removedLocks, removedSessions int, counted bool) {
	if filepath.Ext(name) != ".lock" {
		return 0, 0, false
	}
	id := strings.TrimSuffix(name, ".lock")
	if !validSessionID(id) {
		slog.Warn("startup: skipping lock with invalid id", "name", name)
		return 0, 0, false
	}
	counted = true
	lockPath := filepath.Join(dir, name)
	pid, ok := parseLockFile(lockPath)
	if !ok {
		return 0, 0, counted
	}
	if ctx.Err() != nil {
		return 0, 0, counted
	}
	if !isLockStale(lockPath, pid) {
		return 0, 0, counted // kiro-cli still holds it; keep
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
	// Dead-PID + empty jsonl → this session never made it past
	// its first event. Drop the companion .json too so the
	// directory listing stays consistent with "no zombie
	// sessions". Remove the jsonl first: if we crash between
	// the two calls, the next boot sees the .json without a
	// .jsonl and handles it via the same path (the dead lock
	// is already gone, but a fresh stale lock from a retry
	// would drive the cleanup again).
	jsonlPath := filepath.Join(dir, id+".jsonl")
	info, statErr := os.Stat(jsonlPath)
	if statErr != nil || info.Size() != 0 {
		return removedLocks, 0, counted
	}
	if rmErr := os.Remove(jsonlPath); rmErr != nil {
		slog.Warn("startup: remove empty jsonl failed",
			"session", id, "error", rmErr)
		return removedLocks, 0, counted
	}
	jsonPath := filepath.Join(dir, id+".json")
	if rmErr := os.Remove(jsonPath); rmErr != nil && !os.IsNotExist(rmErr) {
		slog.Warn("startup: remove companion json failed",
			"session", id, "error", rmErr)
		return removedLocks, 0, counted
	}
	removedSessions = 1
	return removedLocks, removedSessions, counted
}
