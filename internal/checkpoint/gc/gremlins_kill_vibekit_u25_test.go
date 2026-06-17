package gc

// Mutant-killing tests for unit vibekit-u25 (package internal/checkpoint/gc).
// Targets surviving gremlins mutants in coordinator.go and events.go.
// All new identifiers are prefixed gk_vibekit_u25_ to avoid collisions.
//
// Two mutants in coordinator.go are intentionally NOT given a test because
// they are equivalent (no input can make an assertion differ):
//   - coordinator.go:252:36 CONDITIONALS_BOUNDARY  (time.Since(modTime) >= BlobGCMinAge):
//     the >= vs > boundary only differs when the age is EXACTLY BlobGCMinAge;
//     that instant is unreachable with the wall-clock time.Since, so no
//     deterministic input distinguishes >= from >.
//   - coordinator.go:283:19 CONDITIONALS_BOUNDARY  (len(uncached) > 0):
//     the > vs >= boundary only differs when len(uncached)==0; entering the
//     block then calls collectUncachedBlobs(ctx, []) which is a pure no-op
//     (returns nil,nil, no side effects), leaving the referenced map and all
//     observable state identical. Equivalent.

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gk_vibekit_u25_captureSlog swaps the global slog default for a buffer-backed
// handler, runs fn synchronously, restores the previous default, and returns
// the captured text. Callers MUST NOT use t.Parallel(): the swap touches the
// process-global slog default. Non-parallel tests run during the serial phase
// (parallel tests are paused at t.Parallel), so no concurrent logging occurs.
func gk_vibekit_u25_captureSlog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

// gk_vibekit_u25_writeFile writes content to a fresh file under a temp dir and
// returns the path. Used to make a non-directory path so os.ReadDir fails with
// ENOTDIR (a non-IsNotExist error).
func gk_vibekit_u25_writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// gk_vibekit_u25_sparseFile creates a sparse file of exactly size bytes (all
// zero bytes) named events.jsonl and returns its path. Cheap on disk; lets a
// test hit the size-cap boundary exactly without writing 100 MiB.
func gk_vibekit_u25_sparseFile(t *testing.T, size int64) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "events.jsonl")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func gk_vibekit_u25_isSizeCapErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "exceeds size cap")
}

// --- coordinator.go:71:15 CONDITIONALS_NEGATION ---
// `if gc.stopCh == nil` lazily creates the stop channel. Negating to `!= nil`
// leaves a nil-constructed Coordinator's stopCh nil after Start.
func Test_gk_vibekit_u25_Start_createsStopChWhenNil(t *testing.T) {
	gc := &Coordinator{
		blobsDir: t.TempDir(),
		chatsDir: t.TempDir(),
		interval: time.Hour,
		cached:   func() map[string]BlobRefer { return nil },
		// stopCh deliberately left nil so the lazy-create branch must run.
	}
	gc.Start(context.Background())
	gc.mu.Lock()
	created := gc.stopCh != nil
	gc.mu.Unlock()
	gc.Stop()
	if !created {
		t.Errorf("Start() with nil stopCh: gc.stopCh = nil, want non-nil (the `== nil` lazy-create branch must run)")
	}
}

// --- coordinator.go:130:9 CONDITIONALS_NEGATION ---
// RunOnceWithCounts: `if err != nil { return 0,0,err }` after collection.
// Negating to `== nil` swallows the collect error and reports success.
func Test_gk_vibekit_u25_RunOnceWithCounts_propagatesCollectError(t *testing.T) {
	t.Parallel()
	gc := &Coordinator{
		chatsDir: gk_vibekit_u25_writeFile(t, "x"), // a file → ReadDir ENOTDIR → collect error
		blobsDir: t.TempDir(),
		cached:   func() map[string]BlobRefer { return nil },
	}
	removed, scanned, err := gc.RunOnceWithCounts(context.Background())
	if err == nil {
		t.Errorf("RunOnceWithCounts() with unreadable chatsDir: err = nil, want non-nil (must return early on collect error)")
	}
	if removed != 0 || scanned != 0 {
		t.Errorf("RunOnceWithCounts() on collect error = (removed=%d, scanned=%d), want (0, 0)", removed, scanned)
	}
}

