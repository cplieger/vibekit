package workspace

import (
	"path/filepath"
	"testing"
)

func FuzzResolveInsideAbs(f *testing.F) {
	f.Add("/workspace", "file.txt")
	f.Add("/workspace", "../escape")
	f.Add("/workspace", "/workspace/sub/file")
	f.Add("/workspace", "")
	f.Add("/", "etc/passwd")
	f.Add("/workspace", "a/../../../etc/passwd")
	f.Add("/tmp", "\x00inject")

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
		if err := AssertInside(result, absWork); err != nil {
			t.Fatalf("result %q escapes absWork %q: %v", result, absWork, err)
		}
	})
}
