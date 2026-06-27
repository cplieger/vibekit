package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ResolveInside turns a client-supplied path into an absolute one
// guaranteed to live inside workDir. Rejects empty input, paths that
// escape via `..`, and symlink-based escape — both the parent
// directory and the final target are evaluated.
//
// Moved to test-only: the only caller is the fuzz test in this package.
// Production code uses ResolveInsideAbs instead.
func ResolveInside(workDir, p string) (string, error) {
	if p == "" {
		return "", errors.New("empty path")
	}
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve workdir: %w", err)
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
