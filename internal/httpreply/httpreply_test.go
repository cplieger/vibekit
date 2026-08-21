package httpreply

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Tests for httpreply.go: vibekit's bare {"error":…} taxonomy, WriteRawJSON, and
// the two logged error paths. The mechanism the helpers sit on (headers, status,
// encode, body cap) is webhttp's and is tested there.

// captureSlog swaps the default slog logger for a buffer-backed text handler
// (Debug level, so Warn/Error/Debug are all captured) and restores the previous
// default on cleanup. Tests using it must not call t.Parallel: it mutates the
// process-wide default logger.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	buf := &bytes.Buffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// --- JSON helpers ---

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
			call:     func(w http.ResponseWriter) { MethodNotAllowed(w, http.MethodPost) },
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

// --- WriteRawJSON pass-through contract ---

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
				t.Errorf("WriteRawJSON(%q) status = %d, want 200", string(tt.data), rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("WriteRawJSON(%q) Content-Type = %q, want application/json", string(tt.data), ct)
			}
			if xcto := rec.Header().Get("X-Content-Type-Options"); xcto != "nosniff" {
				t.Errorf("WriteRawJSON(%q) X-Content-Type-Options = %q, want nosniff", string(tt.data), xcto)
			}
			if got := rec.Body.Bytes(); !bytes.Equal(got, tt.data) {
				t.Errorf("WriteRawJSON(%q) body = %q, want %q", string(tt.data), string(got), string(tt.data))
			}
		})
	}
}

func TestWriteRawJSON_write_failure_logs_and_does_not_panic(t *testing.T) {
	buf := captureSlog(t)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("WriteRawJSON panicked on writer error: %v", r)
		}
	}()
	WriteRawJSON(&errWriter{}, []byte(`{"x":1}`))
	// A best-effort write failure is logged at Debug.
	if !strings.Contains(buf.String(), "raw json write failed") {
		t.Errorf("write failure: want debug log, got %q", buf.String())
	}
}

func TestWriteRawJSON_no_log_on_success(t *testing.T) {
	buf := captureSlog(t)
	WriteRawJSON(httptest.NewRecorder(), []byte(`{"k":1}`))
	if strings.Contains(buf.String(), "raw json write failed") {
		t.Errorf("clean write: want no log, got %q", buf.String())
	}
}

// --- InternalError log path ---

func TestInternalError_logs_cause_when_nonnil(t *testing.T) {
	buf := captureSlog(t)
	rec := httptest.NewRecorder()
	InternalError(rec, os.ErrPermission)
	if !strings.Contains(buf.String(), "httpreply: internal error") {
		t.Errorf("InternalError(err): want error log, got %q", buf.String())
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("InternalError status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestInternalError_does_not_log_when_nil(t *testing.T) {
	buf := captureSlog(t)
	rec := httptest.NewRecorder()
	InternalError(rec, nil)
	if strings.Contains(buf.String(), "httpreply: internal error") {
		t.Errorf("InternalError(nil): want no error log, got %q", buf.String())
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("InternalError(nil) status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
