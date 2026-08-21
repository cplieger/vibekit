package git

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestIsRepo(t *testing.T) {
	tests := []struct {
		setup func(t *testing.T, dir string)
		name  string
		want  bool
	}{
		{
			name:  "empty dir",
			setup: func(_ *testing.T, _ string) {},
			want:  false,
		},
		{
			name: "regular git directory",
			setup: func(t *testing.T, dir string) {
				if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
					t.Fatalf("Mkdir error = %v", err)
				}
			},
			want: true,
		},
		{
			name: "worktree or submodule .git file",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, ".git"),
					[]byte("gitdir: ../.git/worktrees/foo\n"), 0o644); err != nil {
					t.Fatalf("WriteFile error = %v", err)
				}
			},
			want: true,
		},
		{
			name: "symlink .git to directory",
			setup: func(t *testing.T, dir string) {
				if runtime.GOOS == "windows" {
					t.Skip("symlink creation requires elevated perms on Windows")
				}
				target := filepath.Join(dir, "real-git")
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatalf("Mkdir target error = %v", err)
				}
				if err := os.Symlink(target, filepath.Join(dir, ".git")); err != nil {
					t.Fatalf("Symlink error = %v", err)
				}
			},
			want: true,
		},
		{
			name: "broken symlink",
			setup: func(t *testing.T, dir string) {
				if runtime.GOOS == "windows" {
					t.Skip("symlink creation requires elevated perms on Windows")
				}
				if err := os.Symlink(filepath.Join(dir, "missing"),
					filepath.Join(dir, ".git")); err != nil {
					t.Fatalf("Symlink error = %v", err)
				}
			},
			want: false,
		},
		{
			name: "non-git sibling file named similarly",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "gitfile"),
					[]byte("x"), 0o644); err != nil {
					t.Fatalf("WriteFile error = %v", err)
				}
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)
			got := IsRepo(t.Context(), dir)
			if got != tt.want {
				t.Errorf("IsRepo(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// TestIsRepo_CancelledContext short-circuits to false when the context
// is already cancelled, even though a .git entry exists: the context guard
// must run before the filesystem stat.
func TestIsRepo_CancelledContext(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if IsRepo(ctx, dir) {
		t.Error("IsRepo with cancelled context = true, want false (ctx guard must short-circuit the stat)")
	}
}
