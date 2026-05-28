package fileutil

import (
	"bytes"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- IsGitRepo ---

func TestIsGitRepo(t *testing.T) {
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
			got := IsGitRepo(dir)
			if got != tt.want {
				t.Errorf("IsGitRepo(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// --- SaveBytes ---

func TestSaveBytes_round_trips_content_and_applies_mode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode semantics differ on Windows")
	}
	tests := []struct {
		name    string
		data    []byte
		perm    os.FileMode
		dirPerm os.FileMode
	}{
		{"empty", []byte{}, 0o644, 0o755},
		{"text", []byte("hello world\n"), 0o644, 0o755},
		{"binary", []byte{0x00, 0x01, 0xff, 0x7f, 0x80}, 0o644, 0o755},
		{"private perm triggers 0700 parent", []byte("secret"), 0o600, 0o700},
		{"world-readable stays 0755", []byte("pub"), 0o664, 0o755},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "nested")
			path := filepath.Join(dir, "out.bin")
			if err := SaveBytes(path, tt.data, tt.perm); err != nil {
				t.Fatalf("SaveBytes(%q) error = %v", tt.name, err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile error = %v", err)
			}
			if !bytes.Equal(got, tt.data) {
				t.Errorf("SaveBytes(%q) round-trip = %v, want %v", tt.name, got, tt.data)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat error = %v", err)
			}
			if info.Mode().Perm() != tt.perm {
				t.Errorf("SaveBytes(%q) file mode = %o, want %o", tt.name, info.Mode().Perm(), tt.perm)
			}
			dirInfo, err := os.Stat(dir)
			if err != nil {
				t.Fatalf("dir Stat error = %v", err)
			}
			if dirInfo.Mode().Perm() != tt.dirPerm {
				t.Errorf("SaveBytes(%q) dir mode = %o, want %o", tt.name, dirInfo.Mode().Perm(), tt.dirPerm)
			}
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if strings.Contains(e.Name(), ".tmp-") {
					t.Errorf("SaveBytes(%q) left stale temp file: %q", tt.name, e.Name())
				}
			}
		})
	}
}

func TestSaveBytes_parent_unusable_returns_error(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	path := filepath.Join(blocker, "child", "out.bin")
	err := SaveBytes(path, []byte("data"), 0o644)
	if err == nil {
		t.Error("SaveBytes with unusable parent = nil, want error")
	}
}

func TestSaveBytes_writeTempFile_error_is_propagated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits drive this path")
	}
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		t.Skip("root bypasses EACCES on read-only directories")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("Chmod error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	path := filepath.Join(dir, "out.bin")
	err := SaveBytes(path, []byte("data"), 0o644)
	if err == nil {
		t.Fatal("SaveBytes(ro parent) = nil, want error")
	}
	_ = os.Chmod(dir, 0o755)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("SaveBytes(ro parent) left stale temp file: %q", e.Name())
		}
	}
}

func TestSaveBytes_rename_error_cleans_up_temp_file(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "target")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("Mkdir error = %v", err)
	}
	err := SaveBytes(path, []byte("data"), 0o644)
	if err == nil {
		t.Fatal("SaveBytes(target is dir) = nil, want error")
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("SaveBytes(rename fail) left stale temp file: %q", e.Name())
		}
	}
}

// --- SaveJSON ---

func TestSaveJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "data.json")
	var mu sync.Mutex
	if err := SaveJSON(path, &mu, map[string]int{"x": 1}, "test", 0o644); err != nil {
		t.Fatalf("SaveJSON error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	if !strings.Contains(string(data), `"x": 1`) {
		t.Errorf("content = %q", string(data))
	}
	if !mu.TryLock() {
		t.Fatal("SaveJSON did not release mutex")
	}
	mu.Unlock()
}

func TestSaveJSON_applies_perm_mode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode not meaningful on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	var mu sync.Mutex
	if err := SaveJSON(path, &mu, map[string]int{"x": 1}, "test", 0o600); err != nil {
		t.Fatalf("SaveJSON error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 0600", got)
	}
}

func TestSaveJSON_marshal_error_does_not_create_file(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	var mu sync.Mutex
	if err := SaveJSON(path, &mu, make(chan int), "test", 0o644); err == nil {
		t.Error("expected marshal error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file created on marshal error: %v", err)
	}
}

