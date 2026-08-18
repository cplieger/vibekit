package filebrowse

import (
	"archive/zip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/cplieger/vibekit/internal/api"
)

const (
	maxZipBytes = 500 * 1024 * 1024 // 500 MB
	maxZipFiles = 10_000
)

func (h *Handler) handleDownloadZip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var req struct {
		Paths []string `json:"paths"`
	}
	if !api.DecodeJSON(w, r, &req) {
		return
	}
	if len(req.Paths) == 0 {
		api.BadRequest(w, "no paths provided")
		return
	}

	// Resolve all paths upfront before streaming (can't send error after headers).
	paths, ok := h.resolveZipPaths(w, req.Paths)
	if !ok {
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="download.zip"`)

	zw := zip.NewWriter(w)
	defer zw.Close()

	flusher, _ := w.(http.Flusher)
	z := &zipStream{zw: zw, flusher: flusher, ctx: r.Context()}
	for _, p := range paths {
		if !z.add(p, filepath.Base(p.abs)) {
			break
		}
	}
}

// resolveZipPaths resolves every requested path through the
// path-containment guard BEFORE any bytes are written, so a rejection
// can still surface as an HTTP 403 (headers aren't sent yet). Returns
// ok=false once resolveOrForbid has written the rejection response.
func (h *Handler) resolveZipPaths(w http.ResponseWriter, reqPaths []string) (paths []loc, ok bool) {
	paths = make([]loc, 0, len(reqPaths))
	for _, p := range reqPaths {
		l, resolved := h.resolveOrForbid(w, p)
		if !resolved {
			return nil, false
		}
		paths = append(paths, l)
	}
	return paths, true
}

// zipStream carries the mutable accounting for one streaming-zip
// response. Hoisting the directory walk onto named methods (instead of
// a deeply-nested recursive closure) keeps handleDownloadZip's
// control-flow flat; the size/count caps and the os.Root confinement
// are unchanged.
type zipStream struct {
	zw         *zip.Writer
	flusher    http.Flusher
	ctx        context.Context
	totalBytes int64
	fileCount  int
}

// capped reports whether streaming should stop: context cancelled, or
// either the file-count or byte-size cap already reached.
func (z *zipStream) capped() bool {
	return z.ctx.Err() != nil || z.fileCount >= maxZipFiles || z.totalBytes >= maxZipBytes
}

// add writes one entry (file or directory, recursively) into the zip.
// It returns false to stop the whole stream (caps hit, context
// cancelled, or a fatal zip-writer error) and true to continue with
// the next sibling. An unreadable entry is skipped (logged, true).
// Recursion stays inside l's mount by construction (children join onto
// the parent's location), so every open goes through that mount's root.
func (z *zipStream) add(l loc, zipName string) bool {
	if z.capped() {
		return false
	}
	// Sensitive paths are hidden from listings; keep them out of
	// archives too when a directory walk reaches one.
	if IsSensitive(l.abs) {
		return true
	}
	f, err := l.m.root.Open(l.rel())
	if err != nil {
		slog.Warn("filebrowse: zip open failed", "path", l.abs, "error", err)
		return true // skip
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		slog.Warn("filebrowse: zip stat failed", "path", l.abs, "error", err)
		return true
	}
	if info.IsDir() {
		return z.addDir(f, l, zipName)
	}
	return z.writeFile(f, zipName)
}

// addDir recurses into every child of an open directory. A read
// failure skips the directory (logged, true); a stop signal from any
// child propagates as false.
func (z *zipStream) addDir(f *os.File, l loc, zipName string) bool {
	entries, err := f.ReadDir(-1)
	if err != nil {
		slog.Warn("filebrowse: zip readdir failed", "path", l.abs, "error", err)
		return true
	}
	for _, e := range entries {
		child := loc{m: l.m, abs: filepath.Join(l.abs, e.Name())}
		if !z.add(child, filepath.Join(zipName, e.Name())) {
			return false
		}
	}
	return true
}

// writeFile copies one regular file into the archive, updates the
// running totals, flushes, and stops the stream once a cap is reached.
func (z *zipStream) writeFile(f *os.File, zipName string) bool {
	fw, err := z.zw.Create(zipName)
	if err != nil {
		return false
	}
	n, _ := io.Copy(fw, f)
	z.totalBytes += n
	z.fileCount++
	if z.flusher != nil {
		z.flusher.Flush()
	}
	if z.totalBytes >= maxZipBytes {
		slog.Warn("filebrowse: zip size cap reached", "bytes", z.totalBytes)
		return false
	}
	if z.fileCount >= maxZipFiles {
		slog.Warn("filebrowse: zip file cap reached", "count", z.fileCount)
		return false
	}
	return true
}
