package api

// Gremlins mutant-killing tests for unit vibekit-u26 (internal/api).
// Targets in httputil.go, strings.go, validate.go. All helpers/types are
// prefixed gk_vibekit_u26_ to avoid colliding with sibling units.

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- shared test fixtures ---

// gk_vibekit_u26_captureSlog swaps the default slog logger for a buffer-backed
// text handler (Debug level so Warn/Error/Debug are all captured) and restores
// the previous default on cleanup. Returns the buffer the logs land in.
func gk_vibekit_u26_captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	buf := &bytes.Buffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// gk_vibekit_u26_failWriter is an http.ResponseWriter whose Write always errors,
// so json.Encoder.Encode / w.Write return a non-nil error.
type gk_vibekit_u26_failWriter struct{ hdr http.Header }

func (f *gk_vibekit_u26_failWriter) Header() http.Header {
	if f.hdr == nil {
		f.hdr = http.Header{}
	}
	return f.hdr
}

func (f *gk_vibekit_u26_failWriter) Write([]byte) (int, error) {
	return 0, errors.New("gk_vibekit_u26_write_fail")
}

func (f *gk_vibekit_u26_failWriter) WriteHeader(int) {}

// --- httputil.go:69:46 CONDITIONALS_NEGATION (WriteJSONStatus: err != nil) ---

func Test_gk_vibekit_u26_WriteJSONStatus_logsWarnOnEncodeFail(t *testing.T) {
	buf := gk_vibekit_u26_captureSlog(t)
	WriteJSONStatus(&gk_vibekit_u26_failWriter{}, http.StatusOK, map[string]string{"k": "v"})
	// Original (err != nil) logs the warn; the negated mutant (err == nil) does not.
	if !strings.Contains(buf.String(), "json encode failed after status committed") {
		t.Errorf("WriteJSONStatus on failing writer: want encode-failed warn, got %q", buf.String())
	}
}

func Test_gk_vibekit_u26_WriteJSONStatus_noLogOnSuccess(t *testing.T) {
	buf := gk_vibekit_u26_captureSlog(t)
	rec := httptest.NewRecorder()
	WriteJSONStatus(rec, http.StatusOK, map[string]string{"k": "v"})
	// Original logs nothing on success; the negated mutant would log a warn.
	if strings.Contains(buf.String(), "json encode failed") {
		t.Errorf("WriteJSONStatus on success: want no encode-failed log, got %q", buf.String())
	}
}

// --- httputil.go:82:34 CONDITIONALS_NEGATION (WriteRawJSON: err != nil) ---

func Test_gk_vibekit_u26_WriteRawJSON_logsDebugOnWriteFail(t *testing.T) {
	buf := gk_vibekit_u26_captureSlog(t)
	WriteRawJSON(&gk_vibekit_u26_failWriter{}, []byte(`{"k":1}`))
	if !strings.Contains(buf.String(), "raw json write failed") {
		t.Errorf("WriteRawJSON on failing writer: want raw-write-failed debug, got %q", buf.String())
	}
}

func Test_gk_vibekit_u26_WriteRawJSON_noLogOnSuccess(t *testing.T) {
	buf := gk_vibekit_u26_captureSlog(t)
	rec := httptest.NewRecorder()
	WriteRawJSON(rec, []byte(`{"k":1}`))
	if strings.Contains(buf.String(), "raw json write failed") {
		t.Errorf("WriteRawJSON on success: want no raw-write-failed log, got %q", buf.String())
	}
}

// --- httputil.go:122:9 CONDITIONALS_NEGATION (InternalError: err != nil) ---

