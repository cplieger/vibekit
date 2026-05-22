// Package workspace provides path-resolution and boundary-assertion
// primitives for workspace-scoped file operations. These are security
// primitives that prevent symlink escape and ".." traversal.
package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveInsideAbs is like ResolveInside but accepts an already-absolute
// workDir, skipping the filepath.Abs call. Use this when the caller
// stores workDir as an absolute path set once at startup (e.g. Hub.workDir).
func ResolveInsideAbs(absWork, p string) (string, error) {
	if p == "" {
		return "", errors.New("empty path")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(absWork, p)
	}
	clean := filepath.Clean(p)
	if inErr := AssertInside(clean, absWork); inErr != nil {
		return "", inErr
	}
	if resolved, resErr := filepath.EvalSymlinks(clean); resErr == nil {
		if inErr := AssertInside(resolved, absWork); inErr != nil {
			return "", fmt.Errorf("path %q escapes workspace via symlink: %w", p, inErr)
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
	if inErr := AssertInside(parent, absWork); inErr != nil {
		return "", fmt.Errorf("path %q escapes workspace via symlink: %w", p, inErr)
	}
	return filepath.Join(parent, filepath.Base(clean)), nil
}

// AssertInside returns nil iff target is equal to root or contained
// beneath it. Relies on filepath.Rel: a relative path that starts
// with ".." signals escape.
func AssertInside(target, root string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("path escapes workspace")
	}
	return nil
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
