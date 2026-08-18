package filebrowse

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"syscall"

	"github.com/cplieger/atomicfile/v2"
	"github.com/cplieger/vibekit/internal/api"
)

// errStaleWrite is the client sentinel for a refused stale write. Named so the
// editor can branch on it rather than matching prose.
const errStaleWrite = "file changed on disk since you opened it"

func writeFile(w http.ResponseWriter, r *http.Request, l loc) {
	api.LimitBody(w, r, maxFileSize)
	var body struct {
		Content string `json:"content"`
		// ExpectedHash is the content_hash the client received when it LOADED the
		// file. Optional: a caller that omits it gets the previous
		// write-unconditionally behaviour, which keeps every non-editor writer
		// (and any older client) working.
		ExpectedHash string `json:"expected_hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("filebrowse: write body too large",
				"path", l.abs, "limit", maxFileSize, "error", maxErr)
			api.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				api.ErrorJSON(errFileTooLarge))
			return
		}
		api.BadRequest(w, "invalid json")
		return
	}
	// Pre-stat so the user sees a clean 400 for "can't write onto a
	// directory" rather than a generic 500 with the raw EISDIR text
	// (which would leak the resolved filesystem path).
	if info, err := l.m.root.Stat(l.rel()); err == nil && info.IsDir() {
		api.BadRequest(w, "path is a directory")
		return
	}
	// The stale-write guard. The editor's file and the agent's file are the same
	// file, so "changed since you loaded it" is the normal case here, not an edge:
	// without this, opening a file, letting the agent edit it, and saving
	// silently discards the agent's work with no trace.
	//
	// Deliberately NOT locked. A read-hash-compare-then-write is racy against a
	// write landing in the microseconds between, and closing that would need
	// cross-process locking, which this repo declined for a recorded reason (the
	// single server owns the directory and persists atomically). The realistic
	// case is an agent write seconds earlier, which this catches; the
	// microsecond race is not worth a lock the rest of the design avoids.
	if body.ExpectedHash != "" {
		current, rErr := atomicfile.ReadBoundedInRoot(r.Context(), l.m.root, l.rel(), maxFileSize)
		switch {
		case rErr != nil && !errors.Is(rErr, fs.ErrNotExist):
			slog.Warn("filebrowse: stale-check read failed", "path", l.abs, "error", rErr)
			api.WriteJSONStatus(w, http.StatusInternalServerError, api.ErrorJSON("write failed"))
			return
		case rErr == nil:
			if got := contentHash(current); got != body.ExpectedHash {
				slog.Info("filebrowse: refused a stale write",
					"path", l.abs, "expected", body.ExpectedHash, "actual", got)
				// The current content rides the 409 so the client can show what
				// changed instead of asking the user to reload and compare by eye.
				api.WriteJSONStatus(w, http.StatusConflict, map[string]string{
					"error":         errStaleWrite,
					"content":       string(current),
					"content_hash":  got,
					"expected_hash": body.ExpectedHash,
				})
				return
			}
		}
	}
	// Local helper: every 500 branch below shares the same client
	// sentinel ("write failed") while preserving the stage-specific
	// log label for operator diagnosis. Collapses three parallel 5-line
	// blocks into three 1-line calls.
	fail := func(stage string, err error) {
		slog.Warn("filebrowse: "+stage, "path", l.abs, "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			api.ErrorJSON("write failed"))
	}
	// O_NOFOLLOW on the write prevents a dangling symlink planted
	// between resolvePath's EvalSymlinks and this open from steering
	// the write into a sensitive path. Matches actionTouch and the
	// copy/upload destinations; closes the same bypass path the
	// package-doc "Defense layers" section commits us to block.
	f, err := l.m.root.OpenFile(l.rel(),
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		fail("write open failed", err)
		return
	}
	if _, err := f.WriteString(body.Content); err != nil {
		_ = f.Close()
		fail("write failed", err)
		return
	}
	if err := f.Close(); err != nil {
		fail("write close failed", err)
		return
	}
	slog.Info("filebrowse: file written", "path", l.abs, "bytes", len(body.Content))
	api.Ok(w)
}
