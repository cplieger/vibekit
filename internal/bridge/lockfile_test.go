package bridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/sessions"
	"pgregory.net/rapid"
)

// newTestSessionManager creates a sessions.Manager pointing at a fresh
// t.TempDir and returns both the manager and the sessions directory path.
func newTestSessionManager(t *testing.T) (*sessions.Manager, string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".kiro", "sessions", "cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	return sessions.New(dir), dir
}

func TestSessionManager_Dir(t *testing.T) {
	want := "/tmp/test-sessions"
	mgr := sessions.New(want)
	if got := mgr.Dir(); got != want {
		t.Errorf("Manager.Dir() = %q, want %q", got, want)
	}
}

func TestSessionManager_Dir_Empty(t *testing.T) {
	mgr := sessions.New("")
	if got := mgr.Dir(); got != "" {
		t.Errorf("Manager.Dir() with empty = %q, want empty", got)
	}
}

func TestSessionManager_RemoveStaleLock_EmptyDir(t *testing.T) {
	mgr := sessions.New("")
	mgr.RemoveStaleLock(context.Background(), "anything")
}

func TestSessionManager_RemoveStaleLock_InvalidSessionID(t *testing.T) {
	mgr, dir := newTestSessionManager(t)
	outside := filepath.Join(filepath.Dir(dir), "outside.lock")
	if err := os.WriteFile(outside, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("plant: %v", err)
	}
	cases := []string{
		"../outside", "..", ".", "a/b", "a\\b", "a\x00b",
		"", strings.Repeat("x", 129),
	}
	for _, sid := range cases {
		mgr.RemoveStaleLock(context.Background(), sid)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("outside file touched by invalid session id: %v", err)
	}
}

func TestSessionManager_CleanupStale_EmptyDir(t *testing.T) {
	mgr := sessions.New("")
	mgr.CleanupStale(context.Background())
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
		if got := api.ValidSessionID(tc.in); got != tc.want {
			t.Errorf("ValidSessionID(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestCleanupStale_RapidSafety(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		mgr, dir := newTestSessionManager(t)

		// Generate 1-10 lock files with random content shapes.
		n := rapid.IntRange(1, 10).Draw(rt, "numFiles")

		// Plant a file with an invalid session ID that must never be touched.
		invalidPath := filepath.Join(filepath.Dir(dir), "outside.lock")
		if err := os.WriteFile(invalidPath, []byte("keep"), 0o644); err != nil {
			t.Fatalf("plant: %v", err)
		}

		for i := range n {
			sid := rapid.StringMatching(`[a-zA-Z0-9_-]{1,20}`).Draw(rt, fmt.Sprintf("sid_%d", i))
			// Vary content: valid JSON, malformed JSON, empty, huge PID, negative PID.
			contentKind := rapid.IntRange(0, 4).Draw(rt, fmt.Sprintf("kind_%d", i))
			var content string
			switch contentKind {
			case 0:
				pid := rapid.IntRange(1, 99999).Draw(rt, fmt.Sprintf("pid_%d", i))
				content = fmt.Sprintf(`{"pid":%d}`, pid)
			case 1:
				content = `not json at all`
			case 2:
				content = `{"pid":0}`
			case 3:
				content = `{"pid":-1}`
			case 4:
				content = ``
			}

			lockPath := filepath.Join(dir, sid+".lock")
			if err := os.WriteFile(lockPath, []byte(content), 0o644); err != nil {
				t.Fatalf("write lock: %v", err)
			}
		}

		// Stub IsKiroCLI — doesn't matter much since flock probe
		// determines staleness in test env, but must not panic.
		orig := sessions.IsKiroCLI
		sessions.IsKiroCLI = func(pid int) bool { return pid == 1 }
		defer func() { sessions.IsKiroCLI = orig }()

		// Run cleanup — must not panic on any content shape.
		mgr.CleanupStale(context.Background())

		// Invariant: file outside the sessions dir is never touched.
		if _, err := os.Stat(invalidPath); err != nil {
			t.Fatal("outside file was touched by cleanup")
		}
	})
}
