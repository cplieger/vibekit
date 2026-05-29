package fileutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func TestSaveBytes_RapidRoundTrip(t *testing.T) {
	dir := t.TempDir()
	var counter int
	rapid.Check(t, func(t *rapid.T) {
		data := rapid.SliceOf(rapid.Byte()).Draw(t, "data")
		// Use a restricted set of valid file permissions.
		perm := rapid.SampledFrom([]os.FileMode{0o600, 0o644, 0o755}).Draw(t, "perm")

		counter++
		path := filepath.Join(dir, fmt.Sprintf("testfile-%d", counter))

		if err := SaveBytes(path, data, perm); err != nil {
			t.Fatalf("SaveBytes: %v", err)
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}

		if len(data) == 0 && len(got) == 0 {
			return // both empty, OK
		}
		if string(got) != string(data) {
			t.Fatalf("round-trip mismatch: wrote %d bytes, read %d bytes", len(data), len(got))
		}

		// Verify no stale .tmp- files remain.
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".tmp-") {
				t.Errorf("stale temp file found: %s", e.Name())
			}
		}
	})
}
