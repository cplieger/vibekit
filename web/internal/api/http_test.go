package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests for http.go: JSON writers, LimitBody, StripANSI, SaveBytes/SaveJSON,
// ServeJSONFile, IsGitRepo.

// --- StripANSI ---

func TestStripANSI(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"\x1b[31mred\x1b[0m", "red"},
		{"\x1b[1;32mbold green\x1b[0m", "bold green"},
		{"\x1b]0;title\x07rest", "rest"},
		{"no escapes", "no escapes"},
		{"", ""},
	}
	for _, tt := range tests {
		got := StripANSI(tt.in)
		if got != tt.want {
			t.Errorf("StripANSI(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestStripANSI_edge_cases(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"multiple", "\x1b[31ma\x1b[32mb\x1b[0m", "ab"},
		{"unicode", "\x1b[31méñ\x1b[0m", "éñ"},
		{"bare escape", "before\x1bafter", "before\x1bafter"},
		{"OSC no terminator", "\x1b]0;titlewithoutbell", "\x1b]0;titlewithoutbell"},
		{"OSC BEL", "\x1b]0;title\x07after", "after"},
		{"OSC ST", "\x1b]0;title\x1b\\rest", "rest"},
		{"semicolons", "\x1b[1;31;4mx\x1b[0m", "x"},
		{"empty params", "\x1b[mreset", "reset"},
		{"CSI private mode", "\x1b[?25lhidden\x1b[?25h", "hidden"},
		{"charset G0 select", "\x1b(Btext", "text"},
		{"charset G1 line-draw", "\x1b)0box", "box"},
		{"save and restore cursor", "\x1b7pos\x1b8", "pos"},
		{"RIS reset", "\x1bcreset", "reset"},
		{"SS2 / SS3", "\x1bNa\x1bOb", "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripANSI(tt.in); got != tt.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestStripANSI_is_idempotent(t *testing.T) {
	inputs := []string{
		"", "plain", "\x1b[31mred\x1b[0m",
		"mix\x1b[1;32mbold\x1b[0mplain\x1b]0;t\x07end",
		"\x1b[mno-params", "bare\x1b-escape",
		"\x1b]0;title\x1b\\done",
		"\x1b(Btext\x1b)0line",
	}
	for _, in := range inputs {
		once := StripANSI(in)
		twice := StripANSI(once)
		if once != twice {
			t.Errorf("StripANSI not idempotent: %q → %q → %q", in, once, twice)
		}
	}
}

// --- IsGitRepo ---

func TestIsGitRepo(t *testing.T) {
	tests := []struct {
		setup func(t *testing.T, dir string)
		name  string
		want  bool
	}{
		{
			name:  "empty dir",
			setup: func(_ *testing.T, _ string) {},
			want:  false,
		},
		{
			name: "regular git directory",
			setup: func(t *testing.T, dir string) {
				if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
					t.Fatalf("Mkdir error = %v", err)
				}
			},
			want: true,
		},
		{
			name: "worktree or submodule .git file",
			// Worktrees and submodules write ".git" as a regular file
			// containing "gitdir: <abs path>". IsGitRepo checks only
			// existence, not contents.
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, ".git"),
					[]byte("gitdir: ../.git/worktrees/foo\n"), 0o644); err != nil {
					t.Fatalf("WriteFile error = %v", err)
				}
			},
			want: true,
		},
		{
			name: "symlink .git to directory",
			setup: func(t *testing.T, dir string) {
				if runtime.GOOS == "windows" {
					t.Skip("symlink creation requires elevated perms on Windows")
				}
				target := filepath.Join(dir, "real-git")
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatalf("Mkdir target error = %v", err)
				}
				if err := os.Symlink(target, filepath.Join(dir, ".git")); err != nil {
					t.Fatalf("Symlink error = %v", err)
				}
			},
			want: true,
		},
		{
			name: "broken symlink",
			// os.Stat follows symlinks; a dangling link reports ENOENT,
			// which is the documented "not a repo" case.
			setup: func(t *testing.T, dir string) {
				if runtime.GOOS == "windows" {
					t.Skip("symlink creation requires elevated perms on Windows")
				}
				if err := os.Symlink(filepath.Join(dir, "missing"),
					filepath.Join(dir, ".git")); err != nil {
					t.Fatalf("Symlink error = %v", err)
				}
			},
			want: false,
		},
		{
			name: "non-git sibling file named similarly",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "gitfile"),
					[]byte("x"), 0o644); err != nil {
					t.Fatalf("WriteFile error = %v", err)
				}
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)

			got := IsGitRepo(dir)

			if got != tt.want {
				t.Errorf("IsGitRepo(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// --- JSON helpers ---

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, map[string]string{"key": "val"})

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if xcto := rec.Header().Get("X-Content-Type-Options"); xcto != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", xcto)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal error = %v", err)
	}
	if resp["key"] != "val" {
		t.Errorf("body = %+v", resp)
	}
	if !strings.HasSuffix(rec.Body.String(), "\n") {
		t.Errorf("body missing trailing newline")
	}
}

