package translate

import (
	"path/filepath"
	"strings"
	"testing"
)

type relPathDeps struct {
	*baseDeps
	workDir string
}

func (d *relPathDeps) WorkDir() string { return d.workDir }

func FuzzTranslatorRelPath(f *testing.F) {
	f.Add("/workspace", "/workspace/src/main.go")
	f.Add("/workspace", "/other/file.go")
	f.Add("/workspace", "/workspace")
	f.Add("/workspace", "")
	f.Add("", "/workspace/file.go")
	f.Add("/workspace", "/workspace/../escape")
	f.Add("/workspace", "/workspace/deep/../../out")

	f.Fuzz(func(t *testing.T, workDir, abs string) {
		deps := &relPathDeps{baseDeps: newBaseDeps(), workDir: workDir}
		tr := &Translator{deps: deps}

		result := tr.relPath(abs)

		if workDir == "" {
			if result != abs {
				t.Fatalf("empty workDir: got %q, want %q", result, abs)
			}
			return
		}
		clean := filepath.Clean(abs)
		root := filepath.Clean(workDir)
		rel, err := filepath.Rel(root, clean)
		if err != nil || strings.HasPrefix(rel, "..") {
			if result != abs {
				t.Fatalf("escaping: got %q, want %q", result, abs)
			}
		}
	})
}
