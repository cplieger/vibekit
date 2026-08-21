package forges

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// When Stderr is non-empty, Error() uses the stderr form and includes the
// stderr text (rather than falling through to the wrapped-error form).
func TestCmdError_stderrBranchIncludesStderr(t *testing.T) {
	e := &cmdError{
		CLI:      "gh",
		Args:     []string{"issue"},
		ExitCode: 1,
		Stderr:   "permission denied",
		Err:      errors.New("exit status 1"),
	}
	got := e.Error()
	if !strings.Contains(got, "permission denied") {
		t.Errorf("Error() = %q, want to contain stderr %q", got, "permission denied")
	}
}

// With an empty Stderr, Error() uses the wrapped-error form when Err is set
// and the exit-code form when Err is nil.
func TestCmdError_errBranchVsExitCode(t *testing.T) {
	withErr := &cmdError{
		CLI:      "gh",
		Args:     []string{"pr", "list"},
		ExitCode: 7,
		Stderr:   "",
		Err:      errors.New("boom-err"),
	}
	got := withErr.Error()
	if !strings.Contains(got, "boom-err") {
		t.Errorf("Error() = %q, want wrapped-err form containing %q", got, "boom-err")
	}
	if strings.Contains(got, "exit 7") {
		t.Errorf("Error() = %q, must not use exit-code form when Err is set", got)
	}

	noErr := &cmdError{
		CLI:      "gh",
		Args:     []string{"pr", "list"},
		ExitCode: 7,
		Stderr:   "",
		Err:      nil,
	}
	got2 := noErr.Error()
	if !strings.Contains(got2, "exit 7") {
		t.Errorf("Error() = %q, want exit-code form containing %q", got2, "exit 7")
	}
}

// At exactly N == Max the writer is saturated: it reports len(p) consumed
// but writes nothing to the underlying buffer.
func TestCappedWriter_saturatedReturnsLenWithoutWriting(t *testing.T) {
	var buf bytes.Buffer
	cw := &cappedWriter{W: &buf, Max: 5, N: 5}

	n, err := cw.Write([]byte("xyz"))
	if err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}
	if n != 3 {
		t.Errorf("Write n = %d, want 3 (saturated must report len(p))", n)
	}
	if buf.Len() != 0 {
		t.Errorf("buf = %q, want empty (nothing written when N==Max)", buf.String())
	}
}

// Under the cap, the data is written through and N advances.
func TestCappedWriter_writesWhenUnderCap(t *testing.T) {
	var buf bytes.Buffer
	cw := &cappedWriter{W: &buf, Max: 10, N: 0}

	n, err := cw.Write([]byte("abc"))
	if err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}
	if n != 3 {
		t.Errorf("Write n = %d, want 3", n)
	}
	if buf.String() != "abc" {
		t.Errorf("buf = %q, want %q (must write when under cap)", buf.String(), "abc")
	}
	if cw.N != 3 {
		t.Errorf("N = %d, want 3", cw.N)
	}
}

// A write larger than the remaining capacity is truncated to Max-N bytes.
func TestCappedWriter_truncatesToRemaining(t *testing.T) {
	var buf bytes.Buffer
	cw := &cappedWriter{W: &buf, Max: 5, N: 3}

	n, err := cw.Write([]byte("abcde"))
	if err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}
	if n != 2 {
		t.Errorf("Write n = %d, want 2 (remaining = Max-N = 2)", n)
	}
	if buf.String() != "ab" {
		t.Errorf("buf = %q, want %q (truncated to remaining)", buf.String(), "ab")
	}
	if cw.N != 5 {
		t.Errorf("N = %d, want 5 (3 + 2 written)", cw.N)
	}
}

// captureForgeLogs swaps the slog default to a buffer-backed debug handler for
// the duration of the test and restores it on cleanup. The default is
// process-global, so a test using it must not run in parallel — which the
// stub-PATH tests already cannot.
func captureForgeLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// The timeout is per command, and 0 means none. Both halves matter: a positive
// timeout has to actually fire, because a forge CLI that hangs would otherwise
// hang the request waiting on it; and 0 must not become an instant deadline,
// because that would fail every command before it started.
func TestRunCmd_TimeoutIsPerCommandAndZeroMeansNone(t *testing.T) {
	t.Run("a positive timeout cuts a slow command short", func(t *testing.T) {
		dir := stubPath(t)
		// The stub closes the inherited pipes before sleeping. Without that,
		// Run would block until the sleep exited whatever the deadline did —
		// the killed shell's child keeps the write end open — and the elapsed
		// time would measure the fixture rather than the timeout.
		stubCLI(t, dir, "gh", "exec >/dev/null 2>&1\nsleep 3")
		start := time.Now()

		_, err := runCmd(t.Context(), 100*time.Millisecond, nil, "gh", "pr", "list")

		if err == nil {
			t.Fatal("runCmd returned nil for a command that outlived its timeout")
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("runCmd took %v, want it cut short near its 100ms timeout", elapsed)
		}
	})

	t.Run("a zero timeout imposes no deadline", func(t *testing.T) {
		dir := stubPath(t)
		stubCLI(t, dir, "gh", `printf 'done'`)

		out, err := runCmd(t.Context(), 0, nil, "gh", "pr", "list")
		if err != nil {
			t.Fatalf("runCmd with no timeout = %v, want it to run", err)
		}
		if string(out) != "done" {
			t.Errorf("stdout = %q, want %q", out, "done")
		}
	})
}

// A command that never ran has no exit code, and -1 is how that is reported.
// Any other default would be indistinguishable from a real status: 0 reads as a
// clean success the process never had.
func TestRunCmd_ACommandThatNeverRanReportsNoExitCode(t *testing.T) {
	dir := stubPath(t)
	stubCLI(t, dir, "gh", `printf 'unreachable'`)
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // the request went away before the subprocess could start

	_, err := runCmd(ctx, CmdTimeout, nil, "gh", "pr", "list")

	var ce *cmdError
	if !errors.As(err, &ce) {
		t.Fatalf("runCmd on a cancelled context = %T (%v), want a *cmdError", err, err)
	}
	if ce.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1: the process never exited", ce.ExitCode)
	}
}
