package composition

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// acquireInstanceLock takes a non-blocking exclusive flock on
// <configDir>/vibekit.lock. If another vibekit process already holds
// the lock, returns an error. The lock is held for the lifetime of
// the process (the fd is intentionally never closed); the kernel
// releases it automatically on exit, including crash/SIGKILL.
func acquireInstanceLock(configDir string) error {
	path := filepath.Join(configDir, "vibekit.lock")
	// O_CREATE|O_RDWR (no O_TRUNC, no O_APPEND): we never write payload
	// to the lock file, only use its fd as the flock target. If file
	// existed from a prior run, we keep its (empty) contents.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	// LOCK_EX|LOCK_NB: exclusive + non-blocking. Fails immediately
	// with EWOULDBLOCK if another process holds the lock.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Close-error-on-error-path: lock acquisition already failed,
		// the fd carries no buffered writes (we only opened it), so a
		// close error here cannot lose data. Discard explicitly to
		// satisfy CodeQL's go/unhandled-writable-file-close rule.
		_ = f.Close()
		return fmt.Errorf("flock: %w", err)
	}
	// Intentionally do NOT close f — the lock is held by the open fd.
	// The kernel releases it when the process exits.
	return nil
}
