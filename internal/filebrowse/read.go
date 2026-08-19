package filebrowse

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/webhttp"
)

const (
	errFileTooLarge = "file too large"
	errReadFailed   = "read failed"
)

// --- /api/file (GET read) + /api/file/download ---

func readFile(ctx context.Context, w http.ResponseWriter, l loc, reqPath string) {
	// atomicfile.ReadBoundedInRoot owns the confined bounded read: it opens through the
	// root, stats the OPEN HANDLE (so the size guard and the read cannot refer to
	// different files), requires a regular file, and opens non-blocking so a FIFO under a
	// granted root cannot wedge the handler. Hand-rolling that sequence here duplicated
	// three non-obvious details the library already guarantees on the write side, and one
	// of them getting it wrong is a confinement bypass.
	data, err := atomicfile.ReadBoundedInRoot(ctx, l.m.root, l.rel(), maxFileSize)
	if err != nil {
		readFileError(w, l, err)
		return
	}
	if looksBinary(data) {
		webhttp.WriteJSONStatus(w, http.StatusUnsupportedMediaType,
			httpreply.ErrorJSON("binary file"))
		return
	}
	// content_hash is the client's handle on "the bytes I loaded". It comes back
	// on save as expected_hash, which is what turns a blind overwrite into a
	// detected conflict — the file the user is editing is the same tree the agent
	// writes to, so an external change between load and save is routine here
	// rather than exotic.
	//
	// A DIGEST rather than the mtime, deliberately: this repo already measured
	// that Linux stamps inode timestamps from a coarse clock (jiffy granularity),
	// so two writes inside one tick are byte-identical in mtime and an
	// mtime-based guard would miss exactly the rapid agent write it exists to
	// catch. See kiro_docs.go's dirSignature for the same finding.
	webhttp.WriteJSON(w, map[string]string{
		"content":      string(data),
		"content_hash": contentHash(data),
		respPath:       reqPath,
	})
}

// contentHash is the stale-write guard's comparison key: a hex SHA-256 of the
// bytes served. Full hex rather than a truncation because the cost is one field
// on a response that already carries the whole file.
func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// looksBinary reports whether the file's first binarySniffN bytes contain
// a NUL. Not perfect but matches the pre-rewrite behaviour and is good
// enough to keep the editor from blindly loading images / executables.
// bytes.IndexByte is used directly because it hits the runtime's SIMD
// implementation on amd64/arm64 — binary sniffing is a bytes-package
// operation, not a generic slice one.
func looksBinary(data []byte) bool {
	if len(data) > binarySniffN {
		data = data[:binarySniffN]
	}
	return bytes.IndexByte(data, 0) >= 0
}

// --- /api/file/download (GET binary download with Content-Disposition) ---

func (h *Handler) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		httpreply.BadRequest(w, "missing path")
		return
	}
	l, ok := h.resolveOrForbid(w, reqPath)
	if !ok {
		return
	}
	// Open through the mount's os.Root (kernel-confined) rather than
	// serving the absolute path with http.ServeFile. ServeFile re-opens by
	// path and follows symlinks at serve time, which reintroduces the
	// check-to-serve TOCTOU that resolvePath's EvalSymlinks otherwise
	// closes: a symlink swapped in after resolution could point outside
	// the mount. Every other read/write goes through the root; download
	// does too.
	f, err := l.m.root.Open(l.rel())
	if err != nil {
		if os.IsNotExist(err) {
			httpreply.NotFound(w, "not found")
			return
		}
		slog.Warn("filebrowse: download open failed", "path", l.abs, "error", err)
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
			httpreply.ErrorJSON(errReadFailed))
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		slog.Warn("filebrowse: download stat failed", "path", l.abs, "error", err)
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
			httpreply.ErrorJSON(errReadFailed))
		return
	}
	if info.IsDir() {
		httpreply.BadRequest(w, "cannot download directory")
		return
	}
	if info.Size() > maxCopySize {
		webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
			httpreply.ErrorJSON("file too large to download"))
		return
	}
	name := filepath.Base(l.abs)
	ct := mime.TypeByExtension(filepath.Ext(name))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	// `attachment` is a SECURITY CONTROL on this route, not a UX preference, and
	// the case that makes it one is SVG: mime.TypeByExtension(".svg") is
	// "image/svg+xml", which a browser will execute script from if the response is
	// NAVIGATED to as a document. Every `<img src=…>` pointed here is safe on its
	// own (an SVG referenced as an image may not fetch or script), but the file
	// browser also offers a real same-origin anchor to this URL, and CSP does not
	// close the hole — `frame-src` falls back to `default-src 'self'`, which
	// PERMITS a same-origin frame. `attachment` is what makes the browser save the
	// bytes instead of rendering them as a document with this origin's privileges.
	// Do not relax it to `inline` for images: that turns the existing download
	// anchor into stored XSS. TestHandleDownload_SVGIsAttachment pins it.
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, name))
	// Revalidate on every impression. ServeContent supplies Last-Modified but
	// no freshness directive, and a validator with no explicit lifetime gets
	// HEURISTIC caching whose window scales with the document's age — so an
	// hour-old file is served stale for minutes, and the browser's in-page
	// image cache hands an already-decoded image to a new <img> with the same
	// src without a network round trip at all. Both bite the agent's
	// screenshot loop, where a re-shot frame keeps its filename and therefore
	// its URL (see utils-url.ts rewriteWorkspaceImageSrc). `no-cache` does not
	// forbid caching, it requires revalidation, so ServeContent's own
	// If-Modified-Since comparison answers 304 for an unchanged file and 200
	// with the new bytes for a rewritten one. Same policy the static asset
	// handler already uses.
	w.Header().Set("Cache-Control", "no-cache")
	// Debug (not Info): the resolved path can name a workspace file and
	// this line ships to Loki. http.ServeContent serves from the already-
	// open, confined fd (handles Range requests + conditional headers too).
	slog.Debug("filebrowse: download", "path", l.abs, "size", info.Size())
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// readFileError maps a confined-read failure onto the HTTP status the client needs.
//
// The mapping is why this is a switch rather than one 500: a missing file and an
// unreadable one are different answers to the caller, and a client cannot retry
// intelligently if both arrive as "internal error". atomicfile's sentinels make each case
// distinguishable without inspecting error text.
//
// ErrNotRegular covers a directory, FIFO, device node or socket. The previous code
// answered "path is a directory" for the directory case specifically; the message is now
// mode-agnostic because the library does not report WHICH non-regular type it refused,
// and inventing a guess would be worse than a truthful general answer. A cancelled
// request is silent — the client is gone.
func readFileError(w http.ResponseWriter, l loc, err error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return
	case errors.Is(err, fs.ErrNotExist):
		httpreply.NotFound(w, "not found")
	case errors.Is(err, atomicfile.ErrNotRegular):
		httpreply.BadRequest(w, "not a regular file")
	case errors.Is(err, atomicfile.ErrFileTooLarge):
		webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge, httpreply.ErrorJSON(errFileTooLarge))
	default:
		slog.Warn("filebrowse: read failed", "path", l.abs, "error", err)
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError, httpreply.ErrorJSON(errReadFailed))
	}
}