// TestNamedResponseHelpers covers every named helper in one go: status
// code, JSON envelope shape, and the required headers (Content-Type,
// X-Content-Type-Options). Locks in the contract documented in
// vibekit.md's "Server HTTP response helpers" table.
func TestNamedResponseHelpers(t *testing.T) {
	tests := []struct {
		call     func(http.ResponseWriter)
		name     string
		wantBody string
		wantCode int
	}{
		{
			name:     "BadRequest",
			call:     func(w http.ResponseWriter) { BadRequest(w, "bad input") },
			wantCode: http.StatusBadRequest,
			wantBody: `{"error":"bad input"}`,
		},
		{
			name:     "Forbidden",
			call:     func(w http.ResponseWriter) { Forbidden(w, "origin not allowed") },
			wantCode: http.StatusForbidden,
			wantBody: `{"error":"origin not allowed"}`,
		},
		{
			name:     "NotFound",
			call:     func(w http.ResponseWriter) { NotFound(w, "no such chat") },
			wantCode: http.StatusNotFound,
			wantBody: `{"error":"no such chat"}`,
		},
		{
			name:     "Conflict",
			call:     func(w http.ResponseWriter) { Conflict(w, "busy") },
			wantCode: http.StatusConflict,
			wantBody: `{"error":"busy"}`,
		},
		{
			name:     "MethodNotAllowed",
			call:     func(w http.ResponseWriter) { MethodNotAllowed(w) },
			wantCode: http.StatusMethodNotAllowed,
			wantBody: `{"error":"method not allowed"}`,
		},
		{
			name:     "InternalError",
			call:     func(w http.ResponseWriter) { InternalError(w, os.ErrPermission) },
			wantCode: http.StatusInternalServerError,
			wantBody: `{"error":"internal error"}`,
		},
		{
			name:     "InternalError_nil_err",
			call:     func(w http.ResponseWriter) { InternalError(w, nil) },
			wantCode: http.StatusInternalServerError,
			wantBody: `{"error":"internal error"}`,
		},
		{
			name:     "Ok",
			call:     func(w http.ResponseWriter) { Ok(w) },
			wantCode: http.StatusOK,
			wantBody: `{"ok":true}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()

			tt.call(rec)

			if rec.Code != tt.wantCode {
				t.Errorf("%s status = %d, want %d", tt.name, rec.Code, tt.wantCode)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tt.wantBody {
				t.Errorf("%s body = %q, want %q", tt.name, got, tt.wantBody)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("%s Content-Type = %q, want application/json", tt.name, ct)
			}
			if xcto := rec.Header().Get("X-Content-Type-Options"); xcto != "nosniff" {
				t.Errorf("%s X-Content-Type-Options = %q, want nosniff", tt.name, xcto)
			}
		})
	}
}

func TestLimitBody_caps_read_at_max_bytes(t *testing.T) {
	body := strings.NewReader(strings.Repeat("a", 100))
	req := httptest.NewRequest(http.MethodPost, "/x", body)
	rec := httptest.NewRecorder()

	LimitBody(rec, req, 10)

	data, err := io.ReadAll(req.Body)
	if err == nil {
		t.Errorf("expected MaxBytesError")
	}
	if len(data) > 10 {
		t.Errorf("read %d bytes, want <= 10", len(data))
	}
}

func TestLimitBody_allows_read_under_cap(t *testing.T) {
	body := strings.NewReader("small")
	req := httptest.NewRequest(http.MethodPost, "/x", body)
	rec := httptest.NewRecorder()

	LimitBody(rec, req, 100)

	data, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("ReadAll error = %v", err)
	}
	if string(data) != "small" {
		t.Errorf("body = %q, want %q", string(data), "small")
	}
}

// --- SaveBytes ---

func TestSaveBytes_round_trips_content_and_applies_mode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode semantics differ on Windows")
	}
	tests := []struct {
		name    string
		data    []byte
		perm    os.FileMode
		dirPerm os.FileMode
	}{
		{"empty", []byte{}, 0o644, 0o755},
		{"text", []byte("hello world\n"), 0o644, 0o755},
		{"binary", []byte{0x00, 0x01, 0xff, 0x7f, 0x80}, 0o644, 0o755},
		{"private perm triggers 0700 parent", []byte("secret"), 0o600, 0o700},
		{"world-readable stays 0755", []byte("pub"), 0o664, 0o755},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "nested")
			path := filepath.Join(dir, "out.bin")

			if err := SaveBytes(path, tt.data, tt.perm); err != nil {
				t.Fatalf("SaveBytes(%q) error = %v", tt.name, err)
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile error = %v", err)
			}
			if !bytes.Equal(got, tt.data) {
				t.Errorf("SaveBytes(%q) round-trip = %v, want %v", tt.name, got, tt.data)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat error = %v", err)
			}
			if info.Mode().Perm() != tt.perm {
				t.Errorf("SaveBytes(%q) file mode = %o, want %o", tt.name, info.Mode().Perm(), tt.perm)
			}
			dirInfo, err := os.Stat(dir)
			if err != nil {
				t.Fatalf("dir Stat error = %v", err)
			}
			if dirInfo.Mode().Perm() != tt.dirPerm {
				t.Errorf("SaveBytes(%q) dir mode = %o, want %o", tt.name, dirInfo.Mode().Perm(), tt.dirPerm)
			}
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if strings.Contains(e.Name(), ".tmp-") {
					t.Errorf("SaveBytes(%q) left stale temp file: %q", tt.name, e.Name())
				}
			}
		})
	}
}

func TestSaveBytes_parent_unusable_returns_error(t *testing.T) {
	// A child of a regular file cannot be created (ENOTDIR).
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	path := filepath.Join(blocker, "child", "out.bin")

	err := SaveBytes(path, []byte("data"), 0o644)
	if err == nil {
		t.Error("SaveBytes with unusable parent = nil, want error")
	}
	// Target file must not be a regular file (may return ENOTDIR rather
	// than ENOENT when the path traverses a non-dir, which is fine).
	if info, sErr := os.Stat(path); sErr == nil && info.Mode().IsRegular() {
		t.Errorf("target file should not exist as regular file after failure")
	}
}

// --- SaveJSON ---

func TestSaveJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "data.json")
	var mu sync.Mutex
	if err := SaveJSON(path, &mu, map[string]int{"x": 1}, "test", 0o644); err != nil {
		t.Fatalf("SaveJSON error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	if !strings.Contains(string(data), `"x": 1`) {
		t.Errorf("content = %q", string(data))
	}
	// Verify SaveJSON released the mutex.
	if !mu.TryLock() {
		t.Fatal("SaveJSON did not release mutex")
	}
	mu.Unlock()
}

func TestSaveJSON_applies_perm_mode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode not meaningful on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	var mu sync.Mutex
	if err := SaveJSON(path, &mu, map[string]int{"x": 1}, "test", 0o600); err != nil {
		t.Fatalf("SaveJSON error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 0600", got)
	}
}

func TestSaveJSON_marshal_error_does_not_create_file(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	var mu sync.Mutex

	if err := SaveJSON(path, &mu, make(chan int), "test", 0o644); err == nil {
		t.Error("expected marshal error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file created on marshal error: %v", err)
	}
}

func TestSaveJSON_leaves_no_temp_file_on_success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	var mu sync.Mutex
	if err := SaveJSON(path, &mu, map[string]int{"x": 1}, "test", 0o644); err != nil {
		t.Fatalf("SaveJSON error = %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("stale temp file: %q", e.Name())
		}
	}
}

// --- ServeJSONFile ---

func TestServeJSONFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	mux := http.NewServeMux()
	ServeJSONFile(mux, path, `{"default":true}`, 0o644)

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "default") {
		t.Errorf("fallback not returned: %s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"saved":true}`))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("PUT status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "saved") {
		t.Errorf("saved not returned: %s", rec.Body.String())
	}
}

