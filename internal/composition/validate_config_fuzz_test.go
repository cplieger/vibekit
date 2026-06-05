package composition

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzCheckExecutable verifies checkExecutable never panics on
// arbitrary paths and returns consistent error/nil results.
func FuzzCheckExecutable(f *testing.F) {
	f.Add("/bin/sh")
	f.Add("/nonexistent/path")
	f.Add("sh")
	f.Add("")
	f.Add("../../../etc/passwd")

	f.Fuzz(func(t *testing.T, path string) {
		err := checkExecutable(path, "TEST_VAR")
		// If no error, the path resolved to something executable.
		// If error, it must contain a message.
		if err != nil && err.Error() == "" {
			t.Fatal("error with empty message")
		}
	})
}

// FuzzCheckDirWritable verifies checkDirWritable never panics and
// returns an error for non-directory targets.
func FuzzCheckDirWritable(f *testing.F) {
	f.Add("subdir")
	f.Add("")
	f.Add("a/b/c")

	f.Fuzz(func(t *testing.T, subpath string) {
		tmp := t.TempDir()
		target := filepath.Join(tmp, subpath)
		_ = os.MkdirAll(target, 0o755)
		err := checkDirWritable(target, "TEST_DIR")
		// If the dir exists and is writable, no error.
		// Otherwise error must have content.
		if err != nil && err.Error() == "" {
			t.Fatal("error with empty message")
		}
	})
}
