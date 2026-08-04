package steering

import (
	"os"
	"path/filepath"
	"testing"
)

// mustWriteFile writes content to path, creating any missing parent
// directories. It fails the test on any error.
func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// setupKiroHome redirects $HOME so KiroHome() resolves into an isolated
// temp tree and Generate writes there. Returns the resolved
// environment.md steering path.
func setupKiroHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".kiro", "steering", "environment.md")
}