func TestServeJSONFile_PUT_rejects_invalid_json(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	mux := http.NewServeMux()
	ServeJSONFile(mux, path, `{}`, 0o644)

	req := httptest.NewRequest(http.MethodPut, "/api/config",
		strings.NewReader(`{not json`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestServeJSONFile_PUT_rejects_non_object(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	mux := http.NewServeMux()
	ServeJSONFile(mux, path, `{}`, 0o644)

	for _, body := range []string{`null`, `42`, `"str"`, `[1,2,3]`, `true`} {
		req := httptest.NewRequest(http.MethodPut, "/api/config",
			strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("PUT %q status = %d, want 400", body, rec.Code)
		}
	}
}

func TestServeJSONFile_PUT_rejects_oversize_body(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	mux := http.NewServeMux()
	ServeJSONFile(mux, path, `{}`, 0o644)

	// Valid JSON object with padding string that exceeds MaxJSONBody.
	padding := strings.Repeat("a", MaxJSONBody+1)
	body := `{"x":"` + padding + `"}`
	req := httptest.NewRequest(http.MethodPut, "/api/config",
		strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestServeJSONFile_rejects_unsupported_methods(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	mux := http.NewServeMux()
	ServeJSONFile(mux, path, `{}`, 0o644)

	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/api/config", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d", method, rec.Code)
		}
	}
}

func TestServeJSONFile_panics_on_empty_basename(t *testing.T) {
	mux := http.NewServeMux()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic")
		}
	}()
	ServeJSONFile(mux, ".json", `{}`, 0o644)
}

func TestServeJSONFile_panics_on_non_json_extension(t *testing.T) {
	mux := http.NewServeMux()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic")
		}
	}()
	ServeJSONFile(mux, "/tmp/config.yaml", `{}`, 0o644)
}

func TestServeJSONFile_panics_on_invalid_fallback(t *testing.T) {
	mux := http.NewServeMux()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic")
		}
	}()
	ServeJSONFile(mux, "/tmp/config.json", `{not json`, 0o644)
}

