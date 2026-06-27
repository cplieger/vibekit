package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// canonTmp returns a symlink-resolved temp dir so AssertInside /
// EvalSymlinks comparisons aren't confused by a symlinked /tmp.
func canonTmp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(tmp): %v", err)
	}
	return d
}

// An empty path must be rejected outright.
func TestResolveInsideAbs_emptyPathErrors(t *testing.T) {
	base := canonTmp(t)
	got, err := ResolveInsideAbs(base, "")
	if err == nil {
		t.Errorf("ResolveInsideAbs(base, %q) = (%q, nil), want error", "", got)
	}
}

// A non-existent file under the workspace resolves to the cleaned joined
// path with a nil error: the parent dir exists and is inside the workspace.
func TestResolveInsideAbs_nonexistentResolvesToCleanPath(t *testing.T) {
	base := canonTmp(t)
	p := filepath.Join(base, "nope.txt")
	got, err := ResolveInsideAbs(base, p)
	if err != nil {
		t.Fatalf("ResolveInsideAbs(nonexistent under base) err = %v, want nil", err)
	}
	if got != p {
		t.Errorf("ResolveInsideAbs(nonexistent) = %q, want %q", got, p)
	}
}

// An existing real file inside the workspace resolves with a nil error.
func TestResolveInsideAbs_existingFileResolves(t *testing.T) {
	base := canonTmp(t)
	p := filepath.Join(base, "real.txt")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	got, err := ResolveInsideAbs(base, p)
	if err != nil {
		t.Fatalf("ResolveInsideAbs(existing inside) err = %v, want nil", err)
	}
	if got != p {
		t.Errorf("ResolveInsideAbs(existing) = %q, want %q", got, p)
	}
}

// AssertInside accepts a path beneath root and rejects the parent
// directory (whose relative path is "..").
func TestAssertInside_acceptsInsideRejectsParent(t *testing.T) {
	base := canonTmp(t)

	inside := filepath.Join(base, "sub")
	if err := AssertInside(inside, base); err != nil {
		t.Errorf("AssertInside(inside, base) = %v, want nil", err)
	}

	parent := filepath.Dir(base) // relative path is ".."
	if err := AssertInside(parent, base); err == nil {
		t.Error("AssertInside(parent, base) = nil, want error")
	}
}

// RelPath returns the forward-slash-normalized relative path on success and
// an error when the pair cannot be made relative (absolute vs relative).
// It normalizes but does NOT reject escapes: an abs path outside workDir
// yields a "../"-prefixed result with no error (sandboxing is AssertInside's job).
func TestRelPath_normalizesAndErrors(t *testing.T) {
	got, err := RelPath("/work", "/work/sub/f.txt")
	if err != nil {
		t.Fatalf("RelPath(inside) err = %v, want nil", err)
	}
	if got != "sub/f.txt" {
		t.Errorf("RelPath(inside) = %q, want %q", got, "sub/f.txt")
	}

	outside, err := RelPath("/work", "/other/file")
	if err != nil {
		t.Fatalf("RelPath(outside) err = %v, want nil", err)
	}
	if outside != "../other/file" {
		t.Errorf("RelPath(outside) = %q, want %q", outside, "../other/file")
	}

	if _, err := RelPath("/work", "relative/path"); err == nil {
		t.Error("RelPath(abs, relative) err = nil, want error")
	}
}