// --- coordinator.go:143:9 CONDITIONALS_NEGATION ---
// runOnceInternal: `if err != nil` early-returns before sweeping. Negating to
// `== nil` proceeds to sweepBlobs(nil) which would delete unreferenced blobs.
func Test_gk_vibekit_u25_runOnceInternal_skipsSweepOnCollectError(t *testing.T) {
	t.Parallel()
	blobsDir := t.TempDir()
	hash := "ab" + strings.Repeat("0", 62)
	plantBlob(t, blobsDir, hash, time.Now().Add(-10*time.Minute)) // old, unreferenced
	gc := &Coordinator{
		chatsDir: gk_vibekit_u25_writeFile(t, "x"), // a file → collect error
		blobsDir: blobsDir,
		cached:   func() map[string]BlobRefer { return nil },
	}
	gc.RunOnce(context.Background())
	// Original: early return on collect error → no sweep → blob survives.
	// Mutant (== nil): no early return → sweepBlobs(nil) removes the old blob.
	assertBlobExists(t, blobsDir, hash)
}

// --- coordinator.go:157:14 CONDITIONALS_NEGATION ---
// runOnceInternal: `if sweepErr != nil` selects the failure log. Negating to
// `== nil` logs "blob GC finished" instead of "blob GC failed" on a real error.
func Test_gk_vibekit_u25_runOnceInternal_logsFailedOnSweepError(t *testing.T) {
	// NOT parallel: captures the global slog default.
	gc := &Coordinator{
		chatsDir: t.TempDir(),                      // empty → collect succeeds
		blobsDir: gk_vibekit_u25_writeFile(t, "x"), // a file → sweep error (ENOTDIR)
		cached:   func() map[string]BlobRefer { return nil },
	}
	out := gk_vibekit_u25_captureSlog(t, func() {
		gc.RunOnce(context.Background())
	})
	if !strings.Contains(out, "blob GC failed") {
		t.Errorf("runOnceInternal on sweep error: log = %q, want it to contain %q (the `sweepErr != nil` branch)", out, "blob GC failed")
	}
}

