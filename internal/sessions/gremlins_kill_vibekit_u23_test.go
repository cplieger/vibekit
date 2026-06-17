package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gk_vibekit_u23_captureSlog redirects the default slog logger to an
// in-memory text buffer for the duration of t and returns the buffer.
// The sessions package emits Info/Warn through slog.Default(); for the
// counting and log-gating logic in CleanupStale / cleanupOneEntry the
// slog records are the only externally observable signal (the files
// removed are identical regardless of those branches), so several
// mutants can only be killed by asserting on this output.
func gk_vibekit_u23_captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := slog.Default()
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return &buf
}

// Kills sessions.go:50:31 CONDITIONALS_BOUNDARY (`info.Size() > 4096`).
// At exactly 4096 bytes the guard is false so a valid lock parses; the
// mutation to `>=` rejects a 4096-byte file (ok=false).
func Test_gk_vibekit_u23_ParseLockFile_SizeBoundary(t *testing.T) {
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
		p := filepath.Join(dir, fmt.Sprintf("gku23-%d.lock", size))
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatalf("write %d-byte lock: %v", size, err)
		}
		return p
	}

	pid, ok := parseLockFile(writeSized(4096))
	if !ok || pid != 4321 {
		t.Errorf("parseLockFile(4096-byte valid lock) = (%d, %v), want (4321, true)", pid, ok)
	}

	// One byte over the cap is rejected by both original and mutant; sanity.
	if _, ok := parseLockFile(writeSized(4097)); ok {
		t.Errorf("parseLockFile(4097-byte lock) ok = true, want false")
	}
}

// Kills sessions.go:79:9 CONDITIONALS_NEGATION (`err != nil` in
// flockProbeStale). For an existing, un-held file os.Open succeeds so the
// guard is false and the function flocks it successfully -> true. Negating
// to `err == nil` would return false on the same input.
func Test_gk_vibekit_u23_FlockProbeStale_OpenSucceeds(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "present.lock")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := flockProbeStale(existing); got != true {
		t.Errorf("flockProbeStale(existing-unheld) = %v, want true", got)
	}

	// Missing path: open fails -> false (original and mutant agree); sanity.
	if got := flockProbeStale(filepath.Join(dir, "missing.lock")); got != false {
		t.Errorf("flockProbeStale(missing) = %v, want false", got)
	}
}

// Kills sessions.go:123:37 CONDITIONALS_NEGATION (`err != nil` after
// os.Remove in RemoveStaleLock). A successful remove (err==nil) skips the
// failure branch and logs Info "removed stale lock"; negating to `err ==
// nil` takes the failure branch and logs "remove stale lock failed".
func Test_gk_vibekit_u23_RemoveStaleLock_LogsRemovedOnSuccess(t *testing.T) {
	mgr := newTestManager(t)
	buf := gk_vibekit_u23_captureSlog(t)

	dead := deadPID(t)
	body, _ := json.Marshal(lockFile{PID: dead})
	path := writeLock(t, mgr.Dir(), "gku23stale", body)

	mgr.RemoveStaleLock(context.Background(), "gku23stale")

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

// Kills sessions.go:153:8 CONDITIONALS_BOUNDARY (`i >= cleanupMaxEntries`).
// cleanupMaxEntries is 1000; with exactly 1001 directory entries the guard
// fires at index 1000 (break + "cleanup scan truncated"). Mutated to
// `i > cleanupMaxEntries` it would process index 1000 and never warn.
// Non-.lock files keep `valid` empty so no removal work runs.
func Test_gk_vibekit_u23_CleanupStale_ScanTruncationBoundary(t *testing.T) {
	mgr := newTestManager(t)
	buf := gk_vibekit_u23_captureSlog(t)

	const total = cleanupMaxEntries + 1
	for i := range total {
		p := filepath.Join(mgr.Dir(), fmt.Sprintf("gku23entry-%04d.txt", i))
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

// Kills sessions.go:180:13 NEG (`locks > 0`), 181:14 NEG
// (`locks > math.MaxInt32`), and 198:8 NEG (`rl > 0`) in CleanupStale.
// A stale lock with a NON-empty companion jsonl removes the lock only
// (removedSessions stays 0). The aggregate log must report
// locks_removed=1: `locks > 0` negated keeps rl at 0 (no aggregate log);
// `locks > math.MaxInt32` negated clamps locks to 2147483647; `rl > 0`
// negated drops the aggregate log entirely.
func Test_gk_vibekit_u23_CleanupStale_CountsRemovedLock(t *testing.T) {
	mgr := newTestManager(t)
	buf := gk_vibekit_u23_captureSlog(t)

	dead := deadPID(t)
	body, _ := json.Marshal(lockFile{PID: dead})
	lockPath := writeLock(t, mgr.Dir(), "gku23pos", body)
	jsonl := filepath.Join(mgr.Dir(), "gku23pos.jsonl")
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

// Kills sessions.go:186:16 NEG (`sessions > 0`), 187:17 NEG
// (`sessions > math.MaxInt32`), and 247:41 NEG (`rmErr != nil && ...` in
// cleanupOneEntry). A stale lock with an EMPTY jsonl and a present
// companion .json removes the empty session (removedSessions=1). The
// aggregate log must report empty_sessions_removed=1: `sessions > 0`
// negated keeps rs at 0; `sessions > math.MaxInt32` negated clamps rs to
// 2147483647; `rmErr != nil` negated to `rmErr == nil` treats the
// successful json removal as a failure (rs=0 + "remove companion json
// failed").
func Test_gk_vibekit_u23_CleanupStale_CountsRemovedEmptySession(t *testing.T) {
	mgr := newTestManager(t)
	buf := gk_vibekit_u23_captureSlog(t)

	dead := deadPID(t)
	body, _ := json.Marshal(lockFile{PID: dead})
	lockPath := writeLock(t, mgr.Dir(), "gku23sess", body)
	jsonl := filepath.Join(mgr.Dir(), "gku23sess.jsonl")
	if err := os.WriteFile(jsonl, nil, 0o644); err != nil {
		t.Fatalf("write empty jsonl: %v", err)
	}
	jsonPath := filepath.Join(mgr.Dir(), "gku23sess.json")
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

// Kills sessions.go:198:8 BOUNDARY+NEG (`rl > 0`) and 198:18 BOUNDARY+NEG
// (`rs > 0`). A malformed lock parses with ok=false so nothing is removed
// (removedLocks==0, removedSessions==0) and the guard `rl > 0 || rs > 0`
// is false -> no aggregate log. Every boundary/negation mutation of either
// operand makes a zero count satisfy the guard (`>=`/`<=` at 0 are true),
// producing a spurious aggregate log.
func Test_gk_vibekit_u23_CleanupStale_NoAggregateLogWhenNothingRemoved(t *testing.T) {
	mgr := newTestManager(t)
	buf := gk_vibekit_u23_captureSlog(t)

	writeLock(t, mgr.Dir(), "gku23none", []byte("not-json-garbage"))

	mgr.CleanupStale(context.Background())

	if out := buf.String(); strings.Contains(out, "cleaned stale sessions") {
		t.Errorf("CleanupStale with nothing removed logged the aggregate: %q", out)
	}
}
