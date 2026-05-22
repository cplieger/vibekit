package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// --- Filesystem helpers ---

// isStaleTempName reports whether name matches one of the literal
// suffix patterns writeTempFile / writeOneUpload / actionCopy produce:
//   - "<base>.tmp-<rand>"    — SaveBytes/SaveJSON via os.CreateTemp
//   - "<base>.upload-<rand>" — filehandler.writeOneUpload multipart uploads
//   - "<base>.copy-<rand>"   — filehandler.actionCopy streaming copies
//
// os.CreateTemp replaces the single "*" in each pattern with a random
// suffix, so the signature is the tag followed by at least one more
// character with no trailing dot or path separator. Substring matching
// (the pre-anchor check) would also reap legitimate user files whose
// names happened to contain those tags; anchoring + tail validation
// prevents that.
func isStaleTempName(name string) bool {
	for _, tag := range [...]string{".tmp-", ".upload-", ".copy-"} {
		i := strings.LastIndex(name, tag)
		if i < 0 || i+len(tag) >= len(name) {
			continue
		}
		tail := name[i+len(tag):]
		if !strings.ContainsAny(tail, "./\\") {
			return true
		}
	}
	return false
}

// CleanupStaleTemps removes sibling "*.tmp-*" files left by crashes of
// SaveBytes/SaveJSON (SIGKILL between CreateTemp and Rename: OOM, power
// loss, ungraceful container kill). Safe to call concurrently with new
// writes: only files whose mtime is older than maxAge are removed, so
// an in-flight rename's temp won't be deleted out from under it. Errors
// are logged at Warn but not returned; best-effort. Missing dir is not
// an error.
//
// Intended for startup sweep of persistent state directories (e.g.
// `<configDir>/chats/`). A conservative cutoff (≥1h) guarantees no
// race with a live write, which completes in well under a second.
func CleanupStaleTemps(dir string, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("api.CleanupStaleTemps: readdir failed",
				"dir", dir, "error", err)
		}
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		name := e.Name()
		if !isStaleTempName(name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			// Race with another sweeper / the file being renamed out
			// from under us is expected (ENOENT); anything else is a
			// breadcrumb we want to see at debug level so persistent
			// permission issues don't silently leak temps forever.
			if !errors.Is(err, os.ErrNotExist) {
				slog.Debug("api.CleanupStaleTemps: stat failed, skipping",
					"dir", dir, "name", name, "error", err)
			}
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		full := filepath.Join(dir, name)
		if err := os.Remove(full); err != nil {
			slog.Warn("api.CleanupStaleTemps: remove failed",
				"path", full, "error", err)
			continue
		}
		slog.Info("api.CleanupStaleTemps: removed stale temp",
			"path", full, "age", time.Since(info.ModTime()))
	}
}

// IsGitRepo reports whether dir contains a .git entry (directory for
// regular repos, regular file for worktrees and submodules, or a
// symlink to either — os.Stat follows symlinks). Only existence is
// checked; the entry's contents are not validated.
func IsGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// ServeJSONFile registers GET/PUT handlers for a JSON file at
// /api/<basename>. The path must end in ".json" and the fallback must
// itself be valid JSON; both are enforced at registration time via
// panic so wiring mistakes fail at startup.
//
// GET returns the file's contents, or the fallback with HTTP 200 when
// the file doesn't exist yet. Any other read error returns HTTP 500.
//
// PUT accepts a JSON object (arrays and scalars are rejected as
// "expected json object"), round-trips the body through
// json.MarshalIndent before persistence (byte-exact preservation is
// not guaranteed), and enforces MaxJSONBody as the maximum request
// size. Oversized bodies return HTTP 413; malformed JSON returns 400.
func ServeJSONFile(mux *http.ServeMux, path, fallback string, perm os.FileMode) {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	if ext != ".json" {
		panic("ServeJSONFile: path must end in .json, got " + path)
	}
	name := strings.TrimSuffix(base, ext)
	if name == "" {
		panic("ServeJSONFile: cannot derive route name from path " + path)
	}
	if !json.Valid([]byte(fallback)) {
		panic("ServeJSONFile: fallback is not valid JSON for path " + path)
	}
	var mu sync.Mutex
	mux.HandleFunc("/api/"+name, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			serveJSONGet(w, path, name, fallback)
		case http.MethodPut:
			serveJSONPut(w, r, path, name, &mu, perm)
		default:
			MethodNotAllowed(w)
		}
	})
}

// serveJSONGet handles the GET branch of ServeJSONFile: reads the file
// from disk and writes it; on ENOENT writes the caller-supplied
// fallback (valid-JSON was validated at registration time). On any
// other read error writes a sentinel 500 body — the raw error is logged
// but never leaked to the client (prevents filesystem-path exfil).
func serveJSONGet(w http.ResponseWriter, path, name, fallback string) {
	jsonHeaders(w)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, werr := w.Write([]byte(fallback)); werr != nil {
				slog.Debug("serveJSONFile: fallback write failed",
					"route", name, "path", path, "error", werr)
			}
			return
		}
		slog.Warn("serveJSONFile: read failed",
			"route", name, "path", path, "error", err)
		WriteJSONStatus(w, http.StatusInternalServerError,
			map[string]string{"error": "read failed"})
		return
	}
	if _, werr := w.Write(data); werr != nil {
		slog.Debug("serveJSONFile: write failed",
			"route", name, "path", path, "error", werr)
	}
}

// serveJSONPut handles the PUT branch of ServeJSONFile: enforces
// Content-Type, caps body at MaxJSONBody, rejects non-object
// top-levels, and persists the body via SaveJSON. Returns sentinel
// error messages; the raw error is logged but never leaked.
func serveJSONPut(w http.ResponseWriter, r *http.Request, path, name string, mu *sync.Mutex, perm os.FileMode) {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mediaType, _, err := mime.ParseMediaType(ct)
		if err != nil || mediaType != "application/json" {
			slog.Warn("serveJSONFile: unexpected content-type",
				"route", name, "content_type", ct)
			BadRequest(w, "expected application/json")
			return
		}
	}
	LimitBody(w, r, MaxJSONBody)
	var body json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("serveJSONFile: body too large",
				"route", name,
				"limit", MaxJSONBody,
				"content_length", r.Header.Get("Content-Length"),
				"error", maxErr)
			WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				map[string]string{"error": "request body too large"})
			return
		}
		slog.Warn("serveJSONFile: invalid json",
			"route", name, "error", err)
		BadRequest(w, "invalid json")
		return
	}
	// Reject non-object top-level values (null, arrays, scalars)
	// without allocating a key map: JSON objects start with `{`
	// after whitespace. `null` decodes as literal "null" whose
	// first non-whitespace byte is 'n', not '{'.
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		BadRequest(w, "expected json object")
		return
	}
	if err := SaveJSON(path, mu, body, "serveJSONFile:"+name, perm); err != nil {
		slog.Error("serveJSONFile: save failed",
			"route", name, "path", path, "error", err)
		WriteJSONStatus(w, http.StatusInternalServerError,
			map[string]string{"error": "save failed"})
		return
	}
	slog.Info("serveJSONFile: saved",
		"route", name, "bytes", len(body))
	Ok(w)
}
