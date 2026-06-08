package filehandler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/cplieger/vibekit/internal/api"
)

const (
	errFileTooLarge = "file too large"
	errReadFailed   = "read failed"
)

// --- /api/file (GET read) + /api/file/download ---

func readFile(ctx context.Context, w http.ResponseWriter, resolved, reqPath string, h *Handler) {
	// Open+Stat(fd)+LimitReader eliminates the TOCTOU between a
	// separate os.Stat size check and os.ReadFile: the size guard
	// and the read operate on the same file descriptor.
	f, err := h.root.Open(h.relPath(resolved))
	if err != nil {
		if os.IsNotExist(err) {
			api.NotFound(w, "not found")
			return
		}
		slog.Warn("filehandler: open failed", "path", resolved, "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			api.ErrorJSON(errReadFailed))
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		slog.Warn("filehandler: stat failed", "path", resolved, "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			api.ErrorJSON(errReadFailed))
		return
	}
	if info.IsDir() {
		api.BadRequest(w, "path is a directory")
		return
	}
	if info.Size() > maxFileSize {
		api.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
			api.ErrorJSON(errFileTooLarge))
		return
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		slog.Warn("filehandler: read failed", "path", resolved, "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			api.ErrorJSON(errReadFailed))
		return
	}
	if int64(len(data)) > maxFileSize {
		api.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
			api.ErrorJSON(errFileTooLarge))
		return
	}
	if looksBinary(data) {
		api.WriteJSONStatus(w, http.StatusUnsupportedMediaType,
			api.ErrorJSON("binary file"))
		return
	}
	api.WriteJSON(w, map[string]string{"content": string(data), "path": reqPath})
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
		api.MethodNotAllowed(w)
		return
	}
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		api.BadRequest(w, "missing path")
		return
	}
	resolved := resolveOrForbid(w, reqPath)
	if resolved == "" {
		return
	}
	info, err := h.root.Stat(h.relPath(resolved))
	if err != nil {
		if os.IsNotExist(err) {
			api.NotFound(w, "not found")
			return
		}
		slog.Warn("filehandler: download stat failed", "path", resolved, "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			api.ErrorJSON(errReadFailed))
		return
	}
	if info.IsDir() {
		api.BadRequest(w, "cannot download directory")
		return
	}
	if info.Size() > maxCopySize {
		api.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
			api.ErrorJSON("file too large to download"))
		return
	}
	name := filepath.Base(resolved)
	ct := mime.TypeByExtension(filepath.Ext(name))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, name))
	slog.Info("filehandler: download", "path", resolved, "size", info.Size())
	http.ServeFile(w, r, resolved) //nolint:gosec // G703: path validated against workspace root
}
