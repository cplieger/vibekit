package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/pathinside/v2"
)

// FuzzResolveInsideAbs fuzzes the production resolver against its three
// invariants: an accepted result is absolute, clean, and inside absWork.
//
// It used to have a sibling, FuzzResolveInside, whose subject was a 35-line COPY
// of this function living in resolve_helpers_test.go — kept, by its own comment,
// only because that fuzz target was its lone caller. Two implementations of one
// containment rule is the trap, not the duplication: a fix applied to the
// production resolver leaves the copy asserting the old shape still passes, in
// the one predicate where that matters most. The copy and its target are deleted;
// every seed unique to it is promoted below, and the coverage it had that this
// target lacked — a workDir that really exists, so EvalSymlinks SUCCEEDS instead
// of falling through to the parent branch — is now seeded here explicitly,
// including a real in-tree symlink and a real escaping one.
func FuzzResolveInsideAbs(f *testing.F) {
	f.Add("/workspace", "file.txt")
	f.Add("/workspace", "../escape")
	f.Add("/workspace", "/workspace/sub/file")
	f.Add("/workspace", "")
	f.Add("/", "etc/passwd")
	f.Add("/workspace", "a/../../../etc/passwd")
	f.Add("/tmp", "\x00inject")
	f.Add("", "sub/file")
	// Promoted from the retired FuzzResolveInside.
	f.Add("/workspace", "..")
	f.Add("/workspace", "../../../etc/passwd")
	f.Add("/workspace", "foo/../../../bar")
	f.Add("/workspace", "normal/path.txt")
	f.Add("/workspace", "/absolute/path")
	f.Add("/workspace", "symlink\x00inject")
	f.Add("/workspace", strings.Repeat("a/", 200))
	f.Add("/workspace", ".")
	f.Add("/workspace", "../../etc")
	f.Add("/workspace", "inside/path")

	// A workDir that exists, so the EvalSymlinks-succeeds branch is reachable at
	// all. Every seed above names a directory that does not exist in a test
	// container, which sends all of them down the parent-directory fallback and
	// leaves the branch that actually resolves a symlink unexercised.
	work := realWorkDir(f)
	f.Add(work, ".")
	f.Add(work, "plain.txt")
	f.Add(work, "sub/plain.txt")
	f.Add(work, "inlink")           // symlink to a sibling inside work
	f.Add(work, "inlink/plain.txt") // through that symlink
	f.Add(work, "outlink")          // symlink pointing outside work
	f.Add(work, "outlink/plain.txt")
	f.Add(work, "missing/deeper/leaf.txt")

	f.Fuzz(func(t *testing.T, absWork, p string) {
		// The function's contract requires absWork to be absolute.
		if !filepath.IsAbs(absWork) {
			return
		}
		result, err := ResolveInsideAbs(absWork, p)
		if err != nil {
			return
		}
		if !filepath.IsAbs(result) {
			t.Fatalf("result %q is not absolute", result)
		}
		if result != filepath.Clean(result) {
			t.Fatalf("result %q is not clean", result)
		}
		if !pathinside.Root(absWork).Contains(result) {
			t.Fatalf("result %q escapes absWork %q", result, absWork)
		}
	})
}

// realWorkDir builds a workspace on disk holding the shapes the resolver
// branches on: a plain file, a nested one, a symlink that stays inside, and a
// symlink that leaves. Returned symlink-resolved, because an unresolved base
// makes the containment check compare two spellings of the same directory.
func realWorkDir(f *testing.F) string {
	f.Helper()
	work, err := filepath.EvalSymlinks(f.TempDir())
	if err != nil {
		f.Fatalf("EvalSymlinks(work): %v", err)
	}
	outside, err := filepath.EvalSymlinks(f.TempDir())
	if err != nil {
		f.Fatalf("EvalSymlinks(outside): %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "plain.txt"), []byte("x"), 0o600); err != nil {
		f.Fatalf("write plain: %v", err)
	}
	if err := os.Mkdir(filepath.Join(work, "sub"), 0o700); err != nil {
		f.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "sub", "plain.txt"), []byte("x"), 0o600); err != nil {
		f.Fatalf("write sub/plain: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "plain.txt"), []byte("x"), 0o600); err != nil {
		f.Fatalf("write outside plain: %v", err)
	}
	if err := os.Symlink("sub", filepath.Join(work, "inlink")); err != nil {
		f.Fatalf("symlink inlink: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(work, "outlink")); err != nil {
		f.Fatalf("symlink outlink: %v", err)
	}
	return work
}
