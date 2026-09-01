package filebrowse

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/logsafe"
	"github.com/cplieger/webhttp/v2"
)

// errStaleWrite is the client sentinel for a refused stale write. Named so the
// editor can branch on it rather than matching prose.
const errStaleWrite = "file changed on disk since you opened it"

// writeBody is the PUT /api/file payload.
type writeBody struct {
	Content string `json:"content"`
	// ExpectedHash is the content_hash the client received when it LOADED the
	// file. Optional: a caller that omits it gets the previous
	// write-unconditionally behaviour, which keeps every non-editor writer
	// (and any older client) working.
	ExpectedHash string `json:"expected_hash"`
}

func writeFile(w http.ResponseWriter, r *http.Request, l loc) {
	webhttp.LimitBody(w, r, maxFileSize)
	var body writeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("filebrowse: write body too large",
				"path", logsafe.Field(l.abs), "limit", maxFileSize, "error", logsafe.Field(maxErr.Error()))
			webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				httpreply.ErrorJSON(errFileTooLarge))
			return
		}
		httpreply.BadRequest(w, "invalid json")
		return
	}
	// Pre-stat so the user sees a clean 400 for "can't write onto a
	// directory" rather than a generic 500 with the raw EISDIR text
	// (which would leak the resolved filesystem path).
	if info, err := l.m.root.Stat(l.rel()); err == nil && info.IsDir() {
		httpreply.BadRequest(w, "path is a directory")
		return
	}
	if !staleWriteAllowed(w, r, l, body) {
		return
	}
	// Preserve an existing regular file's permission bits: a temp-then-rename
	// sets the mode on the temp, so the mode has to be carried across
	// explicitly or every save would flatten a 0o755 script to 0o644. Only a
	// regular file's bits are read.
	mode := os.FileMode(0o644)
	if info, err := l.m.root.Lstat(l.rel()); err == nil && info.Mode().IsRegular() {
		mode = info.Mode().Perm()
	}
	// One confined atomic write, the same primitive the upload path uses.
	//
	// The open this replaced carried syscall.O_NOFOLLOW, but that flag was
	// INERT: os.Root.OpenFile ORs O_NOFOLLOW in itself and re-resolves the
	// link on the resulting ELOOP (go1.27.0, src/os/root_unix.go:85-101), so
	// a caller-supplied one is silently ignored — and every sensitive path
	// this handler protects lives INSIDE the /config mount, so an in-mount
	// symlink is exactly what the root permits.
	//
	// A temp-then-rename closes it without needing a flag the root will not
	// honour: atomicfile refuses a symlink at the target up front, and even
	// a lost race only replaces the LINK (rename(2) does not follow a final
	// component). It also adds the fsync this write never had.
	if _, err := atomicfile.WriteFileInRoot(r.Context(), l.m.root, l.rel(),
		[]byte(body.Content), atomicfile.WithMode(mode)); err != nil {
		if errors.Is(err, atomicfile.ErrSymlinkTarget) || errors.Is(err, atomicfile.ErrNotRegular) {
			slog.Warn("filebrowse: refused a write onto a non-regular target",
				"path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
			httpreply.BadRequest(w, "not a regular file")
			return
		}
		slog.Warn("filebrowse: write failed", "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
			httpreply.ErrorJSON("write failed"))
		return
	}
	slog.Info("filebrowse: file written", "path", logsafe.Field(l.abs), "bytes", len(body.Content))
	webhttp.Ok(w)
}

// staleWriteAllowed is the stale-write guard: it reports whether the write may
// proceed, having written the 409 (or a 500) itself when it may not. The
// editor's file and the agent's file are the same file, so "changed since
// you loaded it" is the normal case here, not an edge.
//
// Deliberately NOT locked: a read-hash-compare-then-write is racy against a
// write landing in the microseconds between, but closing that would need
// cross-process locking, which this repo declined (the single server owns
// the directory and persists atomically).
//
// An absent file is not stale: a caller may legitimately be re-creating
// something deleted since it loaded.
func staleWriteAllowed(w http.ResponseWriter, r *http.Request, l loc, body writeBody) bool {
	if body.ExpectedHash == "" {
		return true
	}
	current, err := atomicfile.ReadBoundedInRoot(r.Context(), l.m.root, l.rel(), maxFileSize)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		slog.Warn("filebrowse: stale-check read failed", "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError, httpreply.ErrorJSON("write failed"))
		return false
	}
	got := contentHash(current)
	if got == body.ExpectedHash {
		return true
	}
	slog.Info("filebrowse: refused a stale write",
		"path", logsafe.Field(l.abs), "expected", logsafe.Field(body.ExpectedHash), "actual", got)
	// The current content rides the 409 so the client can show what changed
	// instead of asking the user to reload and compare by eye.
	webhttp.WriteJSONStatus(w, http.StatusConflict, map[string]string{
		"error":         errStaleWrite,
		"content":       string(current),
		"content_hash":  got,
		"expected_hash": body.ExpectedHash,
	})
	return false
}
