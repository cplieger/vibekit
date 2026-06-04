package workspace

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzRelPath exercises RelPath with arbitrary workDir and abs
// combinations. Invariants: output never contains OS path separators
// that differ from '/', and round-trips via filepath.Rel produce the
// same result.
func FuzzRelPath(f *testing.F) {
	f.Add("/home/user/project", "/home/user/project/src/main.go")
	f.Add("/workspace", "/workspace")
	f.Add("/workspace", "/other/place")
	f.Add("/a/b/c", "/a/b/c/d/e")
	f.Add("/", "/etc/passwd")
	f.Add("/workspace", "/workspace/../escape")

	f.Fuzz(func(t *testing.T, workDir, abs string) {
		if workDir == "" || abs == "" {
			return
		}
		result, err := RelPath(workDir, abs)
		if err != nil {
			return
		}
		// Round-trip: filepath.Rel(workDir, abs) → ToSlash must equal result.
		rel, relErr := filepath.Rel(workDir, abs)
		if relErr != nil {
			t.Fatalf("filepath.Rel unexpectedly failed: %v", relErr)
		}
		expected := filepath.ToSlash(rel)
		if result != expected {
			t.Fatalf("RelPath(%q, %q) = %q, want %q", workDir, abs, result, expected)
		}
	})
}

// FuzzResolveInsideAbs exercises the production path resolver directly
// (unlike FuzzResolveInside which tests a test-only wrapper). Asserts
// that accepted paths are always absolute, clean, and inside workDir.
func FuzzResolveInsideAbs(f *testing.F) {
	f.Add("..")
	f.Add("../../../etc/passwd")
	f.Add("foo/../../../bar")
	f.Add("normal/path.txt")
	f.Add("/absolute/path")
	f.Add("symlink\x00inject")
	f.Add(strings.Repeat("a/", 200))
	f.Add(".")
	f.Add("")
	f.Add("./relative")
	f.Add("a/b/../c")

	workDir := f.TempDir()

	f.Fuzz(func(t *testing.T, input string) {
		result, err := ResolveInsideAbs(workDir, input)
		if err != nil {
			return
		}
		if !filepath.IsAbs(result) {
			t.Fatalf("result %q is not absolute", result)
		}
		if result != filepath.Clean(result) {
			t.Fatalf("result %q is not clean", result)
		}
		if err := AssertInside(result, workDir); err != nil {
			t.Fatalf("result %q escapes workDir: %v", result, err)
		}
	})
}
