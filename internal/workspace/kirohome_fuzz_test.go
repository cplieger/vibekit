package workspace

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzKiroSteeringPath exercises KiroSteeringPath with arbitrary file
// names. Invariant: result is always filepath.Join(KiroHome(), "steering", name).
func FuzzKiroSteeringPath(f *testing.F) {
	f.Add("custom.md")
	f.Add("environment.md")
	f.Add("")
	f.Add("../escape.md")
	f.Add("../../etc/passwd")
	f.Add("/absolute/path")
	f.Add("sub/dir/file.md")
	f.Add(".\x00null")
	f.Add(strings.Repeat("a", 4096))

	f.Fuzz(func(t *testing.T, name string) {
		result := KiroSteeringPath(name)

		// Must not panic (primary assertion).
		// Result must equal the canonical join.
		expected := filepath.Join(KiroHome(), "steering", name)
		if result != expected {
			t.Fatalf("KiroSteeringPath(%q) = %q, want %q", name, result, expected)
		}
	})
}

// FuzzKiroSettingsPath exercises KiroSettingsPath with arbitrary names.
// Invariant: result is always filepath.Join(KiroHome(), "settings", name).
func FuzzKiroSettingsPath(f *testing.F) {
	f.Add("global.json")
	f.Add("")
	f.Add("../escape")
	f.Add("/etc/passwd")
	f.Add("sub/nested/file.json")
	f.Add("file\x00with\tnull")

	f.Fuzz(func(t *testing.T, name string) {
		result := KiroSettingsPath(name)

		expected := filepath.Join(KiroHome(), "settings", name)
		if result != expected {
			t.Fatalf("KiroSettingsPath(%q) = %q, want %q", name, result, expected)
		}
	})
}

// FuzzKiroSessionsCLIDir asserts that KiroSessionsCLIDir always returns
// the expected path under KiroHome().
func FuzzKiroSessionsCLIDir(f *testing.F) {
	// This target has no input-varying behavior (it's deterministic),
	// but we include it to verify the function never panics under
	// concurrent fuzz-worker access (sync.Once safety).
	f.Add(0)

	f.Fuzz(func(t *testing.T, _ int) {
		result := KiroSessionsCLIDir()

		expected := filepath.Join(KiroHome(), "sessions", "cli")
		if result != expected {
			t.Fatalf("KiroSessionsCLIDir() = %q, want %q", result, expected)
		}
	})
}
