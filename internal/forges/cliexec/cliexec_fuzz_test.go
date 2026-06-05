package cliexec

import (
	"bytes"
	"strings"
	"testing"
)

func FuzzSanitizeEnv(f *testing.F) {
	f.Add("PATH=/usr/bin\nGH_TOKEN=secret\nHOME=/home/u")
	f.Add("GITHUB_TOKEN=x\nGITLAB_TOKEN=y")
	f.Add("")

	f.Fuzz(func(t *testing.T, joined string) {
		env := strings.Split(joined, "\n")
		result := SanitizeEnv(env)
		for _, kv := range result {
			key := kv
			if i := strings.IndexByte(kv, '='); i > 0 {
				key = kv[:i]
			}
			if ShouldStripEnv(key) {
				t.Fatalf("SanitizeEnv leaked %q", key)
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
		got := IsNotLoggedIn(stderr)
		if !got {
			return
		}
		lower := strings.ToLower(stderr)
		found := false
		for _, p := range NotLoggedInPatterns {
			if strings.Contains(lower, p) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("IsNotLoggedIn=true but no pattern found in %q", stderr)
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
		cw := &CappedWriter{W: &buf, Max: max}
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
