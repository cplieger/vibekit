package fileutil

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// gk_vibekit_u29_failWriter is an http.ResponseWriter whose Write always fails,
// used to drive the write-error logging branches in serveJSONGet.
type gk_vibekit_u29_failWriter struct{ hdr http.Header }

func (f *gk_vibekit_u29_failWriter) Header() http.Header {
	if f.hdr == nil {
		f.hdr = http.Header{}
	}
	return f.hdr
}

func (f *gk_vibekit_u29_failWriter) Write([]byte) (int, error) {
	return 0, errors.New("gk_vibekit_u29 forced write error")
}

func (f *gk_vibekit_u29_failWriter) WriteHeader(int) {}

// gk_vibekit_u29_captureDebug installs a Debug-level slog handler writing to a
// buffer and restores the previous default logger on cleanup.
func gk_vibekit_u29_captureDebug(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

const (
	gk_vibekit_u29_fallbackMsg = "serveJSONFile: fallback write failed"
	gk_vibekit_u29_dataMsg     = "serveJSONFile: write failed"
)

// serve.go:64 CONDITIONALS_NEGATION (`werr != nil` -> `==`): the fallback-write
// error is logged only when the write actually fails. With a failing writer the
// original logs the message; the mutant would stay silent.
func Test_gk_vibekit_u29_fallbackWriteFails_logs(t *testing.T) {
	buf := gk_vibekit_u29_captureDebug(t)
	missing := filepath.Join(t.TempDir(), "nope.json")
	serveJSONGet(&gk_vibekit_u29_failWriter{}, missing, "cfg", `{"d":1}`)
	if !strings.Contains(buf.String(), gk_vibekit_u29_fallbackMsg) {
		t.Errorf("fallback write failed but %q not logged; log=%q", gk_vibekit_u29_fallbackMsg, buf.String())
	}
}

// serve.go:64 (other branch): with a succeeding writer the original logs
// nothing; the mutant (`werr == nil`) would log on the success path.
func Test_gk_vibekit_u29_fallbackWriteSucceeds_silent(t *testing.T) {
	buf := gk_vibekit_u29_captureDebug(t)
	missing := filepath.Join(t.TempDir(), "nope.json")
	rec := httptest.NewRecorder()
	serveJSONGet(rec, missing, "cfg", `{"d":1}`)
	if strings.Contains(buf.String(), gk_vibekit_u29_fallbackMsg) {
		t.Errorf("fallback write succeeded but %q logged; log=%q", gk_vibekit_u29_fallbackMsg, buf.String())
	}
	if rec.Body.String() != `{"d":1}` {
		t.Errorf("body = %q, want fallback %q", rec.Body.String(), `{"d":1}`)
	}
}

// serve.go:76 CONDITIONALS_NEGATION (`werr != nil` -> `==`): the data-write
// error is logged only when the write fails.
func Test_gk_vibekit_u29_dataWriteFails_logs(t *testing.T) {
	buf := gk_vibekit_u29_captureDebug(t)
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(path, []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	serveJSONGet(&gk_vibekit_u29_failWriter{}, path, "cfg", `{"d":1}`)
	if !strings.Contains(buf.String(), gk_vibekit_u29_dataMsg) {
		t.Errorf("data write failed but %q not logged; log=%q", gk_vibekit_u29_dataMsg, buf.String())
	}
}

// serve.go:76 (other branch): a succeeding writer logs nothing; the mutant
// would log on the success path.
func Test_gk_vibekit_u29_dataWriteSucceeds_silent(t *testing.T) {
	buf := gk_vibekit_u29_captureDebug(t)
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(path, []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	rec := httptest.NewRecorder()
	serveJSONGet(rec, path, "cfg", `{"d":1}`)
	if strings.Contains(buf.String(), gk_vibekit_u29_dataMsg) {
		t.Errorf("data write succeeded but %q logged; log=%q", gk_vibekit_u29_dataMsg, buf.String())
	}
	if rec.Body.String() != `{"x":1}` {
		t.Errorf("body = %q, want file content %q", rec.Body.String(), `{"x":1}`)
	}
}

// serve.go:133 CONDITIONALS_NEGATION (`perm&0o077 == 0` -> `!=`): the
// auto-created parent directory mode is 0700 when the file perm has no
// group/world bits, else 0755. umask is forced to 0 so the created mode equals
// the chosen dirPerm exactly. The mutant swaps the two outcomes.
func Test_gk_vibekit_u29_serveJSONPut_dirPerm(t *testing.T) {
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
			sub := filepath.Join(base, "gk-"+tc.name)
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
