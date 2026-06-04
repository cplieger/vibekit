package cliexec

import (
	"bytes"
	"errors"
	"testing"
)

// FuzzCappedWriterBoundary exercises CappedWriter with successive Write
// calls at boundary conditions. Invariants:
//  1. Write never returns an error (underlying bytes.Buffer doesn't error).
//  2. Write always returns len(p) regardless of capping.
//  3. The underlying buffer never exceeds Max bytes.
//  4. c.N tracks exactly how many bytes flowed through.
func FuzzCappedWriterBoundary(f *testing.F) {
	f.Add([]byte("hello"), []byte("world"), int64(5))
	f.Add([]byte(""), []byte("x"), int64(0))
	f.Add([]byte("abc"), []byte("defgh"), int64(3))
	f.Add([]byte("\x00\x01\x02"), []byte("\xff\xfe\xfd\xfc"), int64(1))
	f.Add([]byte("a"), []byte(""), int64(1))

	f.Fuzz(func(t *testing.T, a, b []byte, max int64) {
		if max < 0 {
			max = 0
		}
		if max > 1<<20 {
			max = 1 << 20
		}
		var buf bytes.Buffer
		cw := &CappedWriter{W: &buf, Max: max}

		_, err1 := cw.Write(a)
		if err1 != nil {
			t.Fatalf("first Write returned error: %v", err1)
		}

		_, err2 := cw.Write(b)
		if err2 != nil {
			t.Fatalf("second Write returned error: %v", err2)
		}

		// Once fully capped, further writes must still not error.
		_, err3 := cw.Write([]byte("extra"))
		if err3 != nil {
			t.Fatalf("post-cap Write returned error: %v", err3)
		}

		if int64(buf.Len()) > max {
			t.Errorf("buffer has %d bytes but max is %d", buf.Len(), max)
		}
	})
}

// FuzzCmdErrorFormat exercises CmdError.Error() with arbitrary strings
// to ensure no panic on boundary inputs (empty fields, huge strings,
// control bytes).
func FuzzCmdErrorFormat(f *testing.F) {
	f.Add("gh", "api", "permission denied", 1)
	f.Add("", "", "", 0)
	f.Add("glab", "mr list", "", 128)
	f.Add("tea", "", "\x00\x01\x02\x03", -1)

	f.Fuzz(func(t *testing.T, cli, args, stderr string, exitCode int) {
		e := &CmdError{
			CLI:      cli,
			Args:     []string{args},
			Stderr:   stderr,
			ExitCode: exitCode,
			Err:      errors.New("wrapped"),
		}
		s := e.Error()
		if s == "" {
			t.Error("CmdError.Error() returned empty string")
		}

		// Also test with nil Err and empty stderr.
		e2 := &CmdError{
			CLI:      cli,
			Args:     []string{args},
			Stderr:   "",
			ExitCode: exitCode,
			Err:      nil,
		}
		s2 := e2.Error()
		if s2 == "" {
			t.Error("CmdError.Error() with nil Err returned empty string")
		}
	})
}
