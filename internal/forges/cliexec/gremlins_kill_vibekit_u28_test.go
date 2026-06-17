package cliexec

// Mutant-killing tests for unit vibekit-u28 (internal/forges/cliexec).
// All new identifiers are prefixed gk_vibekit_u28_ to avoid colliding
// with sibling units that may share this package.
//
// Two listed mutants are EQUIVALENT and intentionally have no kill test:
//   - cliexec.go:128:19 `if int64(len(p)) > remaining` -> `>=`: the two
//     operators differ only when len(p) == remaining, and there
//     `p = p[:remaining]` slices p to its own length (a no-op), so the
//     bytes written are identical for every input.
//   - cliexec.go:141:41 `i > 0` -> `i >= 0`: differs only when i == 0
//     (kv begins with '='); in that case the extracted key ("=..." vs
//     "") is not in the strip set, so SanitizeEnv keeps the entry
//     identically — no observable difference.

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// --- cliexec.go:37 CmdError.Error `if e.Stderr != ""` (CONDITIONALS_NEGATION) ---

// With a non-empty Stderr, Error() must use the stderr form and include
// the stderr text. The mutant `Stderr == ""` skips that branch and falls
// through to the err form, dropping the stderr text.
func Test_gk_vibekit_u28_CmdError_StderrBranchIncludesStderr(t *testing.T) {
	e := &CmdError{
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

// --- cliexec.go:40 CmdError.Error `if e.Err != nil` (CONDITIONALS_NEGATION) ---

// Reached only when Stderr is empty. With Err set, Error() must use the
// wrapped-err form; with Err nil, the exit-code form. The mutant
// `e.Err == nil` swaps the two.
func Test_gk_vibekit_u28_CmdError_ErrBranchVsExitCode(t *testing.T) {
	withErr := &CmdError{
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

	noErr := &CmdError{
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

// --- cliexec.go:124 CappedWriter.Write `if c.N >= c.Max` (CONDITIONALS_BOUNDARY / CONDITIONALS_NEGATION) ---

// At exactly N == Max the writer is saturated: it returns len(p) and
// writes nothing. Mutant `>` (boundary) and `<` (negation) both make
// `5 ? 5` false, so the writer proceeds with remaining==0 and returns 0.
func Test_gk_vibekit_u28_CappedWriter_SaturatedReturnsLenWithoutWriting(t *testing.T) {
	var buf bytes.Buffer
	cw := &CappedWriter{W: &buf, Max: 5, N: 5}

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

// The false side of line 124: when under the cap, the data IS written.
// Mutant `<` flips this and drops the data without writing.
func Test_gk_vibekit_u28_CappedWriter_WritesWhenUnderCap(t *testing.T) {
	var buf bytes.Buffer
	cw := &CappedWriter{W: &buf, Max: 10, N: 0}

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

// --- cliexec.go:127 CappedWriter.Write `remaining := c.Max - c.N` (ARITHMETIC_BASE / INVERT_NEGATIVES) ---

// remaining must be Max-N. With Max=5, N=3 only 2 more bytes fit, so a
// 5-byte write truncates to "ab". Flipping the sign (Max+N=8) skips
// truncation and writes all 5 bytes; a negative remaining panics — both
// break the assertions.
func Test_gk_vibekit_u28_CappedWriter_TruncatesToRemaining(t *testing.T) {
	var buf bytes.Buffer
	cw := &CappedWriter{W: &buf, Max: 5, N: 3}

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
