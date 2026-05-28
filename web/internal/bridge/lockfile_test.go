package bridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vibekit/internal/sessions"
)

// newTestLockManager creates a LockManager pointing at a fresh t.TempDir
// and returns both the manager and the sessions directory path.
func newTestLockManager(t *testing.T) (*LockManager, string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".kiro", "sessions", "cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	return NewLockManager(dir), dir
}

// stubIsKiroCLI replaces the package-level sessions.IsKiroCLI probe
// for the duration of t.
func stubIsKiroCLI(t *testing.T, returns bool) {
	t.Helper()
	orig := sessions.IsKiroCLI
	sessions.IsKiroCLI = func(int) bool { return returns }
	t.Cleanup(func() { sessions.IsKiroCLI = orig })
}

func TestLockManager_Dir(t *testing.T) {
	want := "/tmp/test-sessions"
	lm := NewLockManager(want)
	if got := lm.Dir(); got != want {
		t.Errorf("LockManager.Dir() = %q, want %q", got, want)
	}
}

func TestLockManager_Dir_Empty(t *testing.T) {
	lm := NewLockManager("")
	if got := lm.Dir(); got != "" {
		t.Errorf("LockManager.Dir() with empty = %q, want empty", got)
	}
}

func TestLockManager_RemoveStaleLock_EmptyDir(t *testing.T) {
	lm := NewLockManager("")
	lm.RemoveStaleLock(context.Background(), "anything")
}

func TestLockManager_RemoveStaleLock_InvalidSessionID(t *testing.T) {
	lm, dir := newTestLockManager(t)
	outside := filepath.Join(filepath.Dir(dir), "outside.lock")
	if err := os.WriteFile(outside, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("plant: %v", err)
	}
	cases := []string{
		"../outside", "..", ".", "a/b", "a\\b", "a\x00b",
		"", strings.Repeat("x", 129),
	}
	for _, sid := range cases {
		lm.RemoveStaleLock(context.Background(), sid)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("outside file touched by invalid session id: %v", err)
	}
}

func TestLockManager_CleanupStaleSessions_EmptyDir(t *testing.T) {
	lm := NewLockManager("")
	lm.CleanupStaleSessions(context.Background())
}

func TestValidSessionID(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{in: "", want: false},
		{in: "abc-123", want: true},
		{in: "01HXY8B6N9", want: true},
		{in: "../../../etc/passwd", want: false},
		{in: "sess/with/slash", want: false},
		{in: "sess\\with\\backslash", want: false},
		{in: "sess\x00null", want: false},
		{in: "..", want: false},
		{in: ".", want: false},
		{in: "ok.but..has-dotdot", want: false},
		{in: strings.Repeat("a", 128), want: true},
		{in: strings.Repeat("a", 129), want: false},
	}
	for _, tc := range cases {
		if got := validSessionID(tc.in); got != tc.want {
			t.Errorf("validSessionID(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
