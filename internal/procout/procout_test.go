package procout

import (
	"errors"
	"io"
	"os/exec"
	"testing"
)

func TestBuffer_KeepsEverythingUnderCap(t *testing.T) {
	b := NewBuffer(100)

	n, err := b.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}
	if n != 5 {
		t.Errorf("Write(%q) n = %d, want 5", "hello", n)
	}
	if b.String() != "hello" {
		t.Errorf("String() = %q, want %q", b.String(), "hello")
	}
	if b.Truncated() {
		t.Errorf("Truncated() = true, want false (under cap)")
	}
	if b.Len() != 5 {
		t.Errorf("Len() = %d, want 5", b.Len())
	}
}

func TestBuffer_KeepsPrefixAndFlagsTruncationAtCap(t *testing.T) {
	b := NewBuffer(3)

	n, err := b.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write err = %v, want nil", err)
	}
	// The full input length, not the kept length: os/exec's copier reads a
	// short write as io.ErrShortWrite and hands it back from cmd.Wait.
	if n != 5 {
		t.Errorf("Write(%q) n = %d, want 5 (full input length)", "hello", n)
	}
	if b.String() != "hel" {
		t.Errorf("String() = %q, want %q (capped at 3)", b.String(), "hel")
	}
	if !b.Truncated() {
		t.Errorf("Truncated() = false, want true (input crossed the cap)")
	}
}

func TestBuffer_NonPositiveCapKeepsNothingAndStillReportsFullWrite(t *testing.T) {
	for _, limit := range []int{0, -1} {
		b := NewBuffer(limit)

		n, err := b.Write([]byte("overflow"))
		if err != nil {
			t.Fatalf("NewBuffer(%d).Write err = %v, want nil", limit, err)
		}
		if n != 8 {
			t.Errorf("NewBuffer(%d).Write(%q) n = %d, want 8", limit, "overflow", n)
		}
		if b.Len() != 0 {
			t.Errorf("NewBuffer(%d) kept %q, want nothing", limit, b.String())
		}
		if !b.Truncated() {
			t.Errorf("NewBuffer(%d).Truncated() = false, want true", limit)
		}
	}
}

func TestBuffer_ZeroValueKeepsNothing(t *testing.T) {
	var b Buffer

	n, err := b.Write([]byte("x"))
	if n != 1 || err != nil {
		t.Errorf("zero-value Write = (%d, %v), want (1, nil)", n, err)
	}
	if b.Len() != 0 || !b.Truncated() {
		t.Errorf("zero-value kept %d bytes, Truncated=%v; want 0, true", b.Len(), b.Truncated())
	}
}

func TestBuffer_SequentialWritesAcrossCap(t *testing.T) {
	b := NewBuffer(5)

	if n, err := b.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("first Write = (%d, %v), want (3, nil)", n, err)
	}
	if b.Truncated() {
		t.Errorf("Truncated() after the under-cap write = true, want false")
	}
	// Straddles the cap: keeps "de", reports 5.
	if n, err := b.Write([]byte("defgh")); n != 5 || err != nil {
		t.Fatalf("straddling Write = (%d, %v), want (5, nil)", n, err)
	}
	// Past a full buffer: keeps nothing, still reports 2.
	if n, err := b.Write([]byte("ij")); n != 2 || err != nil {
		t.Fatalf("post-saturation Write = (%d, %v), want (2, nil)", n, err)
	}

	if b.String() != "abcde" {
		t.Errorf("String() = %q, want %q (cap=5, rest dropped)", b.String(), "abcde")
	}
	if b.Len() != 5 {
		t.Errorf("Len() = %d, want 5 (never grows past the cap)", b.Len())
	}
	if !b.Truncated() {
		t.Errorf("Truncated() = false, want true")
	}
}

