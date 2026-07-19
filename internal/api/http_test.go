package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Tests for httputil.go: JSON writers, LimitBody, the LimitedWriter cap,
// WriteRawJSON; plus sanitize.go's StripANSI/SanitizeUnicode/SanitizeOutput
// and FuzzDecodeJSON.

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

// --- SanitizeUnicode / isHiddenUnicode / SanitizeOutput ---

func TestIsHiddenUnicode_classifies_every_branch(t *testing.T) {
	tests := []struct {
		name string
		r    rune
		want bool
	}{
		{"ascii letter", 'a', false},
		{"ascii digit", '0', false},
		{"ascii space", ' ', false},
		{"accented latin", 'é', false},
		{"cjk", '漢', false},
		{"emoji", '😀', false},
		{"tab (visible whitespace)", '\t', false},
		{"newline", '\n', false},
		{"TAG block lower bound", 0xE0000, true},
		{"TAG mid", 0xE0040, true},
		{"TAG upper bound", 0xE007F, true},
		{"just below TAG block", 0xDFFFF, false},
		{"just above TAG block", 0xE0080, false},
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
		{"bidi ALM singleton", 0x061C, true},
		{"bidi embedding lower", 0x202A, true},
		{"bidi embedding mid", 0x202C, true},
		{"bidi embedding upper", 0x202E, true},
		{"just below bidi embedding", 0x2029, false},
		{"just above bidi embedding", 0x202F, false},
		{"bidi isolate lower", 0x2066, true},
		{"bidi isolate mid", 0x2067, true},
		{"bidi isolate upper", 0x2069, true},
		{"just below bidi isolate", 0x2065, false},
		{"just above bidi isolate", 0x206A, false},
		{"NUL (not stripped)", '\x00', false},
		{"nbsp (visible whitespace, must not be stripped)", 0x00A0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isHiddenUnicode(tt.r); got != tt.want {
				t.Errorf("isHiddenUnicode(U+%04X) = %v, want %v", tt.r, got, tt.want)
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
				t.Errorf("SanitizeUnicode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeUnicode_is_idempotent(t *testing.T) {
	inputs := []string{
		"", "plain text", "a\u200Bb\u200Cc", "\uFEFFhello",
		"visible\U000E0041\U000E0062hidden", "café\u00AD résumé",
		"\u202Abidi\u202E", "\u200B\u200C\u200D\u2060",
	}
	for _, in := range inputs {
		once := SanitizeUnicode(in)
		twice := SanitizeUnicode(once)
		if once != twice {
			t.Errorf("SanitizeUnicode not idempotent: %q → %q → %q", in, once, twice)
		}
	}
}

func TestSanitizeOutput_composes_strip_ANSI_then_unicode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain text passthrough", "hello world", "hello world"},
		{"ansi only", "\x1b[31mred\x1b[0m", "red"},
		{"hidden unicode only", "a\u200Bb\uFEFFc", "abc"},
		{"both ansi and hidden unicode", "\x1b[1;32mclean\u200Btext\x1b[0m\uFEFFafter", "cleantextafter"},
		{"hidden unicode inside ANSI payload is removed", "\x1b[31mred\u200Btext\x1b[0m", "redtext"},
		{"TAG characters embedded between ANSI runs", "\x1b[32mA\x1b[0m\U000E0041\x1b[31mB\x1b[0m", "AB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeOutput(tt.in)
			if got != tt.want {
				t.Errorf("SanitizeOutput(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeOutput_is_idempotent(t *testing.T) {
	inputs := []string{
		"", "plain", "\x1b[31mred\x1b[0m", "a\u200Bb",
		"\x1b[1;32mgreen\u200Btext\x1b[0m\uFEFFtail",
		"visible\U000E0041\x1b[0m\u200Btext",
	}
	for _, in := range inputs {
		once := SanitizeOutput(in)
		twice := SanitizeOutput(once)
		if once != twice {
			t.Errorf("SanitizeOutput not idempotent: %q → %q → %q", in, once, twice)
		}
	}
}

func TestSanitizeOutput_strips_ansi_before_unicode(t *testing.T) {
	tricky := []string{
		"\x1b[31m\u200Bred\u200B\x1b[0m",
		"\x1b]0;title\u200B\x07rest",
		"before\x1b[1;32m\uFEFFbold\x1b[0mafter",
	}
	for _, in := range tricky {
		got := SanitizeOutput(in)
		want := SanitizeUnicode(StripANSI(in))
		if got != want {
			t.Errorf("SanitizeOutput(%q) = %q, want SanitizeUnicode(StripANSI(_)) = %q", in, got, want)
		}
	}
}

// --- WriteJSONStatus encode failure ---

func TestWriteJSONStatus_encode_failure_logs_and_does_not_panic(t *testing.T) {
	buf := captureSlog(t)
	rec := httptest.NewRecorder()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("WriteJSONStatus panicked on non-marshalable value: %v", r)
		}
	}()
	WriteJSONStatus(rec, http.StatusAccepted, make(chan int))
	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// The encode error after the status is committed is logged at Warn.
	if !strings.Contains(buf.String(), "json encode failed after status committed") {
		t.Errorf("encode failure: want warn log, got %q", buf.String())
	}
}

func TestWriteJSONStatus_no_log_on_success(t *testing.T) {
	buf := captureSlog(t)
	WriteJSONStatus(httptest.NewRecorder(), http.StatusOK, map[string]string{"k": "v"})
	if strings.Contains(buf.String(), "json encode failed") {
		t.Errorf("clean encode: want no log, got %q", buf.String())
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

// --- FuzzDecodeJSON ---

func FuzzDecodeJSON(f *testing.F) {
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

		if !ok {
			if rec.Code == http.StatusOK || rec.Code == 0 {
				t.Errorf("DecodeJSON returned false but status = %d (expected error status)", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("DecodeJSON error response Content-Type = %q, want application/json", ct)
			}
		}
	})
}

// --- InternalError log path ---

func TestInternalError_logs_cause_when_nonnil(t *testing.T) {
	buf := captureSlog(t)
	rec := httptest.NewRecorder()
	InternalError(rec, os.ErrPermission)
	if !strings.Contains(buf.String(), "api: internal error") {
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
	if strings.Contains(buf.String(), "api: internal error") {
		t.Errorf("InternalError(nil): want no error log, got %q", buf.String())
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("InternalError(nil) status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// --- LimitedWriter cap ---

func TestLimitedWriter_zero_cap_drops_and_reports_full_length(t *testing.T) {
	var sink bytes.Buffer
	lw := &LimitedWriter{W: &sink, N: 0}
	n, err := lw.Write([]byte("abc"))
	if err != nil {
		t.Fatalf("LimitedWriter{N:0}.Write err = %v, want nil", err)
	}
	// A non-positive cap drops the bytes but reports the full length so callers
	// treating a short write as an error don't trip.
	if n != 3 {
		t.Errorf("LimitedWriter{N:0}.Write(%q) n = %d, want 3", "abc", n)
	}
	if sink.Len() != 0 {
		t.Errorf("LimitedWriter{N:0} wrote %d bytes to sink, want 0", sink.Len())
	}
}

func TestLimitedWriter_truncates_over_cap(t *testing.T) {
	var sink bytes.Buffer
	lw := &LimitedWriter{W: &sink, N: 2}
	n, err := lw.Write([]byte("abcd"))
	if err != nil {
		t.Fatalf("LimitedWriter{N:2}.Write err = %v, want nil", err)
	}
	if n != 2 {
		t.Errorf("LimitedWriter{N:2}.Write(%q) n = %d, want 2", "abcd", n)
	}
	if sink.String() != "ab" {
		t.Errorf("LimitedWriter{N:2} sink = %q, want %q", sink.String(), "ab")
	}
}
