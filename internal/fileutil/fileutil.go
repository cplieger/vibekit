package fileutil

import (
	"context"
	"os"
	"path/filepath"
)

// IsGitRepo reports whether dir contains a .git entry (directory for
// regular repos, regular file for worktrees and submodules, or a
// symlink to either — os.Stat follows symlinks).
func IsGitRepo(ctx context.Context, dir string) bool {
	if ctx.Err() != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}
