package filehandler

import (
	"archive/zip"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"

	"github.com/cplieger/vibekit/internal/api"
)

const (
	maxZipBytes = 500 * 1024 * 1024 // 500 MB
	maxZipFiles = 10_000
)

//nolint:gocyclo // streaming zip writer threads many cases (security checks, recursion, flushing) into one handler; splitting hurts the linear-flow shape readers expect
func (h *Handler) handleDownloadZip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.MethodNotAllowed(w)
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
	type resolved struct{ abs, rel string }
	var paths []resolved
	for _, p := range req.Paths {
		abs := resolveOrForbid(w, p)
		if abs == "" {
			return
		}
		paths = append(paths, resolved{abs, h.relPath(abs)})
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="download.zip"`)

	zw := zip.NewWriter(w)
	defer zw.Close()

	flusher, _ := w.(http.Flusher)
	ctx := r.Context()
	var totalBytes int64
	var fileCount int

	var addFile func(rel, zipName string) bool
	addFile = func(rel, zipName string) bool {
		if ctx.Err() != nil || fileCount >= maxZipFiles || totalBytes >= maxZipBytes {
			return false
		}
		f, err := h.root.Open(rel)
		if err != nil {
			slog.Warn("filehandler: zip open failed", "path", rel, "error", err)
			return true // skip
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil {
			slog.Warn("filehandler: zip stat failed", "path", rel, "error", err)
			return true
		}
		if info.IsDir() {
			entries, readErr := f.ReadDir(-1)
			if readErr != nil {
				slog.Warn("filehandler: zip readdir failed", "path", rel, "error", readErr)
				return true
			}
			for _, e := range entries {
				if !addFile(filepath.Join(rel, e.Name()), filepath.Join(zipName, e.Name())) {
					return false
				}
			}
			return true
		}

		fw, err := zw.Create(zipName)
		if err != nil {
			return false
		}
		n, _ := io.Copy(fw, f)
		totalBytes += n
		fileCount++
		if flusher != nil {
			flusher.Flush()
		}
		if totalBytes >= maxZipBytes {
			slog.Warn("filehandler: zip size cap reached", "bytes", totalBytes)
			return false
		}
		if fileCount >= maxZipFiles {
			slog.Warn("filehandler: zip file cap reached", "count", fileCount)
			return false
		}
		return true
	}

	for _, p := range paths {
		if !addFile(p.rel, filepath.Base(p.abs)) {
			break
		}
	}
}
