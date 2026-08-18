package forges

import (
	"bytes"
	"errors"
	"strings"
	"testing"
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
