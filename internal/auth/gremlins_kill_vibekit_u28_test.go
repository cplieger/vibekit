package auth

// Mutant-killing tests for unit vibekit-u28 (internal/auth).
// All new identifiers are prefixed gk_vibekit_u28_ to avoid colliding
// with sibling units that may share this package.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"testing"
)

// gk_vibekit_u28_captureLogs swaps the default slog logger for a JSON
// handler writing to an in-memory buffer at the given level, runs fn,
// restores the previous default, and returns the parsed log records.
// fn must be synchronous (no background goroutines) so every record is
// flushed before parsing.
func gk_vibekit_u28_captureLogs(t *testing.T, level slog.Level, fn func()) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level})))
	defer slog.SetDefault(prev)
	fn()
	var recs []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("gk_vibekit_u28_captureLogs: bad json log line %q: %v", line, err)
		}
		recs = append(recs, m)
	}
	return recs
}

// gk_vibekit_u28_findRec returns the first record whose "msg" equals msg,
// or nil if none.
func gk_vibekit_u28_findRec(recs []map[string]any, msg string) map[string]any {
	for _, r := range recs {
		if m, _ := r["msg"].(string); m == msg {
			return r
		}
	}
	return nil
}

// gk_vibekit_u28_drainErrReader yields its canned data on the first
// reads, then returns a non-EOF error once the data is exhausted. Used
// to force io.Copy in scanLoginOutputWithDrain to return a non-nil
// error after the URL line has been consumed by scanLoginOutput.
type gk_vibekit_u28_drainErrReader struct {
	err  error
	data []byte
	pos  int
}

func (r *gk_vibekit_u28_drainErrReader) Read(p []byte) (int, error) {
	if r.pos < len(r.data) {
		n := copy(p, r.data[r.pos:])
		r.pos += n
		return n, nil
	}
	return 0, r.err
}

func (r *gk_vibekit_u28_drainErrReader) Close() error { return nil }

// --- auth.go:147 stderrAttr `if s == ""` (CONDITIONALS_NEGATION) ---

// Empty stderr -> nil; non-empty stderr -> ["stderr", text]. The mutant
// `s != ""` inverts both, so each branch's exact shape pins the operator.
func Test_gk_vibekit_u28_StderrAttr_EmptyVsNonEmpty(t *testing.T) {
	var empty bytes.Buffer
	if got := stderrAttr(&empty); got != nil {
		t.Errorf("stderrAttr(empty) = %v, want nil", got)
	}

	var nonEmpty bytes.Buffer
	nonEmpty.WriteString("boom")
	got := stderrAttr(&nonEmpty)
	if len(got) != 2 {
		t.Fatalf("stderrAttr(non-empty) len = %d, want 2; got=%v", len(got), got)
	}
	if got[0] != "stderr" {
		t.Errorf("stderrAttr(non-empty)[0] = %v, want %q", got[0], "stderr")
	}
	if got[1] != "boom" {
		t.Errorf("stderrAttr(non-empty)[1] = %v, want %q", got[1], "boom")
	}
}

// --- auth_login.go:27 validateProvider `len(v) > maxProviderLen` (CONDITIONALS_BOUNDARY) ---

// A valid https URL of length exactly maxProviderLen must pass: with `>`
// it is not "too long"; the mutant `>=` rejects it.
func Test_gk_vibekit_u28_ValidateProvider_BoundaryLengthExactlyMax(t *testing.T) {
	prefix := "https://example.com/"
	v := prefix + strings.Repeat("a", maxProviderLen-len(prefix))
	if len(v) != maxProviderLen {
		t.Fatalf("test setup: len(v) = %d, want %d", len(v), maxProviderLen)
	}
	if err := validateProvider(v); err != nil {
		t.Errorf("validateProvider(len==%d valid https) = %v, want nil", maxProviderLen, err)
	}
}

// --- auth_login.go:52 validateRegion `len(v) > maxRegionLen` (CONDITIONALS_BOUNDARY) ---

// A valid region of length exactly maxRegionLen must pass: with `>` it is
// not "too long"; the mutant `>=` rejects it before the regex check.
func Test_gk_vibekit_u28_ValidateRegion_BoundaryLengthExactlyMax(t *testing.T) {
	// "aa-" + 27 lowercase letters + "-1" == 32 chars, matches awsRegionRe.
	v := "aa-" + strings.Repeat("b", maxRegionLen-5) + "-1"
	if len(v) != maxRegionLen {
		t.Fatalf("test setup: len(v) = %d, want %d", len(v), maxRegionLen)
	}
	if err := validateRegion(v); err != nil {
		t.Errorf("validateRegion(len==%d valid region) = %v, want nil", maxRegionLen, err)
	}
}

