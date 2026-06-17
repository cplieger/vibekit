package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// gk_vibekit_u29_canonTmp returns a symlink-resolved temp dir so
// AssertInside / EvalSymlinks comparisons aren't confused by a symlinked /tmp.
func gk_vibekit_u29_canonTmp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(tmp): %v", err)
	}
	return d
}

// kirohome.go:51 CONDITIONALS_NEGATION (`err != nil` -> `==`) in the KiroHome
// fallback. With HOME set, os.UserHomeDir returns no error, so the original
// returns HOME/.kiro; the `err == nil` mutant takes the error branch and
// returns the bare ".kiro".
func Test_gk_vibekit_u29_KiroHomeFallback(t *testing.T) {
	prev := kiroHomeResolver
	kiroHomeResolver = nil // force the os.UserHomeDir fallback path
	t.Cleanup(func() { kiroHomeResolver = prev })

	t.Setenv("HOME", "/gk-vibekit-u29-home")
	got := KiroHome()
	want := filepath.Join("/gk-vibekit-u29-home", ".kiro")
	if got != want {
		t.Errorf("KiroHome() = %q, want %q", got, want)
	}
}

// resolve.go:18 CONDITIONALS_NEGATION (`p == ""` -> `!=`): an empty path must
// error. The mutant skips the guard and resolves to the workspace root.
func Test_gk_vibekit_u29_ResolveInsideAbs_emptyPath(t *testing.T) {
	base := gk_vibekit_u29_canonTmp(t)
	got, err := ResolveInsideAbs(base, "")
	if err == nil {
		t.Errorf("ResolveInsideAbs(base, \"\") = (%q,nil), want error", got)
	}
}

// resolve.go:28 CONDITIONALS_NEGATION (`resErr == nil` -> `!=`) and
// resolve.go:41 CONDITIONALS_NEGATION (`inErr != nil` -> `==`): a non-existent
// file under the workspace resolves to the joined path with a nil error.
//   - Mutant 28 enters the EvalSymlinks-success block with an empty resolved
//     path and errors.
//   - Mutant 41 rejects the (valid, inside) parent and errors.
func Test_gk_vibekit_u29_ResolveInsideAbs_nonexistent(t *testing.T) {
	base := gk_vibekit_u29_canonTmp(t)
	p := filepath.Join(base, "gk-nope.txt")
	got, err := ResolveInsideAbs(base, p)
	if err != nil {
		t.Fatalf("ResolveInsideAbs(nonexistent under base) err = %v, want nil", err)
	}
	if got != p {
		t.Errorf("ResolveInsideAbs(nonexistent) = %q, want %q", got, p)
	}
}

// resolve.go:29 CONDITIONALS_NEGATION (`inErr != nil` -> `==`): an existing
// real file inside the workspace resolves with a nil error. The mutant rejects
// the inside path as an escape.
func Test_gk_vibekit_u29_ResolveInsideAbs_existing(t *testing.T) {
	base := gk_vibekit_u29_canonTmp(t)
	p := filepath.Join(base, "gk-real.txt")
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

// resolve.go:55 CONDITIONALS_NEGATION (`rel == ".."` -> `!=`) in AssertInside.
// An inside path must return nil; a parent path (rel == "..") must error. The
// mutant inverts both outcomes.
func Test_gk_vibekit_u29_AssertInside(t *testing.T) {
	base := gk_vibekit_u29_canonTmp(t)

	inside := filepath.Join(base, "sub")
	if err := AssertInside(inside, base); err != nil {
		t.Errorf("AssertInside(inside, base) = %v, want nil", err)
	}

	parent := filepath.Dir(base) // rel == ".."
	if err := AssertInside(parent, base); err == nil {
		t.Error("AssertInside(parent, base) = nil, want error")
	}
}

// resolve.go:67 CONDITIONALS_NEGATION (`err != nil` -> `==`) in RelPath. A
// resolvable pair returns the slash-normalized relative path; an unresolvable
// pair (abs vs relative) returns an error. The mutant returns ("",nil) for the
// success case and swallows the error case.
func Test_gk_vibekit_u29_RelPath(t *testing.T) {
	got, err := RelPath("/gk/work", "/gk/work/sub/f.txt")
	if err != nil {
		t.Fatalf("RelPath(success) err = %v, want nil", err)
	}
	if got != "sub/f.txt" {
		t.Errorf("RelPath(success) = %q, want %q", got, "sub/f.txt")
	}

	if _, err := RelPath("/gk/work", "relative/path"); err == nil {
		t.Error("RelPath(abs, relative) err = nil, want error")
	}
}
