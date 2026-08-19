package workspace

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/pathinside/v2"
)

func FuzzResolveInside(f *testing.F) {
	f.Add("..")
	f.Add("../../../etc/passwd")
	f.Add("foo/../../../bar")
	f.Add("normal/path.txt")
	f.Add("/absolute/path")
	f.Add("symlink\x00inject")
	f.Add(strings.Repeat("a/", 200))
	f.Add(".")
	f.Add("")
	// Promoted from the retired FuzzAssertInside, whose subject is now
	// pathinside.Root.Contains and is fuzzed in that package.
	f.Add("../escape")
	f.Add("../../etc")
	f.Add("inside/path")

	workDir := f.TempDir()

	f.Fuzz(func(t *testing.T, input string) {
		result, err := ResolveInside(workDir, input)
		if err != nil {
			return
		}
		if !filepath.IsAbs(result) {
			t.Fatalf("result %q is not absolute", result)
		}
		if result != filepath.Clean(result) {
			t.Fatalf("result %q is not clean", result)
		}
		if !pathinside.Root(workDir).Contains(result) {
			t.Fatalf("result %q escapes workDir %q", result, workDir)
		}
	})
}

func FuzzResolveInsideAbs(f *testing.F) {
	f.Add("/workspace", "file.txt")
	f.Add("/workspace", "../escape")
	f.Add("/workspace", "/workspace/sub/file")
	f.Add("/workspace", "")
	f.Add("/", "etc/passwd")
	f.Add("/workspace", "a/../../../etc/passwd")
	f.Add("/tmp", "\x00inject")
	f.Add("", "sub/file")

	f.Fuzz(func(t *testing.T, absWork, p string) {
		// The function's contract requires absWork to be absolute.
		if !filepath.IsAbs(absWork) {
			return
		}
		result, err := ResolveInsideAbs(absWork, p)
		if err != nil {
			return
		}
		if !filepath.IsAbs(result) {
			t.Fatalf("result %q is not absolute", result)
		}
		if result != filepath.Clean(result) {
			t.Fatalf("result %q is not clean", result)
		}
		if !pathinside.Root(absWork).Contains(result) {
			t.Fatalf("result %q escapes absWork %q", result, absWork)
		}
	})
}