func TestSaveJSON_leaves_no_temp_file_on_success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	var mu sync.Mutex
	if err := SaveJSON(path, &mu, map[string]int{"x": 1}, "test", 0o644); err != nil {
		t.Fatalf("SaveJSON error = %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("stale temp file: %q", e.Name())
		}
	}
}

func TestSaveJSON_nil_mutex_returns_error(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	err := SaveJSON(path, nil, map[string]int{"x": 1}, "test", 0o644)
	if err == nil {
		t.Fatal("SaveJSON(nil mutex) = nil, want error")
	}
	if !strings.Contains(err.Error(), "nil mutex") {
		t.Errorf("error = %q, want substring %q", err.Error(), "nil mutex")
	}
}

// --- CleanupStaleTemps ---

func TestCleanupStaleTemps(t *testing.T) {
	dir := t.TempDir()
	recent := filepath.Join(dir, "live.json.tmp-1111")
	if err := os.WriteFile(recent, []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFile recent: %v", err)
	}
	old := filepath.Join(dir, "chat.json.tmp-2222")
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile old: %v", err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes old: %v", err)
	}
	canonical := filepath.Join(dir, "chat.json")
	if err := os.WriteFile(canonical, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("WriteFile canonical: %v", err)
	}
	if err := os.Chtimes(canonical, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes canonical: %v", err)
	}
	CleanupStaleTemps(dir, time.Hour)
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("recent temp removed: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old temp not removed: stat err = %v", err)
	}
	if _, err := os.Stat(canonical); err != nil {
		t.Errorf("canonical file removed: %v", err)
	}
	CleanupStaleTemps(filepath.Join(dir, "no-such-subdir"), time.Hour)
}

func TestCleanupStaleTemps_preserves_user_file_with_tmp_in_name(t *testing.T) {
	dir := t.TempDir()
	userFile := filepath.Join(dir, "alice.tmp-2024-notes.json")
	if err := os.WriteFile(userFile, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(userFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, "chat.json.tmp-abc123")
	if err := os.WriteFile(tmp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmp, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	CleanupStaleTemps(dir, time.Hour)
	if _, err := os.Stat(userFile); err != nil {
		t.Errorf("user file removed by sweep: %v", err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("real temp not removed: stat err = %v", err)
	}
}

func TestCleanupStaleTemps_readdir_error_does_not_panic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits drive readdir permission")
	}
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		t.Skip("root bypasses EACCES")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "inaccessible")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("Mkdir error = %v", err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("Chmod error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("CleanupStaleTemps panicked on EACCES: %v", r)
		}
	}()
	CleanupStaleTemps(dir, time.Hour)
}

func TestCleanupStaleTemps_continues_after_remove_failure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits drive unlink permission")
	}
	if u, err := user.Current(); err == nil && u.Uid == "0" {
		t.Skip("root bypasses directory-write EACCES")
	}
	dir := t.TempDir()
	blocked := filepath.Join(dir, "a.json.tmp-aaa")
	sweepable := filepath.Join(dir, "b.json.tmp-bbb")
	for _, p := range []string{blocked, sweepable} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", p, err)
		}
		oldTime := time.Now().Add(-2 * time.Hour)
		if err := os.Chtimes(p, oldTime, oldTime); err != nil {
			t.Fatalf("Chtimes(%q) error = %v", p, err)
		}
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("Chmod error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("CleanupStaleTemps panicked on remove failure: %v", r)
		}
	}()
	CleanupStaleTemps(dir, time.Hour)
	_ = os.Chmod(dir, 0o755)
	for _, p := range []string{blocked, sweepable} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("CleanupStaleTemps removed %q despite EACCES: %v", p, err)
		}
	}
}

// --- isStaleTempName ---

func TestIsStaleTempName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"chat temp", "chat.json.tmp-abc123", true},
		{"nested base temp", "state.tmp-xyz", true},
		{"trailing random", "foo.tmp-1", true},
		{"upload temp", "photo.jpg.upload-abc123", true},
		{"copy temp", "backup.tar.copy-xyz789", true},
		{"no .tmp- signature", "regular.json", false},
		{"suffix contains a dot", "alice.tmp-2024-notes.json", false},
		{"suffix contains a slash", "foo.tmp-a/b", false},
		{"suffix contains a backslash", "foo.tmp-a\\b", false},
		{"nothing after suffix", "just.tmp-", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStaleTempName(tt.in); got != tt.want {
				t.Errorf("isStaleTempName(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
