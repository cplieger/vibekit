package workspace

import (
	"path/filepath"
	"testing"
)

// BenchmarkResolveInsideAbs measures the production resolver. It measured a
// test-only COPY of it until that copy was deleted (see FuzzResolveInsideAbs for
// why), which is the worse half of a duplicated implementation: the numbers were
// attributed to a function nothing in production calls, so a regression in the
// real one could not show up here.
//
// b.TempDir() is symlink-resolved, because an unresolved base sends every case
// down the containment-failure path and the benchmark then times the error
// branch.
func BenchmarkResolveInsideAbs(b *testing.B) {
	workDir, err := filepath.EvalSymlinks(b.TempDir())
	if err != nil {
		b.Fatalf("EvalSymlinks: %v", err)
	}

	b.Run("relative", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = ResolveInsideAbs(workDir, "src/main.go")
		}
	})

	b.Run("absolute_inside", func(b *testing.B) {
		abs := filepath.Join(workDir, "deep", "nested", "file.txt")
		b.ReportAllocs()
		for b.Loop() {
			_, _ = ResolveInsideAbs(workDir, abs)
		}
	})

	b.Run("missing_parent", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = ResolveInsideAbs(workDir, "nonexistent/dir/file.txt")
		}
	})
}
