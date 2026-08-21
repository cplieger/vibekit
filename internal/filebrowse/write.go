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
				"path", l.abs, "limit", maxFileSize, "error", maxErr)
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
	// Preserve an existing regular file's permission bits. The open this
	// replaced applied its mode argument only on CREATE, so an existing file
	// kept whatever it had; a temp-then-rename sets the mode on the temp, so
	// the mode has to be carried across explicitly or every save would flatten
	// a 0o755 script to 0o644. Only a regular file's bits are read: the
	// permissions of a symlink, FIFO or device node describe an object this
	// write refuses to replace.
	mode := os.FileMode(0o644)
	if info, err := l.m.root.Lstat(l.rel()); err == nil && info.Mode().IsRegular() {
		mode = info.Mode().Perm()
	}
	// One confined atomic write, the same primitive the upload path already
	// uses (writeOneUpload) and for the same reasons.
	//
	// The open it replaces carried syscall.O_NOFOLLOW with a comment claiming
	// the flag stopped a symlink planted between resolvePath's EvalSymlinks and
	// this open from steering the write into a sensitive path. That claim was
	// FALSE, and this package's own search_test.go already measured why:
	// os.Root.OpenFile ORs O_NOFOLLOW in itself and then re-resolves the link on
	// the resulting ELOOP, so a caller-supplied O_NOFOLLOW is silently ignored
	// (go1.27.0, src/os/root_unix.go:85-101). The exposure was not theoretical
	// either — every sensitive path this handler protects (mcp-secrets.json, the
	// chat store) lives INSIDE the /config mount, so an IN-mount symlink is
	// exactly what the root permits and exactly what IsSensitive can no longer
	// judge once the resolve has already happened.
	//
	// A temp-then-rename closes it without needing a flag the root will not
	// honour: atomicfile refuses a symlink at the target up front, and even a
	// lost race only replaces the LINK, because rename(2) does not follow a
	// final component. It also adds the fsync this write never had — the open it
	// replaces used O_TRUNC and never called Sync, so a crash mid-save left the
	// user's file truncated.
	if _, err := atomicfile.WriteFileInRoot(r.Context(), l.m.root, l.rel(),
		[]byte(body.Content), atomicfile.WithMode(mode)); err != nil {
		if errors.Is(err, atomicfile.ErrSymlinkTarget) || errors.Is(err, atomicfile.ErrNotRegular) {
			slog.Warn("filebrowse: refused a write onto a non-regular target",
				"path", l.abs, "error", err)
			httpreply.BadRequest(w, "not a regular file")
			return
		}
		slog.Warn("filebrowse: write failed", "path", l.abs, "error", err)
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
			httpreply.ErrorJSON("write failed"))
		return
	}
	slog.Info("filebrowse: file written", "path", l.abs, "bytes", len(body.Content))
	webhttp.Ok(w)
}

// staleWriteAllowed is the stale-write guard: it reports whether the write may
// proceed, having written the 409 (or a 500) itself when it may not.
//
// The editor's file and the agent's file are the same file, so "changed since you
// loaded it" is the normal case here, not an edge: without this, opening a file,
// letting the agent edit it, and saving silently discards the agent's work with
// no trace.
//
// Deliberately NOT locked. A read-hash-compare-then-write is racy against a write
// landing in the microseconds between, and closing that would need cross-process
// locking, which this repo declined for a recorded reason (the single server owns
// the directory and persists atomically). The realistic case is an agent write
// seconds earlier, which this catches; the microsecond race is not worth a lock
// the rest of the design avoids.
//
// An absent file is not stale: a caller may legitimately be re-creating something
// deleted since it loaded, and the hash it holds cannot be compared against
// nothing.
func staleWriteAllowed(w http.ResponseWriter, r *http.Request, l loc, body writeBody) bool {
	if body.ExpectedHash == "" {
		return true
	}
	current, err := atomicfile.ReadBoundedInRoot(r.Context(), l.m.root, l.rel(), maxFileSize)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		slog.Warn("filebrowse: stale-check read failed", "path", l.abs, "error", err)
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError, httpreply.ErrorJSON("write failed"))
		return false
	}
	got := contentHash(current)
	if got == body.ExpectedHash {
		return true
	}
	slog.Info("filebrowse: refused a stale write",
		"path", l.abs, "expected", body.ExpectedHash, "actual", got)
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
