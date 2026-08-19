package filebrowse

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
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/webhttp"
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
		// Split the error classes so clients can distinguish
		// "too big, retry smaller" (413) from "invalid multipart"
		// (400). Client disconnects during upload are dropped at
		// Debug — there's nothing to respond to.
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
	dir := r.FormValue("dir")
	if dir == "" {
		dir = defaultUploadDir
	}
	dirLoc, ok := h.resolveOrForbid(w, dir)
	if !ok {
		return
	}
	// Upload-target-directory gate. The package-doc "Defense layers"
	// section commits to running isProtectedDir on directory targets;
	// without this check an agent-triggered upload with dir=/config
	// would silently land files (including overwrites of push-subs.json
	// / vapid-keys.json) inside the sensitive container. IsSensitive on
	// the final per-file path (in writeUploads below) is the second
	// layer; this gate is the first.
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
// per-file cap is rejected loudly with a 413 (matching the whole-body
// MaxBytesReader split in handleUpload), never silently truncated; anything
// else is a 500.
//
// Every one of those bodies carries the names that DID land, because a
// partially-failed batch is not rolled back and must not be reported as
// though nothing happened. Per-file atomicity is the guarantee (each file is
// whole or absent); the batch is not atomic, and true batch rollback is not
// available to want — an upload may OVERWRITE, so undoing one needs a backup
// of every destination, and the rollback can itself fail halfway. So the
// response tells the truth instead: these landed, then this failed. The
// client attaches the ones that landed and names the first that did not.
//
// The 400 branch goes through WriteJSONStatus rather than httpreply.BadRequest
// because that helper writes {"error": msg} and cannot carry a second key.
// It keeps err.Error() (an invalid filename is client-caused and safe to
// echo); 413 and 500 keep their generic sentinels so no raw filesystem error
// reaches the wire.
func respondUploadError(w http.ResponseWriter, dir string, uploaded []string, err error) {
	if errors.Is(err, errInvalidFilename) {
		webhttp.WriteJSONStatus(w, http.StatusBadRequest,
			uploadErrorJSON(err.Error(), uploaded))
		return
	}
	if errors.Is(err, atomicfile.ErrFileTooLarge) {
		slog.Warn("filebrowse: upload too large",
			"limit", maxUploadSize, "uploaded", len(uploaded), "error", err)
		webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
			uploadErrorJSON("upload too large", uploaded))
		return
	}
	slog.Warn("filebrowse: upload write failed",
		"dir", dir, "uploaded", len(uploaded), "error", err)
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
// atomically via write-temp-then-rename, returning the list of
// filenames written plus total bytes for observability. On error, the
// partial temp file is removed; files written earlier in the batch
// remain on disk, and the names of those files ride the error response
// (see respondUploadError) so the client can report and use them. The
// context lets a client disconnect abort the remaining files in a
// batch upload.
func writeUploads(ctx context.Context, dirLoc loc, files []*multipart.FileHeader) (uploaded []string, total int64, err error) {
	uploaded = make([]string, 0, len(files))
	for _, fh := range files {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return uploaded, total, ctxErr
		}
		name := filepath.Base(fh.Filename)
		if name == "" || name == "." || name == ".." {
			slog.Warn("filebrowse: upload rejected: invalid filename",
				"raw_name", fh.Filename, "base", name, "target_dir", dirLoc.abs)
			return uploaded, total, fmt.Errorf("%w: %q", errInvalidFilename, fh.Filename)
		}
		dest := filepath.Join(dirLoc.abs, name)
		// Per-file sensitive-path gate. isProtectedDir on the target
		// directory (in handleUpload) catches container-level drops;
		// this check blocks file-level overwrites of sensitive exact-
		// match entries like /config/push-subs.json when the target
		// is a non-sensitive parent that enclosures the sensitive
		// file. Mirrors the rename-destination check in actionRename.
		if IsSensitive(dest) {
			slog.Warn("filebrowse: upload rejected: sensitive dest",
				"raw_name", fh.Filename, "dest", dest)
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

func writeOneUpload(ctx context.Context, dest loc, fh *multipart.FileHeader) (n int64, err error) {
	src, err := fh.Open()
	if err != nil {
		return 0, err
	}
	defer func() { _ = src.Close() }()

	// Abort at the next chunk boundary on context cancel. The per-file size
	// cap is enforced by WithMaxBytes below, which REJECTS an over-cap file
	// (atomicfile.ErrFileTooLarge, mapped to a 413 in handleUpload) — the
	// old io.LimitReader here silently truncated it instead.
	cr := &countingReader{r: &ctxReader{ctx: ctx, r: src}}
	// WriteReaderInRoot stages a temp inside the mount's root, fsyncs it,
	// renames over the root-relative dest, then fsyncs the parent dir — the
	// durability step the old hand-rolled path omitted — while keeping the
	// write kernel-confined to the mount like the rest of this handler (a
	// symlink planted in the tree cannot redirect it outside). It refuses a
	// symlink dest and removes the temp on any error, so dest is never left
	// a partial write.
	if _, werr := atomicfile.WriteReaderInRoot(ctx, dest.m.root, dest.rel(), cr,
		atomicfile.WithMode(0o600), atomicfile.WithMaxBytes(maxUploadSize)); werr != nil {
		return cr.n, werr
	}
	return cr.n, nil
}
