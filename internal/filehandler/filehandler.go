// Package filehandler provides HTTP endpoints for browsing, reading,
// editing, uploading, and downloading files. The browsable surface is
// an ALLOW-LIST of granted roots (the /workspace and /config mounts by
// default, plus any VIBEKIT_BROWSE_ROOTS grants), each kernel-confined
// through its own os.Root; everything outside the grants is denied by
// default. A sensitive-path list additionally blocks the credential
// and state files living inside /config. The URL namespace is the
// container-absolute path ("/workspace/...", "/config/..."), and "/"
// lists the granted mounts.
//
// Defense layers are documented on paths.go. The handler is the
// gatekeeper for everything the user can do from the browser; every
// route that takes a path runs it through resolveOrForbid so that the
// symlink-aware resolvePath is applied uniformly. Operations that
// touch a DIRECTORY (delete, upload target) additionally consult
// isProtectedDir; operations with a secondary path argument (rename's
// name, copy/move's dest) re-run the full guard on the new path. The
// two RECURSIVE routes (the zip download and the content search)
// resolve their root once and then stay in that mount by
// construction, re-checking the sensitive-path list per entry —
// see filehandler_search.go for why both halves are required.
package filehandler

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/api"
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

	// respPath is the response-body key echoing the request path back
	// to the client (listing and read responses).
	respPath = "path"

	// defaultUploadDir is the upload target when the client sends no
	// "dir". A workspace folder modelled on an OS Downloads folder:
	// user-managed, with no pruning and no retention policy anywhere in
	// this app. It does not have to exist — handleUpload's MkdirAll
	// creates it inside the mount's own os.Root on the first upload, so
	// nothing else in the app pre-creates it.
	//
	// A literal rather than a value derived from KIRO_WORK_DIR, because
	// the handler holds a longest-first sorted mount list and no notion
	// of which of those mounts is "the workspace"; an operator who moves
	// the workspace also has to move this. The client sends the same
	// string for the composer's drop and paste uploads, and
	// TestUploadPolicyMatchesClient pins the two spellings together.
	defaultUploadDir = "/workspace/uploads"
)

// Handler implements api.RouteHandler, serving /api/file/* and /api/files/*.
type Handler struct {
	mounts []mount // sorted longest-dir-first (see openMounts)
}

var _ api.RouteHandler = (*Handler)(nil)

// New creates a file handler whose browsable surface is exactly
// rootDirs. Each granted directory gets its own os.Root, so every file
// operation is kernel-confined to its mount (TOCTOU-free). A grant
// that cannot be opened is skipped with a warning — a typo'd
// VIBEKIT_BROWSE_ROOTS entry must not brick the UI — but zero usable
// mounts is a hard error.
func New(rootDirs ...string) (*Handler, error) {
	mounts, errs := openMounts(rootDirs)
	for _, err := range errs {
		slog.Warn("filehandler: skipping browse root", "error", err)
	}
	if len(mounts) == 0 {
		return nil, fmt.Errorf("filehandler: no usable browse roots in %q", rootDirs)
	}
	return &Handler{mounts: mounts}, nil
}

// RegisterRoutes wires all /api/file* and /api/files* routes onto mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/file", h.handleFile)
	mux.HandleFunc("/api/file/download", h.handleDownload)
	mux.HandleFunc("/api/file/upload", h.handleUpload)
	mux.HandleFunc("/api/files", h.handleFiles)
	mux.HandleFunc("/api/files/action", h.handleFilesAction)
	mux.HandleFunc("/api/files/download", h.handleDownloadZip)
	mux.HandleFunc("/api/files/search", h.handleFilesSearch)
}

// errHandled signals that an action function has already written its
// error response (e.g. for a validation failure with a specific status
// code) and handleFilesAction should not double-write.
var errHandled = errors.New("handled")

// resolveOrForbid is the common path-resolve prelude: returns the
// resolved location or writes a 403 and returns (loc{}, false). Logs
// every rejection at slog.Warn so traversal probes leave a breadcrumb
// without leaking attacker input beyond the structured JSON-escaped
// "path" key.
func (h *Handler) resolveOrForbid(w http.ResponseWriter, reqPath string) (loc, bool) {
	l, err := h.resolvePath(reqPath)
	if err != nil {
		slog.Warn("filehandler: path rejected",
			"path", reqPath, "reason", err.Error())
		api.Forbidden(w, err.Error())
		return loc{}, false
	}
	return l, true
}

// --- /api/file (GET read, PUT write) ---

func (h *Handler) handleFile(w http.ResponseWriter, r *http.Request) {
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		api.BadRequest(w, "missing path")
		return
	}
	l, ok := h.resolveOrForbid(w, reqPath)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		readFile(r.Context(), w, l, reqPath)
	case http.MethodPut:
		writeFile(w, r, l)
	default:
		api.MethodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}
