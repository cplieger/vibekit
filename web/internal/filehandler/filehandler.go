// Package filehandler provides HTTP endpoints for browsing, reading,
// editing, uploading, and downloading files. Rooted at / with a
// blacklist of system directories and a sensitive-path list for
// internal state files.
//
// Defense layers are documented on paths.go. The handler is the
// gatekeeper for everything the user can do from the browser; every
// route that takes a path runs it through resolveOrForbid so that the
// symlink-aware resolvePath is applied uniformly. Operations that
// touch a DIRECTORY (delete, upload target) additionally consult
// isProtectedDir; operations with a secondary path argument (rename's
// name, copy/move's dest) re-run the full guard on the new path.
package filehandler

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"vibekit/internal/api"
)

const (
	maxFileSize   = 2 * 1024 * 1024   // 2 MB for /api/file read/write
	maxUploadSize = 50 * 1024 * 1024  // 50 MB per multipart upload
	maxCopySize   = 100 * 1024 * 1024 // 100 MB per single-file copy
	binarySniffN  = 8192              // bytes of prefix checked for NUL
	copyBufSize   = 32 * 1024         // io.CopyBuffer slab size
	// multipartMaxMemory is the in-RAM buffer ceiling
	// ParseMultipartForm uses before spilling parts to a tmpfile.
	// Small on purpose: the HTTP body cap is enforced by
	// MaxBytesReader; this value only decides when parts page to disk.
	// Matches the 1 MiB default net/http uses via `defaultMaxMemory`
	// and keeps concurrent uploads from stacking 50 MiB each in RAM.
	multipartMaxMemory = 1 * 1024 * 1024
)

// Handler implements api.FileHandler.
type Handler struct {
	root    *os.Root
	rootDir string
}

var _ api.FileHandler = (*Handler)(nil)

// New creates a file handler rooted at rootDir. All file operations
// are kernel-confined to rootDir via os.OpenRoot (TOCTOU-free).
func New(rootDir string) (*Handler, error) {
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return nil, err
	}
	return &Handler{root: root, rootDir: filepath.Clean(rootDir)}, nil
}

// RegisterRoutes wires all /api/file* and /api/files* routes onto mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/file", h.handleFile)
	mux.HandleFunc("/api/file/download", h.handleDownload)
	mux.HandleFunc("/api/file/upload", h.handleUpload)
	mux.HandleFunc("/api/files", h.handleFiles)
	mux.HandleFunc("/api/files/action", h.handleFilesAction)
	mux.HandleFunc("/api/files/download", h.handleDownloadZip)
}

// errHandled signals that an action function has already written its
// error response (e.g. for a validation failure with a specific status
// code) and handleFilesAction should not double-write.
var errHandled = errors.New("handled")

// resolveOrForbid is the common path-resolve prelude: returns the
// resolved path or writes a 403 and returns "". Logs every rejection
// at slog.Warn so traversal probes leave a breadcrumb without leaking
// attacker input beyond the structured JSON-escaped "path" key.
func resolveOrForbid(w http.ResponseWriter, reqPath string) string {
	resolved, err := resolvePath(reqPath)
	if err != nil {
		slog.Warn("filehandler: path rejected",
			"path", reqPath, "reason", err.Error())
		api.Forbidden(w, err.Error())
		return ""
	}
	return resolved
}

// --- /api/file (GET read, PUT write) ---

func (h *Handler) handleFile(w http.ResponseWriter, r *http.Request) {
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		api.BadRequest(w, "missing path")
		return
	}
	resolved := resolveOrForbid(w, reqPath)
	if resolved == "" {
		return
	}
	switch r.Method {
	case http.MethodGet:
		readFile(r.Context(), w, resolved, reqPath, h)
	case http.MethodPut:
		writeFile(w, r, resolved, h)
	default:
		api.MethodNotAllowed(w)
	}
}
