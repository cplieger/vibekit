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

// ResolveInsideAbs is like ResolveInside but accepts an already-absolute
// workDir, skipping the filepath.Abs call. Use this when the caller
// stores workDir as an absolute path set once at startup (e.g. Runtime.workDir).
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