func Test_gk_vibekit_u26_InternalError_logsWhenErrPresent(t *testing.T) {
	buf := gk_vibekit_u26_captureSlog(t)
	rec := httptest.NewRecorder()
	InternalError(rec, errors.New("gk_vibekit_u26_boom"))
	// Original (err != nil) logs; the negated mutant (err == nil) skips the log.
	if !strings.Contains(buf.String(), "api: internal error") {
		t.Errorf("InternalError(err): want error log, got %q", buf.String())
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("InternalError status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func Test_gk_vibekit_u26_InternalError_noLogWhenNil(t *testing.T) {
	buf := gk_vibekit_u26_captureSlog(t)
	rec := httptest.NewRecorder()
	InternalError(rec, nil)
	// Original logs nothing when err is nil; the negated mutant would log.
	if strings.Contains(buf.String(), "api: internal error") {
		t.Errorf("InternalError(nil): want no error log, got %q", buf.String())
	}
}

// --- httputil.go:157:10 CONDITIONALS_BOUNDARY (LimitedWriter: lw.N <= 0) ---

func Test_gk_vibekit_u26_LimitedWriter_zeroCapReportsFullDrop(t *testing.T) {
	var sink bytes.Buffer
	lw := &LimitedWriter{W: &sink, N: 0}
	n, err := lw.Write([]byte("abc"))
	if err != nil {
		t.Fatalf("LimitedWriter{N:0}.Write err = %v, want nil", err)
	}
	// Original (N <= 0) drops the bytes and reports the full length.
	// The boundary mutant (N < 0) falls through, truncates to p[:0], writes 0.
	if n != 3 {
		t.Errorf("LimitedWriter{N:0}.Write(\"abc\") n = %d, want 3", n)
	}
	if sink.Len() != 0 {
		t.Errorf("LimitedWriter{N:0} wrote %d bytes to sink, want 0", sink.Len())
	}
}

// httputil.go:160:19 CONDITIONALS_BOUNDARY (int64(len(p)) > lw.N) is equivalent:
// at len(p)==lw.N the mutated `>=` truncates p[:lw.N] which is the full slice
// (a no-op), so behaviour is identical. This test only covers the strict path.
func Test_gk_vibekit_u26_LimitedWriter_truncatesOverCap(t *testing.T) {
	var sink bytes.Buffer
	lw := &LimitedWriter{W: &sink, N: 2}
	n, err := lw.Write([]byte("abcd"))
	if err != nil {
		t.Fatalf("LimitedWriter{N:2}.Write err = %v, want nil", err)
	}
	if n != 2 {
		t.Errorf("LimitedWriter{N:2}.Write(\"abcd\") n = %d, want 2", n)
	}
	if sink.String() != "ab" {
		t.Errorf("LimitedWriter{N:2} sink = %q, want %q", sink.String(), "ab")
	}
}

// --- strings.go:45:11 ARITHMETIC_BASE (StripCodeFence: s[nl+1:]) ---

func Test_gk_vibekit_u26_StripCodeFence_dropsOpeningFenceLine(t *testing.T) {
	// Original slices from nl+1 (drops the fence line). The mutant (nl-1) keeps
	// the closing backtick + newline of the opening fence.
	cases := []struct {
		name, in, want string
	}{
		{"plain fence", "```\ncontent\n```", "content"},
		{"lang fence", "```go\nfmt.Println()\n```", "fmt.Println()"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripCodeFence(tc.in); got != tc.want {
				t.Errorf("StripCodeFence(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// strings.go:44:42 CONDITIONALS_BOUNDARY (nl >= 0) is equivalent: the prefix
// guard guarantees s starts with "```", so the first newline (if any) is at
// index >= 3 and nl can never be 0 -- the boundary the mutation (`>` vs `>=`)
// would distinguish is unreachable. Coverage of the no-newline branch:
func Test_gk_vibekit_u26_StripCodeFence_noNewlineAndNoFence(t *testing.T) {
	if got := StripCodeFence("```"); got != "```" {
		t.Errorf("StripCodeFence(```) = %q, want %q", got, "```")
	}
	if got := StripCodeFence("plain text"); got != "plain text" {
		t.Errorf("StripCodeFence(plain) = %q, want %q", got, "plain text")
	}
}

// --- validate.go:53:25 CONDITIONALS_BOUNDARY (ValidChatID: len(id) > 128) ---

func Test_gk_vibekit_u26_ValidChatID_lenBoundary(t *testing.T) {
	// 128 valid chars: original (> 128 false) accepts; mutant (>= 128) rejects.
	if !ValidChatID(strings.Repeat("a", 128)) {
		t.Errorf("ValidChatID(128 'a') = false, want true")
	}
	if ValidChatID(strings.Repeat("a", 129)) {
		t.Errorf("ValidChatID(129 'a') = true, want false")
	}
}

// --- validate.go:57:50 CONDITIONALS_BOUNDARY (ValidChatID: r <= 'Z') ---

func Test_gk_vibekit_u26_ValidChatID_upperZBoundary(t *testing.T) {
	// 'Z' is the inclusive upper bound of A-Z; original accepts it, the mutant
	// (r < 'Z') drops it out of every accepted class.
	cases := []struct {
		in   string
		want bool
	}{
		{"Z", true},
		{"A", true},
		{"a", true},
		{"z", true},
		{"0", true},
		{"9", true},
		{"_", true},
		{"-", true},
		{"abcZ09_-", true},
		{" ", false},
	}
	for _, tc := range cases {
		if got := ValidChatID(tc.in); got != tc.want {
			t.Errorf("ValidChatID(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// --- validate.go:87:8 CONDITIONALS_NEGATION (ValidIdent: r != '.') ---

func Test_gk_vibekit_u26_ValidIdent_dotLogic(t *testing.T) {
	// "abc" has no dots: original returns true on the first non-dot char; the
	// negated mutant (r == '.') never returns true and falls through to false.
	cases := []struct {
		in   string
		want bool
	}{
		{"abc", true},
		{"a.b", true},
		{"model-1.2", true},
		{".hidden", false},
		{"-flag", false},
		{"...", false},
		{"", true},
	}
	for _, tc := range cases {
		if got := ValidIdent(tc.in); got != tc.want {
			t.Errorf("ValidIdent(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// --- validate.go:100:23 CONDITIONALS_BOUNDARY (ValidSessionID: len(s) > 128) ---

func Test_gk_vibekit_u26_ValidSessionID_lenBoundary(t *testing.T) {
	if !ValidSessionID(strings.Repeat("s", 128)) {
		t.Errorf("ValidSessionID(128 's') = false, want true")
	}
	if ValidSessionID(strings.Repeat("s", 129)) {
		t.Errorf("ValidSessionID(129 's') = true, want false")
	}
}

// --- validate.go:106:19 CONDITIONALS_NEGATION (ValidSessionID: s == "..") ---

func Test_gk_vibekit_u26_ValidSessionID_normalAcceptedDotDotRejected(t *testing.T) {
	// "abc" is a normal id: original accepts it; the negated mutant (s != "..")
	// makes the middle clause true for everything except "..", rejecting "abc".
	if !ValidSessionID("abc") {
		t.Errorf("ValidSessionID(\"abc\") = false, want true")
	}
	if ValidSessionID("..") {
		t.Errorf("ValidSessionID(\"..\") = true, want false")
	}
	if ValidSessionID(".") {
		t.Errorf("ValidSessionID(\".\") = true, want false")
	}
	if ValidSessionID("a/b") {
		t.Errorf("ValidSessionID(\"a/b\") = true, want false")
	}
}