func TestServeJSONFile_GET_returns_500_on_non_enoent_read_error(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 000 does not deny read on Windows ACLs")
	}
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		t.Skip("root bypasses EACCES")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"real":true}`), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("Chmod error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	mux := http.NewServeMux()
	ServeJSONFile(mux, path, `{"fallback":true}`, 0o644)

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal error = %v; body = %q", err, rec.Body.String())
	}
	// Must be the sentinel "read failed", not the raw OS error, so we
	// don't leak filesystem paths to clients. Mirrors the "save failed"
	// assertion for the PUT branch.
	if resp["error"] != "read failed" {
		t.Errorf("error = %q, want %q", resp["error"], "read failed")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// Defense in depth against a future regression that calls
	// InternalError(w, err) instead of the sentinel — the raw error
	// contains the on-disk path.
	if strings.Contains(rec.Body.String(), path) {
		t.Errorf("response body leaked path %q: %q", path, rec.Body.String())
	}
}
func TestSaveBytes_writeTempFile_error_is_propagated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits drive this path")
	}
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		t.Skip("root bypasses EACCES on read-only directories")
	}
	// Parent exists but is read-only: MkdirAll is a no-op, then
	// writeTempFile's CreateTemp fails with EACCES.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("Chmod error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	path := filepath.Join(dir, "out.bin")

	err := SaveBytes(path, []byte("data"), 0o644)
	if err == nil {
		t.Fatal("SaveBytes(ro parent) = nil, want error")
	}
	if _, sErr := os.Stat(path); !os.IsNotExist(sErr) {
		t.Errorf("SaveBytes(ro parent) created target: stat err = %v", sErr)
	}
	// Restore write perms so ReadDir can enumerate.
	_ = os.Chmod(dir, 0o755)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("SaveBytes(ro parent) left stale temp file: %q", e.Name())
		}
	}
}

func TestSaveBytes_rename_error_cleans_up_temp_file(t *testing.T) {
	// Pre-create the target as a directory. os.Rename of a file onto
	// an existing directory fails with EEXIST, exercising the
	// Remove-tmp + return-err branch.
	root := t.TempDir()
	path := filepath.Join(root, "target")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("Mkdir error = %v", err)
	}

	err := SaveBytes(path, []byte("data"), 0o644)
	if err == nil {
		t.Fatal("SaveBytes(target is dir) = nil, want error")
	}

	// Target must still be a directory (unchanged).
	info, sErr := os.Stat(path)
	if sErr != nil {
		t.Fatalf("Stat(target) error = %v", sErr)
	}
	if !info.IsDir() {
		t.Errorf("SaveBytes overwrote directory target with a file")
	}

	// No stale temp file left in the parent after the failed rename.
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("SaveBytes(rename fail) left stale temp file: %q", e.Name())
		}
	}
}

func TestServeJSONFile_PUT_returns_500_on_save_failure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits drive this path")
	}
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		t.Skip("root bypasses EACCES on read-only directories")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	mux := http.NewServeMux()
	ServeJSONFile(mux, path, `{}`, 0o644)

	// Make the parent dir read-only after registration so the atomic-
	// rename pipeline (CreateTemp) fails inside SaveJSON.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("Chmod error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	req := httptest.NewRequest(http.MethodPut, "/api/config",
		strings.NewReader(`{"x":1}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal error = %v; body = %q", err, rec.Body.String())
	}
	// Must be the sentinel "save failed", not the raw OS error, so we
	// don't leak filesystem paths to clients.
	if resp["error"] != "save failed" {
		t.Errorf("error = %q, want %q", resp["error"], "save failed")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestServeJSONFile_PUT_content_type_enforcement(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		wantBody    string
		wantCode    int
	}{
		{"empty ct accepted", "", `"ok":true`, http.StatusOK},
		{"application/json accepted", "application/json", `"ok":true`, http.StatusOK},
		{
			"application/json with charset accepted",
			"application/json; charset=utf-8", `"ok":true`, http.StatusOK,
		},
		{"text/plain rejected", "text/plain", `"expected application/json"`, http.StatusBadRequest},
		{
			"text/html rejected",
			"text/html; charset=utf-8", `"expected application/json"`, http.StatusBadRequest,
		},
		{
			"application/xml rejected",
			"application/xml", `"expected application/json"`, http.StatusBadRequest,
		},
		{
			"multipart/form-data rejected",
			"multipart/form-data; boundary=xyz", `"expected application/json"`, http.StatusBadRequest,
		},
		{
			"application/json-patch+json rejected (mime parse vs HasPrefix)",
			"application/json-patch+json", `"expected application/json"`, http.StatusBadRequest,
		},
		{
			"application/json5 rejected (not RFC 8259 JSON)",
			"application/json5", `"expected application/json"`, http.StatusBadRequest,
		},
		{
			"malformed content-type rejected",
			"application/json;;;charset=", `"expected application/json"`, http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			mux := http.NewServeMux()
			ServeJSONFile(mux, path, `{}`, 0o644)

			req := httptest.NewRequest(http.MethodPut, "/api/config",
				strings.NewReader(`{"x":1}`))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			rec := httptest.NewRecorder()

			mux.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Errorf("PUT(Content-Type=%q) status = %d, want %d",
					tt.contentType, rec.Code, tt.wantCode)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("PUT(Content-Type=%q) body = %q, want to contain %q",
					tt.contentType, rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestSaveJSON_nil_mutex_returns_error(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")

	err := SaveJSON(path, nil, map[string]int{"x": 1}, "test", 0o644)

	if err == nil {
		t.Fatal("SaveJSON(nil mutex) = nil, want error")
	}
	if !strings.Contains(err.Error(), "nil mutex") {
		t.Errorf("error = %q, want substring %q", err.Error(), "nil mutex")
	}
	if _, sErr := os.Stat(path); !os.IsNotExist(sErr) {
		t.Errorf("SaveJSON(nil mutex) created target: stat err = %v", sErr)
	}
}

func TestCleanupStaleTemps(t *testing.T) {
	dir := t.TempDir()

	// A "new" temp that must be preserved — a live rename may still be
	// racing against this sweep.
	recent := filepath.Join(dir, "live.json.tmp-1111")
	if err := os.WriteFile(recent, []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFile recent: %v", err)
	}

	// An "old" temp — left behind by a prior crash. Must be removed.
	old := filepath.Join(dir, "chat.json.tmp-2222")
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile old: %v", err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes old: %v", err)
	}

	// A canonical file that happens to share a prefix with a temp. Must
	// be preserved regardless of age — only "*.tmp-*" files are swept.
	canonical := filepath.Join(dir, "chat.json")
	if err := os.WriteFile(canonical, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("WriteFile canonical: %v", err)
	}
	if err := os.Chtimes(canonical, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes canonical: %v", err)
	}

	CleanupStaleTemps(dir, time.Hour)

	if _, err := os.Stat(recent); err != nil {
		t.Errorf("recent temp removed: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old temp not removed: stat err = %v", err)
	}
	if _, err := os.Stat(canonical); err != nil {
		t.Errorf("canonical file removed: %v", err)
	}

	// Missing dir: no error, no panic.
	missing := filepath.Join(dir, "no-such-subdir")
	CleanupStaleTemps(missing, time.Hour)
}

// --- SanitizeUnicode / isHiddenUnicode / SanitizeOutput ---

func TestIsHiddenUnicode_classifies_every_branch(t *testing.T) {
	// One test per branch of isHiddenUnicode so a future rune-list trim
	// surfaces as a named failure, not a coverage regression.
	tests := []struct {
		name string
		r    rune
		want bool
	}{
		// Visible text: must NOT be hidden.
		{"ascii letter", 'a', false},
		{"ascii digit", '0', false},
		{"ascii space", ' ', false},
		{"accented latin", 'é', false},
		{"cjk", '漢', false},
		{"emoji", '😀', false},
		{"tab (visible whitespace)", '\t', false},
		{"newline", '\n', false},

		// TAG characters (U+E0000-E007F): full block is hidden.
		{"TAG block lower bound", 0xE0000, true},
		{"TAG mid", 0xE0040, true},
		{"TAG upper bound", 0xE007F, true},
		{"just below TAG block", 0xDFFFF, false},
		{"just above TAG block", 0xE0080, false},

		// Explicit-codepoint set.
		{"soft hyphen", 0x00AD, true},
		{"zero-width space", 0x200B, true},
		{"zero-width non-joiner", 0x200C, true},
		{"zero-width joiner", 0x200D, true},
		{"LTR mark", 0x200E, true},
		{"RTL mark", 0x200F, true},
		{"BOM / zwnbsp", 0xFEFF, true},
		{"word joiner", 0x2060, true},
		{"function application (invisible math)", 0x2061, true},
		{"invisible times", 0x2062, true},
		{"invisible separator", 0x2063, true},
		{"invisible plus", 0x2064, true},

		// Bidi embedding/override range 0x202A..0x202E.
		{"bidi embedding lower", 0x202A, true},
		{"bidi embedding mid", 0x202C, true},
		{"bidi embedding upper", 0x202E, true},
		{"just below bidi embedding", 0x2029, false},
		{"just above bidi embedding", 0x202F, false},

		// Bidi isolate range 0x2066..0x2069.
		{"bidi isolate lower", 0x2066, true},
		{"bidi isolate mid", 0x2067, true},
		{"bidi isolate upper", 0x2069, true},
		{"just below bidi isolate", 0x2065, false},
		{"just above bidi isolate", 0x206A, false},

		// Nearby-but-not-hidden codepoints (regression guards).
		{"NUL (not stripped)", '\x00', false},
		{"nbsp (visible whitespace, must not be stripped)", 0x00A0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isHiddenUnicode(tt.r); got != tt.want {
				t.Errorf("isHiddenUnicode(U+%04X) = %v, want %v",
					tt.r, got, tt.want)
			}
		})
	}
}

func TestSanitizeUnicode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain ascii", "hello world", "hello world"},
		{"preserves newlines and tabs", "line1\n\tline2", "line1\n\tline2"},
		{"strips zero-width space", "a\u200Bb", "ab"},
		{"strips zero-width joiner", "a\u200Db", "ab"},
		{"strips BOM", "\uFEFFhello", "hello"},
		{"strips soft hyphen", "he\u00ADllo", "hello"},
		{"strips word joiner", "a\u2060b", "ab"},
		{"strips LTR mark", "\u200Ehello", "hello"},
		{"strips RTL mark", "hello\u200F", "hello"},
		{"strips bidi embedding range", "a\u202Ab\u202Ec", "abc"},
		{"strips bidi isolate range", "a\u2066b\u2069c", "abc"},
		{"strips TAG characters", "visible\U000E0041\U000E0062hidden", "visiblehidden"},
		{"preserves nbsp (visible)", "a\u00A0b", "a\u00A0b"},
		{"preserves accented characters", "café", "café"},
		{"preserves emoji", "hi 😀", "hi 😀"},
		{"multiple hidden in a row", "a\u200B\u200C\u200D\u2060b", "ab"},
		{"only hidden characters → empty", "\u200B\u200C\u200D\u2060", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeUnicode(tt.in)
			if got != tt.want {
				t.Errorf("SanitizeUnicode(%q) = %q, want %q",
					tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeUnicode_is_idempotent(t *testing.T) {
	// Invariant: applying twice yields the same result as applying once.
	// Pins the "remove-only" contract — no sanitization pass ever
	// introduces characters the next pass would strip.
	inputs := []string{
		"",
		"plain text",
		"a\u200Bb\u200Cc",
		"\uFEFFhello",
		"visible\U000E0041\U000E0062hidden",
		"café\u00AD résumé",
		"\u202Abidi\u202E",
		"\u200B\u200C\u200D\u2060",
	}
	for _, in := range inputs {
		once := SanitizeUnicode(in)
		twice := SanitizeUnicode(once)
		if once != twice {
			t.Errorf("SanitizeUnicode not idempotent: %q → %q → %q",
				in, once, twice)
		}
	}
}

func TestSanitizeOutput_composes_strip_ANSI_then_unicode(t *testing.T) {
	// Production actually calls SanitizeOutput, not the components.
	// These cases cover each of: ANSI only, Unicode only, both, neither.
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain text passthrough", "hello world", "hello world"},
		{"ansi only", "\x1b[31mred\x1b[0m", "red"},
		{"hidden unicode only", "a\u200Bb\uFEFFc", "abc"},
		{
			name: "both ansi and hidden unicode",
			in:   "\x1b[1;32mclean\u200Btext\x1b[0m\uFEFFafter",
			want: "cleantextafter",
		},
		{
			name: "hidden unicode inside ANSI payload is removed",
			in:   "\x1b[31mred\u200Btext\x1b[0m",
			want: "redtext",
		},
		{
			name: "TAG characters embedded between ANSI runs",
			in:   "\x1b[32mA\x1b[0m\U000E0041\x1b[31mB\x1b[0m",
			want: "AB",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeOutput(tt.in)
			if got != tt.want {
				t.Errorf("SanitizeOutput(%q) = %q, want %q",
					tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeOutput_is_idempotent(t *testing.T) {
	// Same invariant as StripANSI / SanitizeUnicode, extended to the
	// composition. Guards against a future refactor that moves
	// sanitization into a stateful codepath that accidentally re-emits
	// escapes or hidden codepoints.
	inputs := []string{
		"",
		"plain",
		"\x1b[31mred\x1b[0m",
		"a\u200Bb",
		"\x1b[1;32mgreen\u200Btext\x1b[0m\uFEFFtail",
		"visible\U000E0041\x1b[0m\u200Btext",
	}
	for _, in := range inputs {
		once := SanitizeOutput(in)
		twice := SanitizeOutput(once)
		if once != twice {
			t.Errorf("SanitizeOutput not idempotent: %q → %q → %q",
				in, once, twice)
		}
	}
}

func TestSanitizeOutput_strips_ansi_before_unicode(t *testing.T) {
	// Pins the composition order documented in the function comment:
	// StripANSI → SanitizeUnicode. Reversing the order (Unicode first)
	// would still pass the composed idempotency test, but would change
	// behaviour on inputs where the ANSI escape itself contains a
	// hidden codepoint. This test pins the actual order by comparing
	// against the manual composition.
	tricky := []string{
		"\x1b[31m\u200Bred\u200B\x1b[0m",
		"\x1b]0;title\u200B\x07rest",
		"before\x1b[1;32m\uFEFFbold\x1b[0mafter",
	}
	for _, in := range tricky {
		got := SanitizeOutput(in)
		want := SanitizeUnicode(StripANSI(in))
		if got != want {
			t.Errorf("SanitizeOutput(%q) = %q, want SanitizeUnicode(StripANSI(_)) = %q",
				in, got, want)
		}
	}
}

// --- WriteJSONStatus encode failure ---

func TestWriteJSONStatus_encode_failure_does_not_panic(t *testing.T) {
	// json.Encode fails on a chan value (unsupported type). The status
	// has already been written; the function must log and return
	// normally without panicking.
	rec := httptest.NewRecorder()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("WriteJSONStatus panicked on non-marshalable value: %v", r)
		}
	}()
	WriteJSONStatus(rec, http.StatusAccepted, make(chan int))

	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d (already committed before encode error)",
			rec.Code, http.StatusAccepted)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// --- isStaleTempName ---

func TestIsStaleTempName(t *testing.T) {
	// Pin the anchor semantics: the pattern must be ".tmp-<rand>" with
	// no trailing dot or path separator. Substring matching would eat
	// legitimate user files whose names happen to contain ".tmp-".
	tests := []struct {
		name string
		in   string
		want bool
	}{
		// Positive cases (actual CreateTemp output).
		{"chat temp", "chat.json.tmp-abc123", true},
		{"nested base temp", "state.tmp-xyz", true},
		{"trailing random", "foo.tmp-1", true},
		{"upload temp", "photo.jpg.upload-abc123", true},
		{"copy temp", "backup.tar.copy-xyz789", true},

		// Negative cases: anchor semantics.
		{"no .tmp- signature", "regular.json", false},
		{"suffix contains a dot", "alice.tmp-2024-notes.json", false},
		{"suffix contains a slash", "foo.tmp-a/b", false},
		{"suffix contains a backslash", "foo.tmp-a\\b", false},
		{"nothing after suffix", "just.tmp-", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStaleTempName(tt.in); got != tt.want {
				t.Errorf("isStaleTempName(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestCleanupStaleTemps_preserves_user_file_with_tmp_in_name(t *testing.T) {
	// Regression guard: a user file named "alice.tmp-2024-notes.json"
	// must survive a sweep even when older than maxAge. Before the
	// isStaleTempName anchor, CleanupStaleTemps would have removed it.
	dir := t.TempDir()
	userFile := filepath.Join(dir, "alice.tmp-2024-notes.json")
	if err := os.WriteFile(userFile, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(userFile, old, old); err != nil {
		t.Fatal(err)
	}

	// And the real temp alongside, same age, which should be swept.
	tmp := filepath.Join(dir, "chat.json.tmp-abc123")
	if err := os.WriteFile(tmp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmp, old, old); err != nil {
		t.Fatal(err)
	}

	CleanupStaleTemps(dir, time.Hour)

	if _, err := os.Stat(userFile); err != nil {
		t.Errorf("user file removed by sweep: %v", err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("real temp not removed: stat err = %v", err)
	}
}

// --- WriteRawJSON pass-through contract ---

// errWriter is an http.ResponseWriter whose Write always fails.
// Used to pin "best-effort on write failure" contracts without
// httptest.ResponseRecorder, whose Write never errors.
type errWriter struct{ hdr http.Header }

func (e *errWriter) Header() http.Header {
	if e.hdr == nil {
		e.hdr = http.Header{}
	}
	return e.hdr
}
func (*errWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (*errWriter) WriteHeader(int)           {}

func TestWriteRawJSON_passes_bytes_through_verbatim(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"object", []byte(`{"ok":true}`)},
		{"array", []byte(`[1,2,3]`)},
		{"scalar", []byte(`42`)},
		{"null", []byte(`null`)},
		{"string literal", []byte(`"hello"`)},
		{"empty", []byte{}},
		{"nested whitespace preserved", []byte(`{ "a" :   1 }`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()

			WriteRawJSON(rec, tt.data)

			if rec.Code != http.StatusOK {
				t.Errorf("WriteRawJSON(%q) status = %d, want 200",
					string(tt.data), rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("WriteRawJSON(%q) Content-Type = %q, want application/json",
					string(tt.data), ct)
			}
			if xcto := rec.Header().Get("X-Content-Type-Options"); xcto != "nosniff" {
				t.Errorf("WriteRawJSON(%q) X-Content-Type-Options = %q, want nosniff",
					string(tt.data), xcto)
			}
			if got := rec.Body.Bytes(); !bytes.Equal(got, tt.data) {
				t.Errorf("WriteRawJSON(%q) body = %q, want %q",
					string(tt.data), string(got), string(tt.data))
			}
		})
	}
}

func TestWriteRawJSON_does_not_panic_when_writer_fails(t *testing.T) {
	// Pins the "best-effort on write failure" contract documented in
	// the function comment: a dead client must not crash the handler.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("WriteRawJSON panicked on writer error: %v", r)
		}
	}()
	WriteRawJSON(&errWriter{}, []byte(`{"x":1}`))
}

// --- CleanupStaleTemps error-branch coverage ---

func TestCleanupStaleTemps_readdir_error_does_not_panic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits drive readdir permission")
	}
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		t.Skip("root bypasses EACCES")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "inaccessible")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("Mkdir error = %v", err)
	}
	// Drop the search bit so os.ReadDir(dir) fails with EACCES — not
	// ENOENT — exercising the Warn-log branch without deleting the dir.
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("Chmod error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("CleanupStaleTemps panicked on EACCES: %v", r)
		}
	}()
	CleanupStaleTemps(dir, time.Hour)
}

func TestCleanupStaleTemps_continues_after_remove_failure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits drive unlink permission")
	}
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		t.Skip("root bypasses directory-write EACCES")
	}
	dir := t.TempDir()

	// Two stale temps aged past the cutoff.
	blocked := filepath.Join(dir, "a.json.tmp-aaa")
	sweepable := filepath.Join(dir, "b.json.tmp-bbb")
	for _, p := range []string{blocked, sweepable} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", p, err)
		}
		old := time.Now().Add(-2 * time.Hour)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("Chtimes(%q) error = %v", p, err)
		}
	}
	// Make parent dir read-only: ReadDir still succeeds, but
	// os.Remove fails with EACCES for every entry. Both temps survive.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("Chmod error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("CleanupStaleTemps panicked on remove failure: %v", r)
		}
	}()
	CleanupStaleTemps(dir, time.Hour)

	// Restore write perms so we can stat and enumerate.
	_ = os.Chmod(dir, 0o755)
	for _, p := range []string{blocked, sweepable} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("CleanupStaleTemps removed %q despite EACCES: %v", p, err)
		}
	}
}

