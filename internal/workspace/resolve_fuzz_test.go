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

func BenchmarkResolveInside(b *testing.B) {
	workDir := b.TempDir()

	b.Run("relative", func(b *testing.B) {
		for range b.N {
			_, _ = ResolveInside(workDir, "src/main.go")
		}
	})

	b.Run("absolute_inside", func(b *testing.B) {
		abs := filepath.Join(workDir, "deep", "nested", "file.txt")
		for range b.N {
			_, _ = ResolveInside(workDir, abs)
		}
	})

	b.Run("missing_parent", func(b *testing.B) {
		for range b.N {
			_, _ = ResolveInside(workDir, "nonexistent/dir/file.txt")
		}
	})
}
