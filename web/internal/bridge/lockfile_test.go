package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// setFakeHome points the sessions directory at a new t.TempDir and
// returns the resulting sessions directory path.
func setFakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".kiro", "sessions", "cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	old := sessionsPath
	SetSessionsDir(dir)
	t.Cleanup(func() { sessionsPath = old })
	return dir
}

// stubIsKiroCLI replaces the package-level isKiroCLI probe for the
// duration of t. Tests that plant os.Getpid() as a "live" lock owner
// need this because the test runner's /proc/<pid>/comm reads as
// whatever the Go test binary is named, not kiro-cli.
func stubIsKiroCLI(t *testing.T, returns bool) {
	t.Helper()
	orig := isKiroCLI
	isKiroCLI = func(int) bool { return returns }
	t.Cleanup(func() { isKiroCLI = orig })
}

func TestSessionsDir_Set(t *testing.T) {
	old := sessionsPath
	t.Cleanup(func() { sessionsPath = old })
	want := "/tmp/test-sessions"
	SetSessionsDir(want)
	if got := sessionsDir(); got != want {
		t.Errorf("sessionsDir() = %q, want %q", got, want)
	}
}

func TestSessionsDir_Unset(t *testing.T) {
	old := sessionsPath
	t.Cleanup(func() { sessionsPath = old })
	SetSessionsDir("")
	if got := sessionsDir(); got != "" {
		t.Errorf("sessionsDir() with empty = %q, want empty", got)
	}
}

func writeLock(t *testing.T, dir, id string, body []byte) string {
	t.Helper()
	path := filepath.Join(dir, id+".lock")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	return path
}

// writeLockHeld writes a lockfile AND holds a flock on it for the
// duration of the test, simulating a live kiro-cli process that keeps
// the fd open. The flock is released when the test completes.
func writeLockHeld(t *testing.T, dir, id string, body []byte) string {
	t.Helper()
	path := filepath.Join(dir, id+".lock")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open lock for flock: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		t.Fatalf("flock: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return path
}

// deadPID returns a PID that is guaranteed to not belong to any
// currently-running process.
func deadPID(t *testing.T) int {
	t.Helper()
	for pid := 4_194_303; pid > 4_190_207; pid-- {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return pid
		}
	}
	t.Fatalf("could not find an unused PID under pid_max")
	return 0
}

func TestRemoveStaleLock(t *testing.T) {
	cases := []struct {
		setup       func(t *testing.T, dir string)
		name        string
		lockContent []byte
		wantRemoved bool
	}{
		{
			name:        "NoLockFile",
			lockContent: nil,
			wantRemoved: false,
		},
		{
			name: "DeadPIDRemoves",
			setup: func(t *testing.T, _ string) {
				t.Helper()
			},
			wantRemoved: true,
		},
		{
			name: "LivePIDKept",
			setup: func(t *testing.T, _ string) {
				t.Helper()
				stubIsKiroCLI(t, true)
			},
			wantRemoved: false,
		},
		{
			name:        "MalformedJSON",
			lockContent: []byte("{this is not json"),
			wantRemoved: false,
		},
		{
			name:        "PIDZero",
			wantRemoved: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := setFakeHome(t)
			if tc.setup != nil {
				tc.setup(t, dir)
			}

			sid := "test-" + tc.name
			var path string
			if tc.lockContent == nil && tc.name == "NoLockFile" {
				// Don't write any lock file.
				RemoveStaleLock(context.Background(), "no-such-session")
				return
			}

			var body []byte
			switch tc.name {
			case "DeadPIDRemoves":
				pid := deadPID(t)
				body, _ = json.Marshal(lockFile{PID: pid})
			case "LivePIDKept":
				body, _ = json.Marshal(lockFile{PID: os.Getpid()})
			case "PIDZero":
				body, _ = json.Marshal(lockFile{PID: 0})
			default:
				body = tc.lockContent
			}
			if tc.name == "LivePIDKept" {
				path = writeLockHeld(t, dir, sid, body)
			} else {
				path = writeLock(t, dir, sid, body)
			}

			RemoveStaleLock(context.Background(), sid)

			_, err := os.Stat(path)
			if tc.wantRemoved {
				if !os.IsNotExist(err) {
					t.Errorf("lock file still exists after RemoveStaleLock: err=%v", err)
				}
			} else {
				if err != nil {
					t.Errorf("lock file was removed unexpectedly: err=%v", err)
				}
			}
		})
	}
}

func TestRemoveStaleLock_HomeUnset(t *testing.T) {
	t.Setenv("HOME", "")
	os.Unsetenv("HOME")
	RemoveStaleLock(context.Background(), "anything")
}

