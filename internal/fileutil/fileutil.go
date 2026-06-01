package fileutil

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// --- File IO ---

// SaveBytes writes raw bytes to path atomically via write-temp-then-
// rename. The temp file is fsynced before rename; the parent directory
// is fsynced after for rename durability on ext4 data=ordered. Parent
// directory permissions are 0o755 for world-readable targets, 0o700
// for private-only.
//
// The caller is responsible for ensuring `path` is constructed from
// trusted values (not user input). SaveBytes does not perform symlink
// resolution or workspace-root containment; if the target is a symlink,
// os.Rename replaces it rather than following it, but the parent
// directory is trusted verbatim.
func SaveBytes(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	dirPerm := os.FileMode(0o755)
	if perm&0o077 == 0 {
		// Private file (no group/world bits) → private parent dir.
		dirPerm = 0o700
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return err
	}
	tmpName, err := writeTempFile(dir, filepath.Base(path), data, perm)
	if err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	fsyncDir(dir)
	return nil
}

// fsyncDir best-effort fsyncs dir for rename durability on ext4
// data=ordered. Failures are logged but not returned: the file data
// itself is already durable (the temp file was fsynced before the
// rename), so the caller's write is complete. The parent-dir fsync is
// only needed to survive crashes on some filesystems.
func fsyncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		slog.Warn("fileutil: parent dir open for fsync failed",
			"dir", dir, "error", err)
		return
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		slog.Warn("fileutil: parent dir fsync failed",
			"dir", dir, "error", err)
	}
}

// writeTempFile creates a sibling temp file in dir, writes data,
// fsyncs, closes, and chmods to perm. Returns the temp file's name on
// success; removes the temp file and returns the error on any failure.
func writeTempFile(dir, baseName string, data []byte, perm os.FileMode) (name string, err error) {
	tmp, err := os.CreateTemp(dir, baseName+".tmp-*")
	if err != nil {
		return "", err
	}
	name = tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(name)
			name = ""
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return
	}
	if err = tmp.Close(); err != nil {
		return
	}
	if err = os.Chmod(name, perm); err != nil {
		return
	}
	return
}

// largeSaveJSONThreshold is the marshalled-payload size above which
// SaveJSON emits a Warn log (but still proceeds).
const largeSaveJSONThreshold = 16 << 20 // 16 MiB

// SaveJSON marshals v to indented JSON (json.MarshalIndent, two-space
// indent) and writes it to path atomically via SaveBytes. The mutex is
// held across marshal, atomic write, and rename so concurrent callers
// never interleave partial writes or compete for the temp-file slot.
// mu must not be nil.
//
// label identifies the caller in slog error output; use a short stable
// string like "serveJSONFile:tools" or "chats/<id>" so log aggregators
// can group failures by call site.
func SaveJSON(path string, mu *sync.Mutex, v any, label string, perm os.FileMode) error {
	if mu == nil {
		return errors.New("fileutil.SaveJSON: nil mutex")
	}
	mu.Lock()
	defer mu.Unlock()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		slog.Error("fileutil.SaveJSON: marshal failed", "label", label, "error", err)
		return err
	}
	if len(data) > largeSaveJSONThreshold {
		slog.Warn("fileutil.SaveJSON: large payload",
			"label", label, "bytes", len(data),
			"threshold", largeSaveJSONThreshold)
	}
	if err := SaveBytes(path, data, perm); err != nil {
		slog.Error("fileutil.SaveJSON: write failed", "label", label, "path", path, "error", err)
		return err
	}
	return nil
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

// CleanupStaleTemps removes sibling "*.tmp-*" files left by crashes of
// SaveBytes/SaveJSON. Safe to call concurrently with new writes: only
// files whose mtime is older than maxAge are removed. Errors are logged
// at Warn but not returned; best-effort. Missing dir is not an error.
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