// --- coordinator.go:158:16 CONDITIONALS_NEGATION ---
// runOnceInternal: nested `if ctx.Err() != nil` selects "cancelled during
// sweep" vs "blob GC failed". Negating to `== nil` logs the wrong message.
func Test_gk_vibekit_u25_runOnceInternal_logsCancelledOnCtxCancel(t *testing.T) {
	// NOT parallel: captures the global slog default.
	blobsDir := t.TempDir()
	hash := "cd" + strings.Repeat("1", 62)
	plantBlob(t, blobsDir, hash, time.Now().Add(-10*time.Minute)) // creates the "cd" fanout dir
	gc := &Coordinator{
		chatsDir: t.TempDir(), // empty → collect returns without touching ctx
		blobsDir: blobsDir,
		cached:   func() map[string]BlobRefer { return nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: sweepBlobs returns ctx error at the first fanout entry
	out := gk_vibekit_u25_captureSlog(t, func() {
		gc.RunOnce(ctx)
	})
	if !strings.Contains(out, "cancelled during sweep") {
		t.Errorf("runOnceInternal on cancelled sweep: log = %q, want it to contain %q (the `ctx.Err() != nil` branch)", out, "cancelled during sweep")
	}
}

// --- coordinator.go:239:13 CONDITIONALS_BOUNDARY ---
// sweepFanout: `if removed > 0` guards the empty-dir cleanup. The boundary
// `>= 0` (and the negation `<= 0`) is true at removed==0, removing an empty
// prefix dir that must survive when nothing was collected.
func Test_gk_vibekit_u25_sweepFanout_emptyDirSurvivesWhenNothingRemoved(t *testing.T) {
	t.Parallel()
	prefixDir := filepath.Join(t.TempDir(), "ab")
	if err := os.MkdirAll(prefixDir, 0o755); err != nil {
		t.Fatal(err)
	}
	removed, scanned := sweepFanout(context.Background(), prefixDir, "ab", map[string]struct{}{})
	if removed != 0 || scanned != 0 {
		t.Fatalf("sweepFanout(empty dir) = (removed=%d, scanned=%d), want (0, 0)", removed, scanned)
	}
	// Original (removed > 0 == false): the empty dir is NOT removed.
	if _, err := os.Stat(prefixDir); err != nil {
		t.Errorf("sweepFanout with removed==0 removed the empty prefix dir %q (err=%v); it must survive", prefixDir, err)
	}
}

// --- coordinator.go:239:13 CONDITIONALS_NEGATION (removed > 0 -> removed <= 0) ---
// --- coordinator.go:240:43 CONDITIONALS_NEGATION (rerr == nil -> rerr != nil) ---
// --- coordinator.go:240:64 CONDITIONALS_NEGATION (len(after) == 0 -> len(after) != 0) ---
// When sweepFanout removes the only (old, unreferenced) blob, the now-empty
// prefix dir is itself removed. Each of the three negations skips that removal.
func Test_gk_vibekit_u25_sweepFanout_removesEmptiedDir(t *testing.T) {
	t.Parallel()
	blobsDir := t.TempDir()
	hash := "ef" + strings.Repeat("2", 62)
	plantBlob(t, blobsDir, hash, time.Now().Add(-10*time.Minute)) // old, unreferenced
	prefixDir := filepath.Join(blobsDir, "ef")
	removed, scanned := sweepFanout(context.Background(), prefixDir, "ef", map[string]struct{}{})
	if removed != 1 || scanned != 1 {
		t.Fatalf("sweepFanout(one removable blob) = (removed=%d, scanned=%d), want (1, 1)", removed, scanned)
	}
	// Original: removed>0 && rerr==nil && len(after)==0 → os.Remove(prefixDir).
	if _, err := os.Stat(prefixDir); !os.IsNotExist(err) {
		t.Errorf("sweepFanout emptied the dir but did not remove %q (stat err=%v); want it removed", prefixDir, err)
	}
}

// --- events.go:31:17 CONDITIONALS_BOUNDARY ---
// readEventLog: `if info.Size() > maxEventLogBytes` must be a strict '>'. The
// '>=' mutant rejects a file that is exactly at the cap.
func Test_gk_vibekit_u25_readEventLog_sizeCapBoundaryStrict(t *testing.T) {
	t.Parallel()
	// Exactly at the cap: strict '>' → not over cap → no size-cap error.
	if _, err := readEventLog(gk_vibekit_u25_sparseFile(t, maxEventLogBytes)); gk_vibekit_u25_isSizeCapErr(err) {
		t.Errorf("readEventLog at exactly the cap returned a size-cap error; boundary must be strict '>' (got %v)", err)
	}
	// One byte over the cap: size-cap error under both '>' and '>='.
	if _, err := readEventLog(gk_vibekit_u25_sparseFile(t, maxEventLogBytes+1)); !gk_vibekit_u25_isSizeCapErr(err) {
		t.Errorf("readEventLog one byte over the cap: err = %v, want a size-cap error", err)
	}
}

// --- events.go:65:17 CONDITIONALS_BOUNDARY ---
// streamEventSHAs: same strict-'>' size-cap boundary as readEventLog.
func Test_gk_vibekit_u25_streamEventSHAs_sizeCapBoundaryStrict(t *testing.T) {
	t.Parallel()
	if _, err := streamEventSHAs(gk_vibekit_u25_sparseFile(t, maxEventLogBytes)); gk_vibekit_u25_isSizeCapErr(err) {
		t.Errorf("streamEventSHAs at exactly the cap returned a size-cap error; boundary must be strict '>' (got %v)", err)
	}
	if _, err := streamEventSHAs(gk_vibekit_u25_sparseFile(t, maxEventLogBytes+1)); !gk_vibekit_u25_isSizeCapErr(err) {
		t.Errorf("streamEventSHAs one byte over the cap: err = %v, want a size-cap error", err)
	}
}
