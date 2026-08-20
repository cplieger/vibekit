// Package workspace provides path-resolution primitives for
// workspace-scoped file operations. These are security primitives that
// prevent symlink escape and ".." traversal.
package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cplieger/pathinside/v2"
)

// ResolveInsideAbs confines p to absWork, which must already be absolute — the
// caller stores it once at startup (Runtime.workDir, Handler.workDir), so an Abs
// call per request would be work with no answer to give.
//
// It answers a LEXICAL question and nothing more. EvalSymlinks runs once, the
// containment is checked once, and the absolute path comes back; the operation
// happens later, and if it happens through ambient os calls the kernel re-resolves
// every component with no boundary attached. A caller that goes on to touch the
// filesystem must name the operation through an os.Root rooted at absWork — see
// agent's lifetime.confineInWorkDir and filebrowse's mount.root for the two
// consumers that do. This function's job is the verdict, not the enforcement.
//
// The boundary is built ONCE, before anything is compared against it, which is
// what keeps the three containment questions below from being asked backwards:
// a pathinside.Root held in a named variable has no pair to transpose, unlike a
// two-argument predicate whose own wrapper read in the opposite order from the
// filepath.Rel it called.
//
// An empty absWork therefore refuses everything rather than confining to the
// process working directory: Root("") contains nothing, and an unset workDir is
// a missing configuration value, not a request to sandbox onto whatever
// directory the process happens to be in.
func ResolveInsideAbs(absWork, p string) (string, error) {
	if p == "" {
		return "", errors.New("empty path")
	}
	root := pathinside.Root(absWork)
	if !filepath.IsAbs(p) {
		p = filepath.Join(absWork, p)
	}
	clean := filepath.Clean(p)
	if !root.Contains(clean) {
		return "", errors.New("path escapes workspace")
	}
	if resolved, resErr := filepath.EvalSymlinks(clean); resErr == nil {
		if !root.Contains(resolved) {
			return "", fmt.Errorf("path %q escapes workspace via symlink", p)
		}
		return resolved, nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(clean))
	if err != nil {
		if os.IsNotExist(err) {
			return clean, nil
		}
		return "", err
	}
	if !root.Contains(parent) {
		return "", fmt.Errorf("path %q escapes workspace via symlink", p)
	}
	return filepath.Join(parent, filepath.Base(clean)), nil
}

// RelPath returns the workspace-relative, forward-slash-normalized path
// for abs under workDir. Returns ("", error) when filepath.Rel fails.
// Use this instead of inline filepath.Rel + filepath.ToSlash sequences
// to ensure consistent normalization across fs handlers.
func RelPath(workDir, abs string) (string, error) {
	rel, err := filepath.Rel(workDir, abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
