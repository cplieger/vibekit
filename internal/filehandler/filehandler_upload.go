package filehandler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"path/filepath"

	"github.com/cplieger/atomicfile/v2"
	"github.com/cplieger/vibekit/internal/api"
)

// --- /api/file/upload (POST multipart into a target directory) ---

func (h *Handler) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.MethodNotAllowed(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	// gosec:G120 is a false positive: r.Body is capped by MaxBytesReader
	// above, so ParseMultipartForm can't cause memory exhaustion here.
	if err := r.ParseMultipartForm(multipartMaxMemory); err != nil { //nolint:gosec // G120: size bounded by nginx proxy
		// Split the error classes so clients can distinguish
		// "too big, retry smaller" (413) from "invalid multipart"
		// (400). Client disconnects during upload are dropped at
		// Debug — there's nothing to respond to.
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("filehandler: upload too large",
				"limit", maxUploadSize, "error", err)
			api.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				api.ErrorJSON("upload too large"))
		} else if errors.Is(err, context.Canceled) {
			slog.Debug("filehandler: upload cancelled by client")
		} else {
			slog.Warn("filehandler: upload form parse failed", "error", err)
			api.BadRequest(w, "invalid multipart form")
		}
		return
	}
	dir := r.FormValue("dir")
	if dir == "" {
		dir = "workspace"
	}
	resolvedDir := resolveOrForbid(w, dir)
	if resolvedDir == "" {
		return
	}
	// Upload-target-directory gate. The package-doc "Defense layers"
	// section commits to running isProtectedDir on directory targets;
	// without this check an agent-triggered upload with dir=/config
	// would silently land files (including overwrites of push-subs.json
	// / vapid-keys.json) inside the sensitive container. isSensitive on
	// the final per-file path (in writeUploads below) is the second
	// layer; this gate is the first.
	if isProtectedDir(resolvedDir) {
		slog.Warn("filehandler: upload blocked on protected dir", "dir", resolvedDir)
		api.Forbidden(w, "upload target is protected")
		return
	}
	if err := h.root.MkdirAll(h.relPath(resolvedDir), 0o755); err != nil {
		slog.Warn("filehandler: upload mkdir failed", "path", resolvedDir, "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			api.ErrorJSON("upload failed"))
		return
	}
	formFiles := r.MultipartForm.File["files"]
	if len(formFiles) == 0 {
		api.BadRequest(w, "no files")
		return
	}
	uploaded, totalBytes, err := h.writeUploads(r.Context(), resolvedDir, formFiles)
	if err != nil {
		if errors.Is(err, errInvalidFilename) {
			api.BadRequest(w, err.Error())
			return
		}
		slog.Warn("filehandler: upload write failed",
			"dir", resolvedDir, "uploaded", len(uploaded), "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			api.ErrorJSON("upload failed"))
		return
	}
	slog.Info("filehandler: upload",
		"dir", resolvedDir, "count", len(uploaded), "bytes", totalBytes)
	api.WriteJSON(w, map[string]any{"ok": true, "uploaded": uploaded})
}

// errInvalidFilename is surfaced as a 400 to the client. Raised on
// "." / ".." / empty-after-Base filenames so silent-skip doesn't
// produce a confusing `uploaded: []` subset response.
var errInvalidFilename = errors.New("invalid filename")

// writeUploads copies each multipart file into targetDir atomically
// via write-temp-then-rename, returning the list of filenames written
// plus total bytes for observability. On error, the partial temp file
// is removed; files written earlier in the batch remain on disk (the
// HTTP response conveys an error anyway). The context lets a client
// disconnect abort the remaining files in a batch upload.
func (h *Handler) writeUploads(ctx context.Context, targetDir string, files []*multipart.FileHeader) (uploaded []string, total int64, err error) {
	uploaded = make([]string, 0, len(files))
	for _, fh := range files {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return uploaded, total, ctxErr
		}
		name := filepath.Base(fh.Filename)
		if name == "" || name == "." || name == ".." {
			slog.Warn("filehandler: upload rejected: invalid filename",
				"raw_name", fh.Filename, "base", name, "target_dir", targetDir)
			return uploaded, total, fmt.Errorf("%w: %q", errInvalidFilename, fh.Filename)
		}
		dest := filepath.Join(targetDir, name)
		// Per-file sensitive-path gate. isProtectedDir on the target
		// directory (in handleUpload) catches container-level drops;
		// this check blocks file-level overwrites of sensitive exact-
		// match entries like /config/push-subs.json when targetDir
		// is a non-sensitive parent that enclosures the sensitive
		// file. Mirrors the rename-destination check in actionRename.
		if isSensitive(dest) {
			slog.Warn("filehandler: upload rejected: sensitive dest",
				"raw_name", fh.Filename, "dest", dest)
			return uploaded, total, fmt.Errorf("%w: %q (protected)", errInvalidFilename, fh.Filename)
		}
		n, wErr := h.writeOneUpload(ctx, dest, fh)
		if wErr != nil {
			return uploaded, total, wErr
		}
		uploaded = append(uploaded, name)
		total += n
	}
	return uploaded, total, nil
}

// writeOneUpload streams fh into a temp file inside the handler's *os.Root,
// fsyncs it, then renames it over dest — kernel-confined to the root like every
// other write in this handler. On any error the temp is removed so a partial
// write never surfaces under the user's expected filename. Returns the number
// of bytes written. ctx lets a client disconnect abort the copy mid-stream
// (the same guarantee actionCopy provides). countingReader tallies bytes read
// so the uploaded size is reported even though atomicfile performs the copy.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (h *Handler) writeOneUpload(ctx context.Context, dest string, fh *multipart.FileHeader) (n int64, err error) {
	src, err := fh.Open()
	if err != nil {
		return 0, err
	}
	defer func() { _ = src.Close() }()

	// Cap the per-file copy (one malformed part can't blow past the global
	// multipart budget) and abort at the next chunk boundary on context cancel.
	cr := &countingReader{r: &ctxReader{ctx: ctx, r: io.LimitReader(src, maxUploadSize)}}
	// WriteReaderInRoot stages a temp inside h.root, fsyncs it, renames over the
	// root-relative dest, then fsyncs the parent dir — the durability step the
	// old hand-rolled path omitted — while keeping the write kernel-confined to
	// the root like the rest of this handler (a symlink planted in the tree
	// cannot redirect it outside). It refuses a symlink dest and removes the
	// temp on any error, so dest is never left a partial write.
	if _, werr := atomicfile.WriteReaderInRoot(ctx, h.root, h.relPath(dest), cr, atomicfile.WithMode(0o600)); werr != nil {
		return cr.n, werr
	}
	return cr.n, nil
}