// --- serveJSONGet write-failure contract ---

// failWriteRecorder is an http.ResponseWriter whose Write always
// fails. httptest.ResponseRecorder's Write never errors, so this is
// required to exercise the defensive write-failure branches.
type failWriteRecorder struct {
	hdr    http.Header
	status int
}

func (f *failWriteRecorder) Header() http.Header {
	if f.hdr == nil {
		f.hdr = http.Header{}
	}
	return f.hdr
}
func (*failWriteRecorder) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}
func (f *failWriteRecorder) WriteHeader(code int) { f.status = code }

func TestServeJSONFile_GET_write_failure_on_fallback_does_not_panic(t *testing.T) {
	// Pins the best-effort contract on the fallback write error path:
	// a dead client (w.Write returns error) must not panic and must
	// not flip the status away from 200. Only a debug log is expected.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	mux := http.NewServeMux()
	ServeJSONFile(mux, path, `{"fallback":true}`, 0o644)

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	w := &failWriteRecorder{}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("serveJSONGet panicked on write failure: %v", r)
		}
	}()
	mux.ServeHTTP(w, req)

	if w.status != 0 && w.status != http.StatusOK {
		t.Errorf("status = %d, want 0 or 200 (fallback uses default 200)", w.status)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestServeJSONFile_GET_write_failure_on_data_does_not_panic(t *testing.T) {
	// Same contract, but the file exists so we exercise the successful-
	// read write-failure branch rather than the fallback branch.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"persisted":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	ServeJSONFile(mux, path, `{}`, 0o644)

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	w := &failWriteRecorder{}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("serveJSONGet panicked on persisted-data write failure: %v", r)
		}
	}()
	mux.ServeHTTP(w, req)

	if w.status != 0 && w.status != http.StatusOK {
		t.Errorf("status = %d, want 0 or 200", w.status)
	}
}

