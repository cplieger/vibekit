package filebrowse

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"path/filepath"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/logsafe"
	"github.com/cplieger/webhttp/v2"
)

// --- /api/file/upload (POST multipart into a target directory) ---

func (h *Handler) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	// gosec:G120 is a false positive: r.Body is capped by MaxBytesReader
	// above, so ParseMultipartForm can't cause memory exhaustion here.
	if err := r.ParseMultipartForm(multipartMaxMemory); err != nil { //nolint:gosec // G120: size bounded by nginx proxy
		// Split the error classes so clients can distinguish "too big,
		// retry smaller" (413) from "invalid multipart" (400).
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("filebrowse: upload too large",
				"limit", maxUploadSize, "error", err)
			webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				httpreply.ErrorJSON("upload too large"))
		} else if errors.Is(err, context.Canceled) {
			slog.Debug("filebrowse: upload cancelled by client")
		} else {
			slog.Warn("filebrowse: upload form parse failed", "error", err)
			httpreply.BadRequest(w, "invalid multipart form")
		}
		return
	}
	dir := cmp.Or(r.FormValue("dir"), defaultUploadDir)
	dirLoc, ok := h.resolveOrForbid(w, dir)
	if !ok {
		return
	}
	// Upload-target-directory gate: without this, an agent-triggered
	// upload with dir=/config would silently land files inside the
	// sensitive container. IsSensitive on the final per-file path (in
	// writeUploads) is the second layer.
	if isProtectedDir(dirLoc.abs) {
		slog.Warn("filebrowse: upload blocked on protected dir", "dir", dirLoc.abs)
		httpreply.Forbidden(w, "upload target is protected")
		return
	}
	if err := dirLoc.m.root.MkdirAll(dirLoc.rel(), 0o755); err != nil {
		slog.Warn("filebrowse: upload mkdir failed", "path", dirLoc.abs, "error", err)
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
			httpreply.ErrorJSON("upload failed"))
		return
	}
	formFiles := r.MultipartForm.File["files"]
	if len(formFiles) == 0 {
		httpreply.BadRequest(w, "no files")
		return
	}
	uploaded, totalBytes, err := writeUploads(r.Context(), dirLoc, formFiles)
	if err != nil {
		respondUploadError(w, dirLoc.abs, uploaded, err)
		return
	}
	slog.Info("filebrowse: upload",
		"dir", dirLoc.abs, "count", len(uploaded), "bytes", totalBytes)
	webhttp.WriteJSON(w, map[string]any{"ok": true, "uploaded": uploaded})
}

// respondUploadError maps a writeUploads failure to its HTTP response: an
// invalid filename is the client's fault (400); a single file crossing the
// per-file cap is rejected loudly with a 413, never silently truncated;
// anything else is a 500.
//
// Every body carries the names that DID land, because a partially-failed
// batch is not rolled back: each file is whole or absent, but the batch is
// not atomic (an upload may overwrite, so undoing one needs a backup of
// every destination, and the rollback can itself fail halfway). The 400
// branch goes through WriteJSONStatus rather than httpreply.BadRequest
// because that helper cannot carry the uploaded-files key.
func respondUploadError(w http.ResponseWriter, dir string, uploaded []string, err error) {
	if errors.Is(err, errInvalidFilename) {
		webhttp.WriteJSONStatus(w, http.StatusBadRequest,
			uploadErrorJSON(err.Error(), uploaded))
		return
	}
	if errors.Is(err, atomicfile.ErrFileTooLarge) {
		slog.Warn("filebrowse: upload too large",
			"limit", maxUploadSize, "uploaded", len(uploaded), "error", logsafe.Field(err.Error()))
		webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
			uploadErrorJSON("upload too large", uploaded))
		return
	}
	slog.Warn("filebrowse: upload write failed",
		"dir", dir, "uploaded", len(uploaded), "error", logsafe.Field(err.Error()))
	webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
		uploadErrorJSON("upload failed", uploaded))
}

