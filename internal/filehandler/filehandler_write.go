package filehandler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"syscall"

	"github.com/cplieger/vibekit/internal/api"
)

func writeFile(w http.ResponseWriter, r *http.Request, l loc, observer WriteObserver) {
	api.LimitBody(w, r, maxFileSize)
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("filehandler: write body too large",
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
	// Local helper: every 500 branch below shares the same client
	// sentinel ("write failed") while preserving the stage-specific
	// log label for operator diagnosis. Collapses three parallel 5-line
	// blocks into three 1-line calls.
	fail := func(stage string, err error) {
		slog.Warn("filehandler: "+stage, "path", l.abs, "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			api.ErrorJSON("write failed"))
	}
	// Editor-save checkpoint capture fires BEFORE the write lands so
	// the pre-save content is still on disk to snapshot as the undo
	// target (see WriteObserver's contract for the ordering rationale
	// and the failed-write phantom tradeoff).
	if observer != nil {
		observer(r.Context(), l.abs, []byte(body.Content))
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
	slog.Info("filehandler: file written", "path", l.abs, "bytes", len(body.Content))
	api.Ok(w)
}
