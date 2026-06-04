package cliexec

import (
	"bytes"
	"strings"
	"testing"
)

// FuzzSanitizeEnv verifies SanitizeEnv never panics and always strips
// known credential variables regardless of surrounding input.
func FuzzSanitizeEnv(f *testing.F) {
	f.Add("GH_TOKEN=secret")
	f.Add("PATH=/usr/bin")
	f.Add("GITHUB_TOKEN=abc")
	f.Add("GITLAB_TOKEN=xyz")
	f.Add("=value")
	f.Add("")
	f.Add("NOEQUALS")
	f.Add("GITEA_TOKEN=t\x00ok")

	f.Fuzz(func(t *testing.T, kv string) {
		result := SanitizeEnv([]string{kv})
		for _, out := range result {
			key := out
			if i := strings.IndexByte(out, '='); i > 0 {
				key = out[:i]
			}
			if ShouldStripEnv(key) {
				t.Fatalf("SanitizeEnv kept stripped key %q", key)
			}
		}
	})
}

// FuzzIsNotLoggedIn verifies the detector never panics and is
// case-insensitive.
func FuzzIsNotLoggedIn(f *testing.F) {
	f.Add("not logged in to any github hosts")
	f.Add("No token configured")
	f.Add("")
	f.Add("everything is fine")
	f.Add("LOGIN REQUIRED")
	f.Add("authentication required please login")

	f.Fuzz(func(t *testing.T, stderr string) {
		got := IsNotLoggedIn(stderr)
		// Invariant: if any known pattern (case-insensitive) is present,
		// result must be true.
		lower := strings.ToLower(stderr)
		for _, p := range NotLoggedInPatterns {
			if strings.Contains(lower, p) {
				if !got {
					t.Fatalf("IsNotLoggedIn(%q) = false, but contains pattern %q", stderr, p)
				}
				return
			}
		}
		// No pattern found: result must be false.
		if got {
			t.Fatalf("IsNotLoggedIn(%q) = true, but no known pattern found", stderr)
		}
	})
}

// FuzzCappedWriter verifies the writer never writes more than Max bytes
// to the underlying buffer and never returns an error from a bytes.Buffer.
func FuzzCappedWriter(f *testing.F) {
	f.Add([]byte("hello"), int64(3))
	f.Add([]byte(""), int64(0))
	f.Add([]byte("abcdef"), int64(100))
	f.Add([]byte("x"), int64(1))

	f.Fuzz(func(t *testing.T, data []byte, max int64) {
		if max < 0 {
			max = 0
		}
		var buf bytes.Buffer
		cw := &CappedWriter{W: &buf, Max: max}

		// First write.
		_, err := cw.Write(data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if int64(buf.Len()) > max {
			t.Fatalf("wrote %d bytes, exceeds max %d", buf.Len(), max)
		}

		// Second write should still respect cap.
		_, err = cw.Write(data)
		if err != nil {
			t.Fatalf("unexpected error on second write: %v", err)
		}
		if int64(buf.Len()) > max {
			t.Fatalf("after second write: %d bytes, exceeds max %d", buf.Len(), max)
		}
	})
}
