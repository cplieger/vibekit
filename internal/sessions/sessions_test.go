package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// newTestManager creates a Manager pointing at a temp sessions dir.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".kiro", "sessions", "cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	return New(dir)
}

// stubIsKiroCLI replaces the package-level IsKiroCLI probe for the
// duration of t.
func stubIsKiroCLI(t *testing.T, returns bool) {
	t.Helper()
	orig := IsKiroCLI
	IsKiroCLI = func(int) bool { return returns }
	t.Cleanup(func() { IsKiroCLI = orig })
}

func writeLock(t *testing.T, dir, id string, body []byte) string {
	t.Helper()
	path := filepath.Join(dir, id+".lock")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	return path
}

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
			mgr := newTestManager(t)
			if tc.setup != nil {
				tc.setup(t, mgr.Dir())
			}

			sid := "test-" + tc.name
			var path string
			if tc.lockContent == nil && tc.name == "NoLockFile" {
				mgr.RemoveStaleLock(context.Background(), "no-such-session")
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
				path = writeLockHeld(t, mgr.Dir(), sid, body)
			} else {
				path = writeLock(t, mgr.Dir(), sid, body)
			}

			mgr.RemoveStaleLock(context.Background(), sid)

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

func TestRemoveStaleLock_EmptyDir(t *testing.T) {
	mgr := New("")
	mgr.RemoveStaleLock(context.Background(), "anything")
}

func TestRemoveStaleLock_InvalidSessionID(t *testing.T) {
	mgr := newTestManager(t)
	outside := filepath.Join(filepath.Dir(mgr.Dir()), "outside.lock")
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

func TestCleanupStale_MixedLocks(t *testing.T) {
	mgr := newTestManager(t)
	stubIsKiroCLI(t, true)

	dead := deadPID(t)
	deadBody, _ := json.Marshal(lockFile{PID: dead})
	deadLock := writeLock(t, mgr.Dir(), "stale", deadBody)
	deadJSONL := filepath.Join(mgr.Dir(), "stale.jsonl")
	deadJSON := filepath.Join(mgr.Dir(), "stale.json")
	if err := os.WriteFile(deadJSONL, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deadJSON, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	liveBody, _ := json.Marshal(lockFile{PID: os.Getpid()})
	liveLock := writeLockHeld(t, mgr.Dir(), "live", liveBody)

	badLock := writeLock(t, mgr.Dir(), "bad", []byte("garbage"))

	zeroBody, _ := json.Marshal(lockFile{PID: 0})
	zeroLock := writeLock(t, mgr.Dir(), "zero", zeroBody)

	dead2 := deadPID(t)
	dead2Body, _ := json.Marshal(lockFile{PID: dead2})
	dead2Lock := writeLock(t, mgr.Dir(), "nonempty", dead2Body)
	dead2JSONL := filepath.Join(mgr.Dir(), "nonempty.jsonl")
	if err := os.WriteFile(dead2JSONL, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	other := filepath.Join(mgr.Dir(), "README.txt")
	if err := os.WriteFile(other, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr.CleanupStale(context.Background())

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

func TestCleanupStale_EmptyDir(t *testing.T) {
	mgr := New("")
	mgr.CleanupStale(context.Background())
}

func TestCleanupStale_MissingDir(t *testing.T) {
	mgr := New("/nonexistent/path/sessions")
	mgr.CleanupStale(context.Background())
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

		if ok && pid <= 0 {
			t.Fatalf("ok=true but pid=%d", pid)
		}

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

// captureSlog redirects the default slog logger to an in-memory text
// buffer for the duration of t and returns the buffer. CleanupStale and
// cleanupOneEntry emit their counting and log-gating decisions only
// through slog (the files they remove are identical regardless of those
// branches), so the slog records are the only externally observable
// signal for that logic.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	orig := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return &buf
}

// TestParseLockFile_SizeBoundary verifies the 4096-byte size guard is
// inclusive: a 4096-byte valid lock parses, one byte over is rejected.
func TestParseLockFile_SizeBoundary(t *testing.T) {
	dir := t.TempDir()

	writeSized := func(size int) string {
		t.Helper()
		head := []byte(`{"pid":4321}`)
		if size < len(head) {
			t.Fatalf("requested size %d smaller than header", size)
		}
		body := make([]byte, size)
		copy(body, head)
		for i := len(head); i < size; i++ {
			body[i] = ' ' // trailing whitespace keeps the JSON valid
		}
		p := filepath.Join(dir, fmt.Sprintf("sized-%d.lock", size))
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatalf("write %d-byte lock: %v", size, err)
		}
		return p
	}

	pid, ok := parseLockFile(writeSized(4096))
	if !ok || pid != 4321 {
		t.Errorf("parseLockFile(4096-byte valid lock) = (%d, %v), want (4321, true)", pid, ok)
	}

	if _, ok := parseLockFile(writeSized(4097)); ok {
		t.Errorf("parseLockFile(4097-byte lock) ok = true, want false")
	}
}

// TestFlockProbeStale_OpenSucceeds verifies an existing, un-held lock
// file is reported stale (the flock probe succeeds) and a missing file
// is reported not-stale (open fails).
func TestFlockProbeStale_OpenSucceeds(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "present.lock")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := flockProbeStale(existing); got != true {
		t.Errorf("flockProbeStale(existing-unheld) = %v, want true", got)
	}

	if got := flockProbeStale(filepath.Join(dir, "missing.lock")); got != false {
		t.Errorf("flockProbeStale(missing) = %v, want false", got)
	}
}

// TestRemoveStaleLock_LogsRemovedOnSuccess verifies a successful removal
// logs the success line and not the failure line.
func TestRemoveStaleLock_LogsRemovedOnSuccess(t *testing.T) {
	mgr := newTestManager(t)
	buf := captureSlog(t)

	dead := deadPID(t)
	body, _ := json.Marshal(lockFile{PID: dead})
	path := writeLock(t, mgr.Dir(), "stale-logs", body)

	mgr.RemoveStaleLock(context.Background(), "stale-logs")

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("precondition: stale lock not removed (err=%v)", err)
	}
	out := buf.String()
	if !strings.Contains(out, "removed stale lock") {
		t.Errorf("RemoveStaleLock log = %q, want it to contain %q", out, "removed stale lock")
	}
	if strings.Contains(out, "remove stale lock failed") {
		t.Errorf("RemoveStaleLock log = %q, must not contain the failure message on success", out)
	}
}

// TestCleanupStale_ScanTruncationBoundary verifies the scan stops at
// cleanupMaxEntries and logs the truncation warning. With cleanupMaxEntries+1
// non-lock entries the cap is hit at the last index.
func TestCleanupStale_ScanTruncationBoundary(t *testing.T) {
	mgr := newTestManager(t)
	buf := captureSlog(t)

	const total = cleanupMaxEntries + 1
	for i := range total {
		p := filepath.Join(mgr.Dir(), fmt.Sprintf("entry-%04d.txt", i))
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatalf("write entry %d: %v", i, err)
		}
	}

	mgr.CleanupStale(context.Background())

	if out := buf.String(); !strings.Contains(out, "cleanup scan truncated") {
		t.Errorf("CleanupStale(%d entries) log = %q, want it to contain %q",
			total, out, "cleanup scan truncated")
	}
}

// TestCleanupStale_CountsRemovedLock verifies that removing a stale lock
// whose companion jsonl is non-empty reports locks_removed=1 and
// empty_sessions_removed=0 in the aggregate log.
func TestCleanupStale_CountsRemovedLock(t *testing.T) {
	mgr := newTestManager(t)
	buf := captureSlog(t)

	dead := deadPID(t)
	body, _ := json.Marshal(lockFile{PID: dead})
	lockPath := writeLock(t, mgr.Dir(), "count-lock", body)
	jsonl := filepath.Join(mgr.Dir(), "count-lock.jsonl")
	if err := os.WriteFile(jsonl, []byte(`{"line":1}`), 0o644); err != nil {
		t.Fatalf("write non-empty jsonl: %v", err)
	}

	mgr.CleanupStale(context.Background())

	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("precondition: stale lock not removed (err=%v)", err)
	}
	out := buf.String()
	if !strings.Contains(out, "cleaned stale sessions") {
		t.Errorf("CleanupStale log = %q, want aggregate %q", out, "cleaned stale sessions")
	}
	if !strings.Contains(out, "locks_removed=1") {
		t.Errorf("CleanupStale log = %q, want %q", out, "locks_removed=1")
	}
	if !strings.Contains(out, "empty_sessions_removed=0") {
		t.Errorf("CleanupStale log = %q, want %q", out, "empty_sessions_removed=0")
	}
}

