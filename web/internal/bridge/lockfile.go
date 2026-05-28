package bridge

import (
	"context"

	"vibekit/internal/api"
	"vibekit/internal/sessions"
)

// LockManager manages kiro-cli session lock files. Construct via
// NewLockManager with the sessions directory path; pass the instance
// to Bridge or call its methods from the composition root.
type LockManager struct {
	mgr *sessions.Manager
}

// NewLockManager creates a LockManager that operates on the given
// sessions directory. An empty dir disables all operations.
func NewLockManager(dir string) *LockManager {
	return &LockManager{mgr: sessions.New(dir)}
}

// Dir returns the configured sessions directory.
func (lm *LockManager) Dir() string { return lm.mgr.Dir() }

// RemoveStaleLock removes the lock file for sessionID if the owning
// PID is dead. Delegates to the sessions package.
func (lm *LockManager) RemoveStaleLock(ctx context.Context, sessionID string) {
	lm.mgr.RemoveStaleLock(ctx, sessionID)
}

// CleanupStaleSessions removes stale kiro-cli lock files and empty
// session files on startup. Delegates to the sessions package.
func (lm *LockManager) CleanupStaleSessions(ctx context.Context) {
	lm.mgr.CleanupStale(ctx)
}

// validSessionID delegates to api.ValidSessionID.
func validSessionID(s string) bool {
	return api.ValidSessionID(s)
}
