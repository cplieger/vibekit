package fileutil

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

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

	padding := strings.Repeat("a", api.MaxJSONBody+1)
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
	if resp["error"] != "read failed" {
		t.Errorf("error = %q, want %q", resp["error"], "read failed")
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
	if resp["error"] != "save failed" {
		t.Errorf("error = %q, want %q", resp["error"], "save failed")
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
		{"application/json with charset accepted", "application/json; charset=utf-8", `"ok":true`, http.StatusOK},
		{"text/plain rejected", "text/plain", `"expected application/json"`, http.StatusBadRequest},
		{"text/html rejected", "text/html; charset=utf-8", `"expected application/json"`, http.StatusBadRequest},
		{"application/xml rejected", "application/xml", `"expected application/json"`, http.StatusBadRequest},
		{"multipart/form-data rejected", "multipart/form-data; boundary=xyz", `"expected application/json"`, http.StatusBadRequest},
		{"application/json-patch+json rejected", "application/json-patch+json", `"expected application/json"`, http.StatusBadRequest},
		{"application/json5 rejected", "application/json5", `"expected application/json"`, http.StatusBadRequest},
		{"malformed content-type rejected", "application/json;;;charset=", `"expected application/json"`, http.StatusBadRequest},
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

// failWriteRecorder is an http.ResponseWriter whose Write always fails.
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
func (*failWriteRecorder) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (f *failWriteRecorder) WriteHeader(code int)    { f.status = code }

// captureDebugLog redirects the default slog logger to a buffer at Debug
// level for the duration of the test, restoring the previous logger on
// cleanup. Returns the buffer so callers can assert on emitted records.
func captureDebugLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// A failing ResponseWriter on the not-found fallback path must log the
// write error (and never panic). Driving the branch through a writer
// that always errors pins the `werr != nil` guard: a negated guard would
// stay silent here.
func TestServeJSONFile_GET_logs_when_fallback_write_fails(t *testing.T) {
	logs := captureDebugLog(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json") // does not exist -> fallback write path
	mux := http.NewServeMux()
	ServeJSONFile(mux, path, `{"fallback":true}`, 0o644)

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	mux.ServeHTTP(&failWriteRecorder{}, req)

	if !strings.Contains(logs.String(), "serveJSONFile: fallback write failed") {
		t.Errorf("fallback write failed but was not logged; log=%q", logs.String())
	}
}

// A failing ResponseWriter on the file-data path must log the write
// error (and never panic), pinning the data-write `werr != nil` guard.
func TestServeJSONFile_GET_logs_when_data_write_fails(t *testing.T) {
	logs := captureDebugLog(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"persisted":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	ServeJSONFile(mux, path, `{}`, 0o644)

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	mux.ServeHTTP(&failWriteRecorder{}, req)

	if !strings.Contains(logs.String(), "serveJSONFile: write failed") {
		t.Errorf("data write failed but was not logged; log=%q", logs.String())
	}
}

// PUT auto-creates a missing parent directory, deriving its mode from the
// file perm: 0700 when the file perm has no group/world bits, else 0755.
// umask is forced to 0 so the created mode equals the chosen dirPerm exactly.
func TestServeJSONFile_PUT_creates_parent_dir_with_derived_mode(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	cases := []struct {
		name     string
		perm     os.FileMode
		wantMode os.FileMode
	}{
		{"perm0600_dir0700", 0o600, 0o700},
		{"perm0644_dir0755", 0o644, 0o755},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			sub := filepath.Join(base, "created-"+tc.name)
			path := filepath.Join(sub, "config.json")
			req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"a":1}`))
			rec := httptest.NewRecorder()
			var mu sync.Mutex

			serveJSONPut(rec, req, path, "config", &mu, tc.perm)

			if rec.Code != http.StatusOK {
				t.Fatalf("serveJSONPut code = %d, want 200; body=%q", rec.Code, rec.Body.String())
			}
			info, err := os.Stat(sub)
			if err != nil {
				t.Fatalf("stat created parent dir: %v", err)
			}
			if got := info.Mode().Perm(); got != tc.wantMode {
				t.Errorf("created dir mode = %#o, want %#o (file perm=%#o)", got, tc.wantMode, tc.perm)
			}
		})
	}
}
