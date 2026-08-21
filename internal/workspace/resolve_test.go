package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// canonTmp returns a symlink-resolved temp dir so containment /
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

// The containment boundary accepts a path beneath the workspace and rejects the
// parent directory (whose relative path is ".."). Asserted through
// ResolveInsideAbs because the boundary is no longer a callable pair: the root
// is a pathinside.Root the function holds, which is what removes the
// transposition hazard rather than renaming it.
func TestResolveInsideAbs_acceptsInsideRejectsParent(t *testing.T) {
	base := canonTmp(t)

	inside := filepath.Join(base, "sub")
	if _, err := ResolveInsideAbs(base, inside); err != nil {
		t.Errorf("ResolveInsideAbs(base, inside) = %v, want nil", err)
	}

	parent := filepath.Dir(base) // relative path is ".."
	if got, err := ResolveInsideAbs(base, parent); err == nil {
		t.Errorf("ResolveInsideAbs(base, parent) = (%q, nil), want an error", got)
	}
}

// An empty workspace root contains NOTHING, so every path is refused. The
// hand-rolled filepath.Rel predicate this replaced failed OPEN here: Rel cleans
// an empty base to ".", so a relative path was silently confined to the
// process's working directory — a boundary nobody chose — and an unset workDir
// is a missing configuration value, not that request.
func TestResolveInsideAbs_emptyRootContainsNothing(t *testing.T) {
	for _, p := range []string{"sub/file.txt", "file.txt", "/abs/file.txt", "."} {
		if got, err := ResolveInsideAbs("", p); err == nil {
			t.Errorf("ResolveInsideAbs(%q, %q) = (%q, nil), want an error", "", p, got)
		}
	}
}

// RelPath returns the forward-slash-normalized relative path on success and
// an error when the pair cannot be made relative (absolute vs relative).
// It normalizes but does NOT reject escapes: an abs path outside workDir
// yields a "../"-prefixed result with no error (containment is
// ResolveInsideAbs's job).
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
