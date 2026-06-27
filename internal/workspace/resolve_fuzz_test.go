package workspace

import (
	"path/filepath"
	"strings"
	"testing"
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
		if err := AssertInside(result, workDir); err != nil {
			t.Fatalf("result %q escapes workDir: %v", result, err)
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

func FuzzAssertInside(f *testing.F) {
	f.Add("../escape")
	f.Add("../../etc")
	f.Add("inside/path")
	f.Add(".")
	f.Add("")

	root := f.TempDir()

	f.Fuzz(func(t *testing.T, rel string) {
		target := filepath.Join(root, rel)
		target = filepath.Clean(target)

		err := AssertInside(target, root)
		if err != nil {
			return
		}
		got, relErr := filepath.Rel(root, target)
		if relErr != nil {
			t.Fatalf("filepath.Rel failed: %v", relErr)
		}
		if got == ".." || strings.HasPrefix(got, ".."+string(filepath.Separator)) {
			t.Fatalf("AssertInside accepted escaping path: rel=%q", got)
		}
	})
}
