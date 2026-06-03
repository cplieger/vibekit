package fileutil

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/atomicfile"
)

// SaveBytes writes raw bytes to path atomically via the atomicfile
// library (temp→fsync→rename→dir-fsync). Thin wrapper that preserves
// the original call-site signature.
func SaveBytes(path string, data []byte, perm os.FileMode) error {
	return atomicfile.SaveBytes(path, data, perm)
}

// SaveJSON marshals v to indented JSON and writes it atomically via
// atomicfile.SaveJSON. Thin wrapper preserving the original signature.
func SaveJSON(path string, mu *sync.Mutex, v any, label string, perm os.FileMode) error {
	return atomicfile.SaveJSON(path, mu, v, label, perm)
}

// isStaleTempName reports whether name matches one of the literal
// suffix patterns writeTempFile / writeOneUpload / actionCopy produce:
//   - "<base>.tmp-<rand>"    — SaveBytes/SaveJSON via os.CreateTemp
//   - "<base>.upload-<rand>" — filehandler.writeOneUpload multipart uploads
//   - "<base>.copy-<rand>"   — filehandler.actionCopy streaming copies
func isStaleTempName(name string) bool {
	for _, tag := range [...]string{".tmp-", ".upload-", ".copy-"} {
		i := strings.LastIndex(name, tag)
		if i < 0 || i+len(tag) >= len(name) {
			continue
		}
		tail := name[i+len(tag):]
		if !strings.ContainsAny(tail, "./\\") {
			return true
		}
	}
	return false
}

// CleanupStaleTemps removes sibling temp files left by crashes of
// SaveBytes/SaveJSON and upload/copy operations. Safe to call
// concurrently with new writes: only files whose mtime is older than
// maxAge are removed. Errors are logged at Warn but not returned;
// best-effort. Missing dir is not an error.
func CleanupStaleTemps(dir string, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("fileutil.CleanupStaleTemps: readdir failed",
				"dir", dir, "error", err)
		}
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		name := e.Name()
		if !isStaleTempName(name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				slog.Debug("fileutil.CleanupStaleTemps: stat failed, skipping",
					"dir", dir, "name", name, "error", err)
			}
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		full := filepath.Join(dir, name)
		if err := os.Remove(full); err != nil {
			slog.Warn("fileutil.CleanupStaleTemps: remove failed",
				"path", full, "error", err)
			continue
		}
		slog.Info("fileutil.CleanupStaleTemps: removed stale temp",
			"path", full, "age", time.Since(info.ModTime()))
	}
}

// IsGitRepo reports whether dir contains a .git entry (directory for
// regular repos, regular file for worktrees and submodules, or a
// symlink to either — os.Stat follows symlinks).
func IsGitRepo(ctx context.Context, dir string) bool {
	if ctx.Err() != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}
