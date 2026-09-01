package filebrowse

import (
	"bytes"
	"cmp"
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
	"strconv"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/logsafe"
	"github.com/cplieger/webhttp/v2"
)

const (
	errFileTooLarge = "file too large"
	errReadFailed   = "read failed"
)

// --- /api/file (GET read) + /api/file/download ---

func readFile(ctx context.Context, w http.ResponseWriter, l loc, reqPath string) {
	// atomicfile.ReadBoundedInRoot owns the confined bounded read: it opens
	// through the root, stats the OPEN HANDLE, requires a regular file, and
	// opens non-blocking so a FIFO under a granted root cannot wedge the
	// handler.
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
	// content_hash is the client's handle on "the bytes I loaded"; it comes
	// back on save as expected_hash, turning a blind overwrite into a
	// detected conflict.
	//
	// A DIGEST rather than the mtime, deliberately: Linux stamps inode
	// timestamps from a coarse clock, so two writes inside one tick are
	// byte-identical in mtime and an mtime-based guard would miss exactly
	// the rapid agent write it exists to catch.
	webhttp.WriteJSON(w, map[string]string{
		"content":      string(data),
		"content_hash": contentHash(data),
		respPath:       reqPath,
	})
}

// contentHash is the stale-write guard's comparison key: a hex SHA-256 of
// the served bytes.
func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// looksBinary reports whether the file's first binarySniffN bytes contain a
// NUL. bytes.IndexByte is used directly to hit the runtime's SIMD
// implementation on amd64/arm64.
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
	// Open through the mount's os.Root rather than http.ServeFile, which
	// re-opens by path and follows symlinks at serve time — reintroducing
	// the check-to-serve TOCTOU resolvePath's EvalSymlinks otherwise closes.
	f, err := l.m.root.Open(l.rel())
	if err != nil {
		if os.IsNotExist(err) {
			httpreply.NotFound(w, "not found")
			return
		}
		slog.Warn("filebrowse: download open failed", "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
			httpreply.ErrorJSON(errReadFailed))
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		slog.Warn("filebrowse: download stat failed", "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
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
	ct := cmp.Or(mime.TypeByExtension(filepath.Ext(name)), "application/octet-stream")
	w.Header().Set("Content-Type", ct)
	// `attachment` is a SECURITY CONTROL here, not a UX preference: an SVG
	// served inline (mime.TypeByExtension(".svg") is "image/svg+xml") would
	// execute script if navigated to as a document, and CSP does not close
	// this — `frame-src` falls back to `default-src 'self'`, which permits
	// a same-origin frame. Do not relax to `inline` for images: that turns
	// the existing download anchor into stored XSS.
	// TestHandleDownload_SVGIsAttachment pins it.
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, name))
	// Revalidate on every impression: a validator with no explicit lifetime
	// gets heuristic caching whose window scales with the document's age,
	// which bites the agent's screenshot loop where a re-shot frame keeps
	// its filename (see utils-url.ts rewriteWorkspaceImageSrc). `no-cache`
	// requires revalidation rather than forbidding caching.
	w.Header().Set("Cache-Control", "no-cache")
	// A strong validator: Last-Modified's HTTP-date truncates to ONE SECOND,
	// so two writes within a second answer 304 with stale bytes (see
	// content_hash above for the same mtime-resolution problem). Size plus
	// mtime-in-nanoseconds shrinks that window to one clock tick at zero I/O
	// cost. The QUOTING is load-bearing — an unquoted value is not a valid
	// strong validator and ServeContent falls back to the mtime. Not a
	// content hash: this handler streams from an open fd and supports
	// Range, so hashing means a second full read per impression.
	w.Header().Set("ETag", strconv.Quote(fmt.Sprintf("%x-%x", info.Size(), info.ModTime().UnixNano())))
	// Debug (not Info): the resolved path can name a workspace file and
	// this line ships to Loki. http.ServeContent serves from the already-
	// open, confined fd (handles Range requests + conditional headers too).
	slog.Debug("filebrowse: download", "path", logsafe.Field(l.abs), "size", info.Size())
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// readFileError maps a confined-read failure onto the HTTP status the
// client needs. atomicfile's sentinels distinguish the cases without
// inspecting error text; a cancelled request is silent since the client
// is gone.
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
		slog.Warn("filebrowse: read failed", "path", logsafe.Field(l.abs), "error", logsafe.Field(err.Error()))
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError, httpreply.ErrorJSON(errReadFailed))
	}
}
