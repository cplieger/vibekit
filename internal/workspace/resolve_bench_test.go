package workspace

import (
	"path/filepath"
	"testing"
)

func BenchmarkResolveInside(b *testing.B) {
	workDir := b.TempDir()

	b.Run("relative", func(b *testing.B) {
		for b.Loop() {
			_, _ = ResolveInside(workDir, "src/main.go")
		}
	})

	b.Run("absolute_inside", func(b *testing.B) {
		abs := filepath.Join(workDir, "deep", "nested", "file.txt")
		for b.Loop() {
			_, _ = ResolveInside(workDir, abs)
		}
	})

	b.Run("missing_parent", func(b *testing.B) {
		for b.Loop() {
			_, _ = ResolveInside(workDir, "nonexistent/dir/file.txt")
		}
	})
}