// uploadErrorJSON is the error body every upload failure shares: the reason
// plus the files already written. uploaded is always a non-nil slice so the
// key encodes as [] rather than null and the client needs no null check.
func uploadErrorJSON(msg string, uploaded []string) map[string]any {
	if uploaded == nil {
		uploaded = []string{}
	}
	return map[string]any{"error": msg, "uploaded": uploaded}
}

// errInvalidFilename is surfaced as a 400 to the client. Raised on
// "." / ".." / empty-after-Base filenames so silent-skip doesn't
// produce a confusing `uploaded: []` subset response.
var errInvalidFilename = errors.New("invalid filename")

// writeUploads copies each multipart file into the target directory
// atomically via write-temp-then-rename, returning the list of filenames
// written plus total bytes. On error, the partial temp file is removed;
// files written earlier in the batch remain on disk, and their names ride
// the error response. The context lets a client disconnect abort the
// remaining files in a batch upload.
func writeUploads(ctx context.Context, dirLoc loc, files []*multipart.FileHeader) (uploaded []string, total int64, err error) {
	uploaded = make([]string, 0, len(files))
	for _, fh := range files {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return uploaded, total, ctxErr
		}
		name := filepath.Base(fh.Filename)
		if name == "" || name == "." || name == ".." {
			slog.Warn("filebrowse: upload rejected: invalid filename",
				"raw_name", logsafe.Field(fh.Filename), "base", logsafe.Field(name), "target_dir", logsafe.Field(dirLoc.abs))
			return uploaded, total, fmt.Errorf("%w: %q", errInvalidFilename, fh.Filename)
		}
		dest := filepath.Join(dirLoc.abs, name)
		// Per-file sensitive-path gate: isProtectedDir on the target
		// directory catches container-level drops, this blocks file-level
		// overwrites of sensitive exact-match entries when the target
		// directory itself is not sensitive.
		if IsSensitive(dest) {
			slog.Warn("filebrowse: upload rejected: sensitive dest",
				"raw_name", logsafe.Field(fh.Filename), "dest", logsafe.Field(dest))
			return uploaded, total, fmt.Errorf("%w: %q (protected)", errInvalidFilename, fh.Filename)
		}
		n, wErr := writeOneUpload(ctx, loc{m: dirLoc.m, abs: dest}, fh)
		if wErr != nil {
			return uploaded, total, wErr
		}
		uploaded = append(uploaded, name)
		total += n
	}
	return uploaded, total, nil
}

// writeOneUpload streams fh into a temp file inside the handler's *os.Root,
// fsyncs it, then renames it over dest — kernel-confined to the root. On any
// error the temp is removed so a partial write never surfaces under the
// user's expected filename. ctx lets a client disconnect abort the copy
// mid-stream.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func writeOneUpload(ctx context.Context, dest loc, fh *multipart.FileHeader) (n int64, err error) {
	src, err := fh.Open()
	if err != nil {
		return 0, err
	}
	defer func() { _ = src.Close() }()

	// Abort at the next chunk boundary on context cancel. The per-file cap
	// is enforced by WithMaxBytes below, which REJECTS an over-cap file
	// rather than silently truncating it.
	cr := &countingReader{r: &ctxReader{ctx: ctx, r: src}}
	// WriteReaderInRoot stages a temp inside the mount's root, fsyncs it,
	// renames over the root-relative dest, then fsyncs the parent dir,
	// staying kernel-confined to the mount. It refuses a symlink dest and
	// removes the temp on any error.
	if _, werr := atomicfile.WriteReaderInRoot(ctx, dest.m.root, dest.rel(), cr,
		atomicfile.WithMode(0o600), atomicfile.WithMaxBytes(maxUploadSize)); werr != nil {
		return cr.n, werr
	}
	return cr.n, nil
}
