package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

// fakeCLIRunner is an in-memory CLIRunner for exercising handler logic
// without a real kiro-cli binary. It records which method the handler
// invoked so tests can assert the STDOUT-only path is used.
type fakeCLIRunner struct {
	stdout    string
	truncated bool
	runErr    error

	runCalls    int
	cappedCalls int
	gotLimit    int
	gotArgs     []string
}

var _ CLIRunner = (*fakeCLIRunner)(nil)

func (f *fakeCLIRunner) Run(context.Context, ...string) ([]byte, error) {
	f.runCalls++
	return []byte(f.stdout), f.runErr
}

func (f *fakeCLIRunner) RunStdoutCapped(_ context.Context, limit int, args ...string) ([]byte, bool, error) {
	f.cappedCalls++
	f.gotLimit = limit
	f.gotArgs = args
	if f.runErr != nil {
		return nil, false, f.runErr
	}
	out := f.stdout
	trunc := f.truncated
	if len(out) > limit {
		out = out[:limit]
		trunc = true
	}
	return []byte(out), trunc, nil
}

func postDiagnostics(t *testing.T, runner CLIRunner) *httptest.ResponseRecorder {
	t.Helper()
	s := &Server{cliRunner: runner, cliTimeouts: defaultCLITimeouts()}
	req := httptest.NewRequest(http.MethodPost, "/api/diagnostics", nil)
	rec := httptest.NewRecorder()
	s.handleDiagnostics(rec, req)
	return rec
}

// TestHandleDiagnostics_StdoutOnlyAndShape verifies the handler uses the
// STDOUT-only capped runner (never the combined stdout+stderr Run path),
// forwards the expected args + cap, and returns the {"report": ...} shape
// the client renders as text.
func TestHandleDiagnostics_StdoutOnlyAndShape(t *testing.T) {
	f := &fakeCLIRunner{stdout: `{"q-details":{"version":"1.2.3"}}`}
	rec := postDiagnostics(t, f)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if f.runCalls != 0 {
		t.Errorf("Run (combined stdout+stderr) called %d times, want 0", f.runCalls)
	}
	if f.cappedCalls != 1 {
		t.Errorf("RunStdoutCapped called %d times, want 1", f.cappedCalls)
	}
	if f.gotLimit != diagnosticsMaxBytes {
		t.Errorf("cap = %d, want %d", f.gotLimit, diagnosticsMaxBytes)
	}
	if got := strings.Join(f.gotArgs, " "); got != "diagnostic --force --format json-pretty" {
		t.Errorf("args = %q, want %q", got, "diagnostic --force --format json-pretty")
	}

	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not JSON: %v; body=%s", err, rec.Body.String())
	}
	if got["report"] != f.stdout {
		t.Errorf("report = %q, want %q", got["report"], f.stdout)
	}
	if _, ok := got["error"]; ok {
		t.Errorf("unexpected error key in success response: %v", got)
	}
}

// TestHandleDiagnostics_OversizeCappedAndMarked verifies an oversize
// diagnostics dump is capped at diagnosticsMaxBytes and marked "[truncated]".
func TestHandleDiagnostics_OversizeCappedAndMarked(t *testing.T) {
	f := &fakeCLIRunner{stdout: strings.Repeat("A", diagnosticsMaxBytes+5000)}
	rec := postDiagnostics(t, f)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	report := got["report"]
	const marker = "\n\n[truncated]"
	if !strings.HasSuffix(report, marker) {
		t.Fatalf("report missing %q suffix (len=%d)", marker, len(report))
	}
	if body := strings.TrimSuffix(report, marker); len(body) != diagnosticsMaxBytes {
		t.Errorf("capped body len = %d, want %d", len(body), diagnosticsMaxBytes)
	}
}

