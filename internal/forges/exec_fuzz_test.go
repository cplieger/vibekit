package forges

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func FuzzSanitizeEnv(f *testing.F) {
	f.Add("PATH=/usr/bin\nGH_TOKEN=secret\nHOME=/home/u")
	f.Add("GITHUB_TOKEN=x\nGITLAB_TOKEN=y")
	f.Add("")

	f.Fuzz(func(t *testing.T, joined string) {
		env := strings.Split(joined, "\n")
		result := sanitizeEnv(env)
		for _, kv := range result {
			key := kv
			if i := strings.IndexByte(kv, '='); i > 0 {
				key = kv[:i]
			}
			if shouldStripEnv(key) {
				t.Fatalf("sanitizeEnv leaked %q", key)
			}
		}
	})
}

func FuzzIsNotLoggedIn(f *testing.F) {
	f.Add("error: not logged in to github.com")
	f.Add("no token configured for host")
	f.Add("all good")
	f.Add("")

	f.Fuzz(func(t *testing.T, stderr string) {
		got := isNotLoggedIn(stderr)
		if !got {
			return
		}
		lower := strings.ToLower(stderr)
		found := false
		for _, p := range notLoggedInPatterns {
			if strings.Contains(lower, p) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("isNotLoggedIn=true but no pattern found in %q", stderr)
		}
	})
}

func FuzzCappedWriter(f *testing.F) {
	f.Add([]byte("hello world"), int64(5))
	f.Add([]byte(""), int64(0))
	f.Add([]byte("abc"), int64(100))

	f.Fuzz(func(t *testing.T, data []byte, max int64) {
		if max < 0 {
			max = 0
		}
		var buf bytes.Buffer
		cw := &cappedWriter{W: &buf, Max: max}
		n, err := cw.Write(data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n < 0 {
			t.Fatalf("Write returned negative: %d", n)
		}
		if int64(buf.Len()) > max {
			t.Fatalf("buffer exceeded cap: %d > %d", buf.Len(), max)
		}
	})
}

// FuzzCappedWriterBoundary exercises cappedWriter with successive Write
// calls at boundary conditions. Invariants:
//  1. Write never returns an error (underlying bytes.Buffer doesn't error).
//  2. The underlying buffer never exceeds Max bytes across writes.
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
		cw := &cappedWriter{W: &buf, Max: max}

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

// FuzzCmdErrorFormat exercises cmdError.Error() with arbitrary strings
// to ensure no panic on boundary inputs (empty fields, control bytes)
// and that it never returns an empty string.
func FuzzCmdErrorFormat(f *testing.F) {
	f.Add("gh", "api", "permission denied", 1)
	f.Add("", "", "", 0)
	f.Add("glab", "mr list", "", 128)
	f.Add("tea", "", "\x00\x01\x02\x03", -1)

	f.Fuzz(func(t *testing.T, cli, args, stderr string, exitCode int) {
		e := &cmdError{
			CLI:      cli,
			Args:     []string{args},
			Stderr:   stderr,
			ExitCode: exitCode,
			Err:      errors.New("wrapped"),
		}
		if s := e.Error(); s == "" {
			t.Error("cmdError.Error() returned empty string")
		}

		// Also exercise the nil-Err, empty-stderr (exit-code) form.
		e2 := &cmdError{
			CLI:      cli,
			Args:     []string{args},
			Stderr:   "",
			ExitCode: exitCode,
			Err:      nil,
		}
		if s2 := e2.Error(); s2 == "" {
			t.Error("cmdError.Error() with nil Err returned empty string")
		}
	})
}
