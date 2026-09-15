package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// twoRoots builds the shape ResolveInsideAnyAbs exists for: a workspace and a
// SIBLING uploads directory, each holding one real file, plus a third directory
// neither root contains. Both are symlink-resolved for canonTmp's reason.
func twoRoots(t *testing.T) (work, uploads, outside string) {
	t.Helper()
	work = canonTmp(t)
	uploads = canonTmp(t)
	outside = canonTmp(t)
	for _, dir := range []string{work, uploads, outside} {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s/f.txt: %v", dir, err)
		}
	}
	return work, uploads, outside
}

// The two roots are tried in order and either one resolves an ABSOLUTE path,
// which is the whole point: an uploaded file's path names a directory beside the
// workspace, and a single-root confinement refuses half the attachments a
// composer produces.
func TestResolveInsideAnyAbs_absolutePathResolvesInEitherRoot(t *testing.T) {
	work, uploads, outside := twoRoots(t)
	roots := []string{work, uploads}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"in the workspace", filepath.Join(work, "f.txt"), filepath.Join(work, "f.txt")},
		{"in the uploads dir", filepath.Join(uploads, "f.txt"), filepath.Join(uploads, "f.txt")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveInsideAnyAbs(roots, tc.in)
			if err != nil {
				t.Fatalf("ResolveInsideAnyAbs(roots, %q) err = %v, want nil", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ResolveInsideAnyAbs(roots, %q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// A path in NEITHER root is refused, and the message names the workspace —
	// roots[0]'s error is the one returned, so a reader is not told about a
	// directory they did not name.
	stray := filepath.Join(outside, "f.txt")
	_, err := ResolveInsideAnyAbs(roots, stray)
	if err == nil {
		t.Fatalf("ResolveInsideAnyAbs(roots, %q) err = nil, want an error", stray)
	}
	if !strings.Contains(err.Error(), "workspace") {
		t.Errorf("error = %q, want it to name the workspace", err)
	}
}

// A RELATIVE path resolves against roots[0] ONLY. With two roots a relative path
// would otherwise name two different files, so keeping it single-valued preserves
// ResolveInsideAbs's behaviour exactly.
func TestResolveInsideAnyAbs_relativePathTakesTheFirstRootOnly(t *testing.T) {
	work, uploads, _ := twoRoots(t)

	got, err := ResolveInsideAnyAbs([]string{work, uploads}, "f.txt")
	if err != nil {
		t.Fatalf("ResolveInsideAnyAbs(roots, %q) err = %v, want nil", "f.txt", err)
	}
	if want := filepath.Join(work, "f.txt"); got != want {
		t.Errorf("ResolveInsideAnyAbs(roots, %q) = %q, want the workspace's copy %q", "f.txt", got, want)
	}

	// Swapping the order swaps the answer, which is what proves the resolution
	// is positional rather than a search over both.
	got, err = ResolveInsideAnyAbs([]string{uploads, work}, "f.txt")
	if err != nil {
		t.Fatalf("ResolveInsideAnyAbs(swapped, %q) err = %v, want nil", "f.txt", err)
	}
	if want := filepath.Join(uploads, "f.txt"); got != want {
		t.Errorf("ResolveInsideAnyAbs(swapped, %q) = %q, want %q", "f.txt", got, want)
	}
}

// A ".." escape is refused from EITHER root, and a symlink leaving one is refused
// too — the second root widens which directories may be named, never the
// confinement each one applies.
func TestResolveInsideAnyAbs_refusesEscapes(t *testing.T) {
	work, uploads, outside := twoRoots(t)
	roots := []string{work, uploads}

	if err := os.Symlink(outside, filepath.Join(uploads, "out")); err != nil {
		t.Fatalf("symlink out of uploads: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(work, "out")); err != nil {
		t.Fatalf("symlink out of workspace: %v", err)
	}

	for _, tc := range []struct {
		name string
		in   string
	}{
		{"dot-dot out of the workspace", filepath.Join(work, "..", "elsewhere")},
		{"dot-dot out of the uploads dir", filepath.Join(uploads, "..", "elsewhere")},
		{"symlink out of the uploads dir", filepath.Join(uploads, "out", "f.txt")},
		{"symlink out of the workspace", filepath.Join(work, "out", "f.txt")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := ResolveInsideAnyAbs(roots, tc.in); err == nil {
				t.Errorf("ResolveInsideAnyAbs(roots, %q) = (%q, nil), want an error", tc.in, got)
			}
		})
	}
}

// An empty second root is legal and INERT, so a caller with no upload surface may
// pass "" rather than branching: pathinside.Root("") contains no path, so it can
// only ever fail to match.
func TestResolveInsideAnyAbs_emptySecondRootIsInert(t *testing.T) {
	work, _, outside := twoRoots(t)
	roots := []string{work, ""}

	inside := filepath.Join(work, "f.txt")
	if got, err := ResolveInsideAnyAbs(roots, inside); err != nil || got != inside {
		t.Errorf("ResolveInsideAnyAbs(roots, %q) = (%q, %v), want (%q, nil)", inside, got, err, inside)
	}
	stray := filepath.Join(outside, "f.txt")
	if got, err := ResolveInsideAnyAbs(roots, stray); err == nil {
		t.Errorf("ResolveInsideAnyAbs(roots, %q) = (%q, nil), want an error", stray, got)
	}
}

// The two degenerate inputs: an empty path is refused like ResolveInsideAbs's,
// and NO roots is refused rather than silently confining to nothing in
// particular.
func TestResolveInsideAnyAbs_refusesDegenerateInputs(t *testing.T) {
	work := canonTmp(t)
	if got, err := ResolveInsideAnyAbs([]string{work}, ""); err == nil {
		t.Errorf("ResolveInsideAnyAbs(roots, %q) = (%q, nil), want an error", "", got)
	}
	if got, err := ResolveInsideAnyAbs(nil, "f.txt"); err == nil {
		t.Errorf("ResolveInsideAnyAbs(nil, %q) = (%q, nil), want an error", "f.txt", got)
	}
}