// TestHandleDiagnostics_NotTruncatedNoMarker verifies a small report gets
// no truncation marker.
func TestHandleDiagnostics_NotTruncatedNoMarker(t *testing.T) {
	f := &fakeCLIRunner{stdout: "small clean report"}
	rec := postDiagnostics(t, f)

	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if strings.Contains(got["report"], "[truncated]") {
		t.Errorf("unexpected truncated marker on small output: %q", got["report"])
	}
}

// TestHandleDiagnostics_ExecError verifies an exec failure returns the
// generic error envelope and no report body.
func TestHandleDiagnostics_ExecError(t *testing.T) {
	f := &fakeCLIRunner{runErr: errors.New("boom")}
	rec := postDiagnostics(t, f)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (error envelope)", rec.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if got["error"] != "diagnostic command failed" {
		t.Errorf("error = %q, want %q", got["error"], "diagnostic command failed")
	}
	if _, ok := got["report"]; ok {
		t.Errorf("report key present on error: %v", got)
	}
}

// TestHandleDiagnostics_MethodNotAllowed verifies non-POST is rejected
// before the runner is invoked.
func TestHandleDiagnostics_MethodNotAllowed(t *testing.T) {
	f := &fakeCLIRunner{stdout: "x"}
	s := &Server{cliRunner: f, cliTimeouts: defaultCLITimeouts()}
	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics", nil)
	rec := httptest.NewRecorder()
	s.handleDiagnostics(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
	if f.cappedCalls != 0 {
		t.Errorf("runner invoked on rejected method")
	}
}

// TestHandleDiagnostics_RedactsSecrets verifies obvious secret patterns in
// the diagnostics output are redacted before reaching the browser.
func TestHandleDiagnostics_RedactsSecrets(t *testing.T) {
	const (
		sessionTok = "AQoEXAMPLEsessiontokenvalue0123456789"
		awsKey     = "AKIAIOSFODNN7EXAMPLE"
		bearerTok  = "abcdefABCDEF0123456789tokenvalue"
	)
	stdout := `{
  "aws_session_token": "` + sessionTok + `",
  "note": "found ` + awsKey + ` and header Authorization: Bearer ` + bearerTok + `"
}`
	f := &fakeCLIRunner{stdout: stdout}
	rec := postDiagnostics(t, f)

	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	report := got["report"]
	for _, secret := range []string{sessionTok, awsKey, bearerTok} {
		if strings.Contains(report, secret) {
			t.Errorf("secret %q leaked into report: %s", secret, report)
		}
	}
	if !strings.Contains(report, "[redacted]") {
		t.Errorf("no [redacted] placeholder in report: %s", report)
	}
}

// TestCappedBuffer_CapsAndFlags exercises the capped-buffer cap + overflow
// tracking that RunStdoutCapped relies on for truncation detection.
func TestCappedBuffer_CapsAndFlags(t *testing.T) {
	c := &cappedBuffer{limit: 10}

	n, _ := c.Write([]byte("hello"))
	if n != 5 || c.overflow || string(c.data) != "hello" {
		t.Fatalf("under cap: n=%d overflow=%v data=%q", n, c.overflow, c.data)
	}

	// Crossing the cap keeps the prefix, reports a full write (so exec is
	// not killed by a short write), and flags overflow.
	n, _ = c.Write([]byte("world!!!!!"))
	if n != 10 {
		t.Errorf("Write must report full input length, got %d", n)
	}
	if !c.overflow {
		t.Errorf("overflow not set after crossing cap")
	}
	if string(c.data) != "helloworld" {
		t.Errorf("data = %q, want %q (capped at 10)", c.data, "helloworld")
	}

	// Writes past a full buffer are dropped without growth.
	_, _ = c.Write([]byte("more"))
	if len(c.data) != 10 {
		t.Errorf("data grew past cap: len=%d", len(c.data))
	}
}

// TestExecCLIRunner_RunStdoutCapped_SeparatesStderr verifies the production
// runner captures STDOUT only and does not merge stderr into the result.
func TestExecCLIRunner_RunStdoutCapped_SeparatesStderr(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	r := &execCLIRunner{cliPath: func() string { return sh }}
	out, truncated, err := r.RunStdoutCapped(context.Background(), 1024, "-c", "printf OUT; printf ERRLINE 1>&2")
	if err != nil {
		t.Fatalf("RunStdoutCapped: %v", err)
	}
	if truncated {
		t.Errorf("truncated = true, want false for tiny output")
	}
	if string(out) != "OUT" {
		t.Errorf("stdout = %q, want %q (stderr must not be merged)", out, "OUT")
	}
	if strings.Contains(string(out), "ERRLINE") {
		t.Errorf("stderr leaked into stdout: %q", out)
	}
}

// TestExecCLIRunner_RunStdoutCapped_Truncates verifies the production runner
// caps stdout and reports truncation.
func TestExecCLIRunner_RunStdoutCapped_Truncates(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	r := &execCLIRunner{cliPath: func() string { return sh }}
	out, truncated, err := r.RunStdoutCapped(context.Background(), 10, "-c", "printf ABCDEFGHIJKLMNOPQRST")
	if err != nil {
		t.Fatalf("RunStdoutCapped: %v", err)
	}
	if !truncated {
		t.Errorf("truncated = false, want true (20 bytes into a 10-byte cap)")
	}
	if string(out) != "ABCDEFGHIJ" {
		t.Errorf("out = %q, want prefix %q", out, "ABCDEFGHIJ")
	}
}

// TestHandleDiagnostics_TruncationCannotStrandAPartialSecret pins the
// line-boundary rule on the capped dump.
//
// The cap cuts on a BYTE boundary, and redact.Report's secret-named-field rule
// matches `"key": "value"` — it needs the CLOSING quote. So a cut landing inside a
// value leaves `"apiKey": "AAAA` unterminated, the pattern does not match, and the
// partial secret reaches the browser with its redaction anchor intact and its
// terminator gone. Trimming to the last complete line before redacting is what
// closes it; upstream hit the mirror image on a size-capped tail (KiroCrew #583).
func TestHandleDiagnostics_TruncationCannotStrandAPartialSecret(t *testing.T) {
	const secret = "SUPERSECRETTOKENVALUE0123456789"
	// The cut must land INSIDE the apiKey value, which is the only state that
	// tests anything: a cut at a line boundary leaves a well-formed document, and a
	// cut before the line removes the secret for free. So the padding is sized
	// EXACTLY, ending in a newline, and the value straddles the cap.
	const tail = 35 // bytes of the apiKey line that survive the cut
	prefix := `  "apiKey": "`
	pad := strings.Repeat("p", diagnosticsMaxBytes-tail-1) + "\n"
	stdout := pad + prefix + secret + `"`

	// Guard the fixture rather than trusting the arithmetic: the surviving slice
	// must end part-way through the secret, with no closing quote.
	survives := stdout[:diagnosticsMaxBytes]
	if !strings.HasPrefix(survives[len(pad):], prefix) || strings.HasSuffix(survives, `"`) {
		t.Fatalf("fixture does not straddle the cap: survives=%q", survives[len(pad):])
	}
	leaked := survives[len(pad)+len(prefix):]
	if len(leaked) < 12 {
		t.Fatalf("fixture leaks only %d secret chars, too few to assert on", len(leaked))
	}

	f := &fakeCLIRunner{stdout: stdout}
	rec := postDiagnostics(t, f)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	report := got["report"]
	if !strings.HasSuffix(report, "\n\n[truncated]") {
		t.Fatalf("fixture did not truncate, so it tests nothing (len=%d, cap=%d)",
			len(f.stdout), diagnosticsMaxBytes)
	}
	// Any prefix of the secret long enough to be a credential must be absent. The
	// whole partial line is dropped, so no prefix survives at all.
	// The exact prefix the cut would have stranded, plus shorter ones.
	for _, n := range []int{len(leaked), 12, 8} {
		if strings.Contains(report, secret[:n]) {
			t.Errorf("report leaked a %d-char prefix of the secret across the cap", n)
		}
	}
}