// --- auth_login_process.go:129 scanLoginOutputWithDrain `if err != nil` (CONDITIONALS_NEGATION) ---

// When io.Copy on the drain returns a non-nil (non-EOF) error, the
// `err != nil` branch logs "login: stdout drain stopped". The mutant
// `err == nil` suppresses that log on this path.
func Test_gk_vibekit_u28_ScanLoginOutputWithDrain_LogsOnDrainError(t *testing.T) {
	r := &gk_vibekit_u28_drainErrReader{
		data: []byte("Open this URL: https://example.com/auth\n"),
		err:  errors.New("gk_vibekit_u28 drain boom"),
	}
	ch := make(chan map[string]string, 1)
	recs := gk_vibekit_u28_captureLogs(t, slog.LevelDebug, func() {
		scanLoginOutputWithDrain(r, ch)
	})

	got := <-ch
	if got["url"] != "https://example.com/auth" {
		t.Fatalf("url = %q, want https://example.com/auth", got["url"])
	}
	if gk_vibekit_u28_findRec(recs, "login: stdout drain stopped") == nil {
		t.Errorf("expected debug log %q on drain error; logs=%v",
			"login: stdout drain stopped", recs)
	}
}

// --- auth_login_process.go:180 scanLoginOutput `"has_code", code != ""` (CONDITIONALS_NEGATION) ---

// The "login: auth URL extracted" log records has_code = (code != "").
// With a Code: line has_code must be true; without one, false. The
// mutant `code == ""` inverts both.
func Test_gk_vibekit_u28_ScanLoginOutput_HasCodeLogAttribute(t *testing.T) {
	withCode := gk_vibekit_u28_captureLogs(t, slog.LevelInfo, func() {
		ch := make(chan map[string]string, 1)
		scanLoginOutput(strings.NewReader(
			"Code: ABCD-1234\nOpen this URL: https://idp.example.com/\n"), ch)
	})
	rc := gk_vibekit_u28_findRec(withCode, "login: auth URL extracted")
	if rc == nil {
		t.Fatalf("no 'auth URL extracted' log (with code); logs=%v", withCode)
	}
	if hc, _ := rc["has_code"].(bool); !hc {
		t.Errorf("has_code = %v, want true (Code: line present)", rc["has_code"])
	}

	noCode := gk_vibekit_u28_captureLogs(t, slog.LevelInfo, func() {
		ch := make(chan map[string]string, 1)
		scanLoginOutput(strings.NewReader(
			"Open this URL: https://idp.example.com/\n"), ch)
	})
	rn := gk_vibekit_u28_findRec(noCode, "login: auth URL extracted")
	if rn == nil {
		t.Fatalf("no 'auth URL extracted' log (no code); logs=%v", noCode)
	}
	if hc, _ := rn["has_code"].(bool); hc {
		t.Errorf("has_code = %v, want false (no Code: line)", rn["has_code"])
	}
}

// --- auth_login_process.go:187 scanLoginOutput `if lineCount >= maxLoginLines` (CONDITIONALS_BOUNDARY) ---

// Exactly maxLoginLines lines of URL-free output must trip the line cap.
// Original `>=` fires at line N==maxLoginLines; the mutant `>` lets the
// loop run dry and reports the generic "no auth URL found" instead.
func Test_gk_vibekit_u28_ScanLoginOutput_LineCapBoundaryExactlyMax(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxLoginLines; i++ {
		fmt.Fprintf(&b, "noise line %d\n", i)
	}
	ch := make(chan map[string]string, 1)
	scanLoginOutput(strings.NewReader(b.String()), ch)
	got := <-ch
	if got["error"] != "CLI produced too much output without auth URL" {
		t.Errorf("scanLoginOutput(exactly %d lines) error = %q, want %q",
			maxLoginLines, got["error"], "CLI produced too much output without auth URL")
	}
}

// --- auth_logout.go:109 killLoginProcess `if err == nil` (CONDITIONALS_NEGATION) ---

// After the subprocess is reaped, loginKill returns ESRCH (non-nil), so
// the `err == nil` early-return is NOT taken and the ESRCH branch logs
// "login: kill group no-op (already reaped)". The mutant `err != nil`
// returns immediately and emits no log.
func Test_gk_vibekit_u28_KillLoginProcess_ReapedLogsAlreadyReaped(t *testing.T) {
	skipIfNotUnix(t)
	cmd := exec.Command("/bin/true")
	setLoginProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	recs := gk_vibekit_u28_captureLogs(t, slog.LevelDebug, func() {
		killLoginProcess(cmd)
	})
	if gk_vibekit_u28_findRec(recs, "login: kill group no-op (already reaped)") == nil {
		t.Errorf("expected debug log %q for reaped process; logs=%v",
			"login: kill group no-op (already reaped)", recs)
	}
}