// TestBuffer_ReachedCapIsNotACommandFailure is the regression test for the
// defect this type was extracted to fix. A capping writer that reports the
// number of bytes it KEPT makes os/exec's io.Copy return io.ErrShortWrite,
// which reaches the caller one of two ways depending on whether the child is
// still writing when the copier gives up — as a killed process ("signal:
// broken pipe") or as the copy error on a process that exited 0 ("short
// write"). Both scripts below are measured shapes of the predecessor's failure,
// so both must come back nil here.
func TestBuffer_ReachedCapIsNotACommandFailure(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh not available: %v", err)
	}

	for _, tc := range []struct {
		name          string
		script        string
		limit         int
		wantTruncated bool
		wantLen       int
	}{
		// Writes 100 bytes to stderr one at a time, so the child is still
		// writing when the cap is crossed: the predecessor's pipe closed under
		// it and the child died of SIGPIPE.
		{
			name:          "cap reached while the child is still writing",
			script:        `i=0; while [ $i -lt 100 ]; do printf x >&2; i=$((i+1)); done; exit 0`,
			limit:         10,
			wantTruncated: true,
			wantLen:       10,
		},
		// One 100-byte write then exit, so the child is usually gone before the
		// copier reports: the predecessor surfaced io.ErrShortWrite from Wait.
		{
			name:          "cap reached in a single write before exit",
			script:        `printf '%0100d' 0 >&2; exit 0`,
			limit:         10,
			wantTruncated: true,
			wantLen:       10,
		},
		{
			name:          "cap ample",
			script:        `i=0; while [ $i -lt 100 ]; do printf x >&2; i=$((i+1)); done; exit 0`,
			limit:         1000,
			wantTruncated: false,
			wantLen:       100,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBuffer(tc.limit)
			cmd := exec.CommandContext(t.Context(), sh, "-c", tc.script)
			cmd.Stderr = b

			runErr := cmd.Run()
			if runErr != nil {
				t.Fatalf("cmd.Run() = %v, want nil (the child exited 0); short write = %v",
					runErr, errors.Is(runErr, io.ErrShortWrite))
			}
			if b.Truncated() != tc.wantTruncated {
				t.Errorf("Truncated() = %v, want %v", b.Truncated(), tc.wantTruncated)
			}
			if b.Len() != tc.wantLen {
				t.Errorf("Len() = %d, want %d", b.Len(), tc.wantLen)
			}
		})
	}
}

// TestBuffer_MergedStreamsShareOneBuffer pins the shape internal/auth's logout
// handler relies on: the same *Buffer on both Stdout and Stderr, which os/exec
// serialises onto one pipe rather than racing two copiers.
func TestBuffer_MergedStreamsShareOneBuffer(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh not available: %v", err)
	}
	b := NewBuffer(64)
	cmd := exec.CommandContext(t.Context(), sh, "-c", `printf out; printf err >&2`)
	cmd.Stdout = b
	cmd.Stderr = b

	if runErr := cmd.Run(); runErr != nil {
		t.Fatalf("cmd.Run() = %v, want nil", runErr)
	}
	if got := b.String(); got != "outerr" && got != "errout" {
		t.Errorf("merged capture = %q, want both streams present", got)
	}
	if b.Truncated() {
		t.Errorf("Truncated() = true, want false")
	}
}

func FuzzBufferNeverExceedsItsCap(f *testing.F) {
	f.Add(0, []byte("hello"))
	f.Add(3, []byte("hello"))
	f.Add(100, []byte("hi"))
	f.Add(-1, []byte("x"))
	f.Add(1, []byte{})

	f.Fuzz(func(t *testing.T, limit int, p []byte) {
		b := NewBuffer(limit)

		n, err := b.Write(p)
		if err != nil {
			t.Fatalf("Write err = %v, want nil (Buffer never errors)", err)
		}
		// The os/exec contract: always the full input length.
		if n != len(p) {
			t.Fatalf("Write returned %d, want len(p)=%d", n, len(p))
		}
		if b.Len() > max(limit, 0) {
			t.Fatalf("kept %d bytes, cap was %d", b.Len(), limit)
		}
		// Truncated is exactly "some byte was dropped".
		if want := b.Len() < len(p); b.Truncated() != want {
			t.Fatalf("Truncated() = %v, want %v (kept %d of %d)",
				b.Truncated(), want, b.Len(), len(p))
		}
		if string(b.Bytes()) != string(p[:b.Len()]) {
			t.Fatalf("kept %q, want the prefix %q", b.Bytes(), p[:b.Len()])
		}
	})
}