func TestRemoveStaleLock_InvalidSessionID(t *testing.T) {
	dir := setFakeHome(t)
	outside := filepath.Join(filepath.Dir(dir), "outside.lock")
	if err := os.WriteFile(outside, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("plant: %v", err)
	}
	cases := []string{
		"../outside", "..", ".", "a/b", "a\\b", "a\x00b",
		"", strings.Repeat("x", 129),
	}
	for _, sid := range cases {
		RemoveStaleLock(context.Background(), sid)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("outside file touched by invalid session id: %v", err)
	}
}

func TestCleanupStaleSessions_MixedLocks(t *testing.T) {
	dir := setFakeHome(t)
	stubIsKiroCLI(t, true)

	dead := deadPID(t)
	deadBody, _ := json.Marshal(lockFile{PID: dead})
	deadLock := writeLock(t, dir, "stale", deadBody)
	deadJSONL := filepath.Join(dir, "stale.jsonl")
	deadJSON := filepath.Join(dir, "stale.json")
	if err := os.WriteFile(deadJSONL, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deadJSON, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	liveBody, _ := json.Marshal(lockFile{PID: os.Getpid()})
	liveLock := writeLockHeld(t, dir, "live", liveBody)

	badLock := writeLock(t, dir, "bad", []byte("garbage"))

	zeroBody, _ := json.Marshal(lockFile{PID: 0})
	zeroLock := writeLock(t, dir, "zero", zeroBody)

	dead2 := deadPID(t)
	dead2Body, _ := json.Marshal(lockFile{PID: dead2})
	dead2Lock := writeLock(t, dir, "nonempty", dead2Body)
	dead2JSONL := filepath.Join(dir, "nonempty.jsonl")
	if err := os.WriteFile(dead2JSONL, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	other := filepath.Join(dir, "README.txt")
	if err := os.WriteFile(other, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	CleanupStaleSessions(context.Background())

	if _, err := os.Stat(deadLock); !os.IsNotExist(err) {
		t.Errorf("dead lock remains: %v", err)
	}
	if _, err := os.Stat(deadJSONL); !os.IsNotExist(err) {
		t.Errorf("empty jsonl remains: %v", err)
	}
	if _, err := os.Stat(deadJSON); !os.IsNotExist(err) {
		t.Errorf("companion .json remains: %v", err)
	}
	if _, err := os.Stat(liveLock); err != nil {
		t.Errorf("live lock removed: %v", err)
	}
	if _, err := os.Stat(badLock); err != nil {
		t.Errorf("malformed lock removed: %v", err)
	}
	if _, err := os.Stat(zeroLock); err != nil {
		t.Errorf("zero PID lock removed: %v", err)
	}
	if _, err := os.Stat(dead2Lock); !os.IsNotExist(err) {
		t.Errorf("dead lock with non-empty jsonl: lock should be removed, err=%v", err)
	}
	if _, err := os.Stat(dead2JSONL); err != nil {
		t.Errorf("non-empty jsonl was incorrectly removed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("unrelated file was touched: %v", err)
	}
}

func TestCleanupStaleSessions_NoHome(t *testing.T) {
	t.Setenv("HOME", "")
	os.Unsetenv("HOME")
	CleanupStaleSessions(context.Background())
}

func TestCleanupStaleSessions_MissingDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	CleanupStaleSessions(context.Background())
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

func FuzzParseLockFile(f *testing.F) {
	f.Add([]byte(`{"pid":1}`))
	f.Add([]byte(`{"pid":0}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Add([]byte{})
	f.Add(make([]byte, 4097))

	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, "fuzz.lock")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		pid, ok := parseLockFile(path)

		// Invariant 1: if ok, pid must be positive.
		if ok && pid <= 0 {
			t.Fatalf("ok=true but pid=%d", pid)
		}

		// Invariant 2: if input is valid JSON with pid>0 and
		// size<=4096, parseLockFile must return ok=true.
		if len(data) <= 4096 {
			var lf lockFile
			if json.Unmarshal(data, &lf) == nil && lf.PID > 0 {
				if !ok {
					t.Fatalf("valid JSON pid=%d size=%d but ok=false", lf.PID, len(data))
				}
			}
		}
	})
}

func BenchmarkParseLockFile(b *testing.B) {
	for _, n := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			dir := b.TempDir()
			paths := make([]string, n)
			for i := range n {
				body, _ := json.Marshal(lockFile{PID: i + 1})
				p := filepath.Join(dir, fmt.Sprintf("sess-%d.lock", i))
				if err := os.WriteFile(p, body, 0o644); err != nil {
					b.Fatal(err)
				}
				paths[i] = p
			}
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				for _, p := range paths {
					parseLockFile(p)
				}
			}
		})
	}
}

func BenchmarkLockfileWriteParse(b *testing.B) {
	dir := b.TempDir()
	body, _ := json.Marshal(lockFile{PID: os.Getpid()})
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			p := filepath.Join(dir, fmt.Sprintf("bench-%d.lock", i))
			_ = os.WriteFile(p, body, 0o644)
			parseLockFile(p)
			os.Remove(p)
			i++
		}
	})
}