// --- FuzzDecodeJSON ---

func FuzzDecodeJSON(f *testing.F) {
	// Seed corpus: valid JSON, invalid JSON, oversize, empty, various content-types.
	f.Add("application/json", []byte(`{"key":"value"}`))
	f.Add("application/json", []byte(`{}`))
	f.Add("application/json", []byte(`{"nested":{"a":1}}`))
	f.Add("application/json", []byte(`{not json`))
	f.Add("application/json", []byte(``))
	f.Add("application/json", []byte(`null`))
	f.Add("application/json", []byte(`[1,2,3]`))
	f.Add("text/plain", []byte(`{"key":"value"}`))
	f.Add("", []byte(`{"key":"value"}`))
	f.Add("application/xml", []byte(`<xml/>`))
	f.Add("application/json", []byte{0x00, 0xff, 0xfe})

	f.Fuzz(func(t *testing.T, contentType string, body []byte) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}

		var dest map[string]any
		ok := DecodeJSON(rec, req, &dest)

		// Invariant 1: never panics (implicit — reaching here means no panic).

		// Invariant 2: exactly one HTTP response is written.
		// If ok is false, a response was written (status >= 400).
		// If ok is true, no error response was written (status remains 200).
		if !ok {
			if rec.Code == http.StatusOK || rec.Code == 0 {
				t.Errorf("DecodeJSON returned false but status = %d (expected error status)", rec.Code)
			}
			// Invariant 3: error responses have application/json Content-Type.
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("DecodeJSON error response Content-Type = %q, want application/json", ct)
			}
		}
	})
}