// TestCleanupStale_CountsRemovedEmptySession verifies that removing a
// stale lock with an empty jsonl and present companion json reports
// empty_sessions_removed=1 and does not warn on the successful json
// removal.
func TestCleanupStale_CountsRemovedEmptySession(t *testing.T) {
	mgr := newTestManager(t)
	buf := captureSlog(t)

	dead := deadPID(t)
	body, _ := json.Marshal(lockFile{PID: dead})
	lockPath := writeLock(t, mgr.Dir(), "count-sess", body)
	jsonl := filepath.Join(mgr.Dir(), "count-sess.jsonl")
	if err := os.WriteFile(jsonl, nil, 0o644); err != nil {
		t.Fatalf("write empty jsonl: %v", err)
	}
	jsonPath := filepath.Join(mgr.Dir(), "count-sess.json")
	if err := os.WriteFile(jsonPath, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write companion json: %v", err)
	}

	mgr.CleanupStale(context.Background())

	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("precondition: stale lock not removed (err=%v)", err)
	}
	out := buf.String()
	if !strings.Contains(out, "empty_sessions_removed=1") {
		t.Errorf("CleanupStale log = %q, want %q", out, "empty_sessions_removed=1")
	}
	if strings.Contains(out, "remove companion json failed") {
		t.Errorf("CleanupStale log = %q, must not warn on a successful json removal", out)
	}
	if !strings.Contains(out, "locks_removed=1") {
		t.Errorf("CleanupStale log = %q, want %q", out, "locks_removed=1")
	}
}

// TestCleanupStale_NoAggregateLogWhenNothingRemoved verifies that when a
// malformed lock causes nothing to be removed, the aggregate summary log
// is suppressed entirely.
func TestCleanupStale_NoAggregateLogWhenNothingRemoved(t *testing.T) {
	mgr := newTestManager(t)
	buf := captureSlog(t)

	writeLock(t, mgr.Dir(), "none-removed", []byte("not-json-garbage"))

	mgr.CleanupStale(context.Background())

	if out := buf.String(); strings.Contains(out, "cleaned stale sessions") {
		t.Errorf("CleanupStale with nothing removed logged the aggregate: %q", out)
	}
}
