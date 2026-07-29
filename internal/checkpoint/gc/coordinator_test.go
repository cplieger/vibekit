package gc

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- Unit tests (tarch-gc-coordinator-unit-p1) ---

func TestSweepBlobs_ReferencedBlobsSurvive(t *testing.T) {
	t.Parallel()
	blobsDir := t.TempDir()
	plantBlob(t, blobsDir, "aabbccdd1234567890abcdef1234567890abcdef1234567890abcdef12345678", time.Now().Add(-10*time.Minute))
	plantBlob(t, blobsDir, "11223344aabbccddee112233445566778899aabbccddeeff0011223344556677", time.Now().Add(-10*time.Minute))

	referenced := map[string]struct{}{
		"aabbccdd1234567890abcdef1234567890abcdef1234567890abcdef12345678": {},
	}

	gc := &Coordinator{blobsDir: blobsDir}
	removed, scanned, err := gc.sweepBlobs(context.Background(), referenced)
	if err != nil {
		t.Fatal(err)
	}
	if scanned != 2 {
		t.Errorf("scanned = %d, want 2", scanned)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	// Referenced blob must still exist.
	assertBlobExists(t, blobsDir, "aabbccdd1234567890abcdef1234567890abcdef1234567890abcdef12345678")
}

func TestSweepBlobs_UnreferencedOldBlobsRemoved(t *testing.T) {
	t.Parallel()
	blobsDir := t.TempDir()
	hash := "aabbccdd1234567890abcdef1234567890abcdef1234567890abcdef12345678"
	plantBlob(t, blobsDir, hash, time.Now().Add(-10*time.Minute))

	gc := &Coordinator{blobsDir: blobsDir}
	removed, _, err := gc.sweepBlobs(context.Background(), map[string]struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
}

func TestSweepBlobs_YoungBlobsSurvive(t *testing.T) {
	t.Parallel()
	blobsDir := t.TempDir()
	hash := "aabbccdd1234567890abcdef1234567890abcdef1234567890abcdef12345678"
	plantBlob(t, blobsDir, hash, time.Now()) // recent mtime

	gc := &Coordinator{blobsDir: blobsDir}
	removed, scanned, err := gc.sweepBlobs(context.Background(), map[string]struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if scanned != 1 {
		t.Errorf("scanned = %d, want 1", scanned)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 (young blob should survive)", removed)
	}
	assertBlobExists(t, blobsDir, hash)
}

func TestCollectReferencedBlobs_MergesCachedAndUncached(t *testing.T) {
	t.Parallel()
	chatsDir := t.TempDir()

	// Create a cached chat with known refs — must have a directory in chatsDir.
	cachedRefs := &fakeBlobRefer{shas: []string{"sha-cached-1", "sha-cached-2"}}
	cached := map[string]BlobRefer{"chat-cached": cachedRefs}
	if err := os.MkdirAll(filepath.Join(chatsDir, "chat-cached"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create an uncached chat with an events.jsonl file.
	uncachedDir := filepath.Join(chatsDir, "chat-uncached")
	if err := os.MkdirAll(uncachedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	eventsContent := `{"before_sha":"sha-uncached-1","after_sha":"sha-uncached-2"}` + "\n"
	if err := os.WriteFile(filepath.Join(uncachedDir, "events.jsonl"), []byte(eventsContent), 0o644); err != nil {
		t.Fatal(err)
	}

	gc := &Coordinator{chatsDir: chatsDir, eventsFile: "events.jsonl", cached: func() map[string]BlobRefer { return cached }}
	refs, err := gc.collectReferencedBlobs(context.Background(), cached)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"sha-cached-1", "sha-cached-2", "sha-uncached-1", "sha-uncached-2"} {
		if _, ok := refs[want]; !ok {
			t.Errorf("missing expected ref %q", want)
		}
	}
}

func TestRunOnce_EmptyBlobDir(t *testing.T) {
	t.Parallel()
	blobsDir := filepath.Join(t.TempDir(), "nonexistent")
	chatsDir := t.TempDir()

	gc := &Coordinator{
		blobsDir: blobsDir,
		chatsDir: chatsDir,
		cached:   func() map[string]BlobRefer { return nil },
	}
	removed, scanned, err := gc.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 || scanned != 0 {
		t.Errorf("expected 0/0, got %d/%d", removed, scanned)
	}
}

// --- Lifecycle tests (tarch-gc-coordinator-lifecycle-p3) ---

func TestCoordinator_StartStop_Idempotent(t *testing.T) {
	t.Parallel()
	gc := newTestCoordinator(t)
	ctx := context.Background()

	gc.Start(ctx)
	gc.Start(ctx) // second start is no-op
	gc.Stop()
	gc.Stop() // second stop is no-op — no panic
}

func TestCoordinator_StopBeforeStart(t *testing.T) {
	t.Parallel()
	gc := newTestCoordinator(t)
	gc.Stop() // no panic on fresh coordinator
}

func TestCoordinator_ContextCancellation(t *testing.T) {
	t.Parallel()
	blobsDir := t.TempDir()
	hash := "aabbccdd1234567890abcdef1234567890abcdef1234567890abcdef12345678"
	plantBlob(t, blobsDir, hash, time.Now().Add(-10*time.Minute))

	gc := &Coordinator{
		blobsDir: blobsDir,
		chatsDir: t.TempDir(),
		interval: time.Hour, // won't tick during test
		cached:   func() map[string]BlobRefer { return nil },
		stopCh:   make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	_, _, _ = gc.RunOnce(ctx)
	// Blob should survive because context was cancelled.
	assertBlobExists(t, blobsDir, hash)
}

// --- Benchmark (tarch-gc-sweep-benchmark-p7) ---

func BenchmarkSweepBlobs(b *testing.B) {
	blobsDir := b.TempDir()
	const total = 1000
	referenced := make(map[string]struct{}, total/2)

	for i := range total {
		hash := blobHash(i)
		plantBlobB(b, blobsDir, hash, time.Now().Add(-10*time.Minute))
		if i%2 == 0 {
			referenced[hash] = struct{}{}
		}
	}

	gc := &Coordinator{blobsDir: blobsDir}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		// Re-plant removed blobs for next iteration.
		for i := range total {
			if i%2 != 0 {
				plantBlobB(b, blobsDir, blobHash(i), time.Now().Add(-10*time.Minute))
			}
		}
		removed, scanned, err := gc.sweepBlobs(context.Background(), referenced)
		if err != nil {
			b.Fatal(err)
		}
		if removed != total/2 {
			b.Fatalf("removed = %d, want %d", removed, total/2)
		}
		_ = scanned
	}
}

// --- Helpers ---

func newTestCoordinator(t *testing.T) *Coordinator {
	t.Helper()
	return &Coordinator{
		blobsDir: t.TempDir(),
		chatsDir: t.TempDir(),
		interval: time.Hour,
		cached:   func() map[string]BlobRefer { return nil },
		stopCh:   make(chan struct{}),
	}
}

func plantBlob(t *testing.T, blobsDir, hash string, mtime time.Time) {
	t.Helper()
	prefix := hash[:2]
	rest := hash[2:]
	dir := filepath.Join(blobsDir, prefix)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, rest)
	if err := os.WriteFile(p, []byte("blob"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func plantBlobB(b *testing.B, blobsDir, hash string, mtime time.Time) {
	b.Helper()
	prefix := hash[:2]
	rest := hash[2:]
	dir := filepath.Join(blobsDir, prefix)
	_ = os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, rest)
	_ = os.WriteFile(p, []byte("blob"), 0o644)
	_ = os.Chtimes(p, mtime, mtime)
}

func assertBlobExists(t *testing.T, blobsDir, hash string) {
	t.Helper()
	p := filepath.Join(blobsDir, hash[:2], hash[2:])
	if _, err := os.Stat(p); err != nil {
		t.Errorf("blob %s should exist but got: %v", hash, err)
	}
}

func blobHash(i int) string {
	// Generate a deterministic 64-char hex hash.
	s := fmt.Sprintf("%064x", i)
	return s
}

type fakeBlobRefer struct {
	shas []string
}

func (f *fakeBlobRefer) ReferencedBlobs(_ context.Context) []string {
	return f.shas
}

// --- Error / cancellation / boundary behavior (folded from per-source tests) ---

// Start must lazily create the stop channel when the Coordinator was
// constructed with a nil stopCh, otherwise Stop has nothing to close.
func TestCoordinator_Start_CreatesStopChannelWhenNil(t *testing.T) {
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
		t.Errorf("Start() with nil stopCh left gc.stopCh nil; want it lazily created")
	}
}

// RunOnce must surface a collection error and report zero counts,
// not silently treat the failed collection as a successful empty sweep.
func TestRunOnce_PropagatesCollectError(t *testing.T) {
	t.Parallel()
	gc := &Coordinator{
		chatsDir: writeNonDirFile(t, "x"), // a file → ReadDir ENOTDIR → collect error
		blobsDir: t.TempDir(),
		cached:   func() map[string]BlobRefer { return nil },
	}
	removed, scanned, err := gc.RunOnce(context.Background())
	if err == nil {
		t.Errorf("RunOnce() with unreadable chatsDir: err = nil, want non-nil")
	}
	if removed != 0 || scanned != 0 {
		t.Errorf("RunOnce() on collect error = (removed=%d, scanned=%d), want (0, 0)", removed, scanned)
	}
}

// A failed reference collection must skip the sweep entirely: sweeping with
// an empty referenced-set would delete every blob. The planted old blob
// must survive.
func TestRunOnce_SkipsSweepOnCollectError(t *testing.T) {
	t.Parallel()
	blobsDir := t.TempDir()
	hash := "ab" + strings.Repeat("0", 62)
	plantBlob(t, blobsDir, hash, time.Now().Add(-10*time.Minute)) // old, unreferenced
	gc := &Coordinator{
		chatsDir: writeNonDirFile(t, "x"), // a file → collect error
		blobsDir: blobsDir,
		cached:   func() map[string]BlobRefer { return nil },
	}
	_, _, _ = gc.RunOnce(context.Background())
	assertBlobExists(t, blobsDir, hash)
}

// A sweep error that is not a cancellation is logged at the failure level.
func TestRunOnce_LogsFailedOnSweepError(t *testing.T) {
	// NOT parallel: captures the global slog default.
	gc := &Coordinator{
		chatsDir: t.TempDir(),             // empty → collect succeeds
		blobsDir: writeNonDirFile(t, "x"), // a file → sweep error (ENOTDIR)
		cached:   func() map[string]BlobRefer { return nil },
	}
	out := captureSlog(t, func() {
		_, _, _ = gc.RunOnce(context.Background())
	})
	if !strings.Contains(out, "blob GC failed") {
		t.Errorf("runOnce on sweep error: log = %q, want it to contain %q", out, "blob GC failed")
	}
}

// A sweep cut short by context cancellation logs the cancellation path, not
// the generic failure path.
func TestRunOnce_LogsCancelledOnContextCancel(t *testing.T) {
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
	out := captureSlog(t, func() {
		_, _, _ = gc.RunOnce(ctx)
	})
	if !strings.Contains(out, "cancelled during sweep") {
		t.Errorf("runOnce on cancelled sweep: log = %q, want it to contain %q", out, "cancelled during sweep")
	}
}

// When nothing is removed, the prefix directory is left in place: the
// empty-dir cleanup only runs after a real removal.
func TestSweepFanout_EmptyDirSurvivesWhenNothingRemoved(t *testing.T) {
	t.Parallel()
	prefixDir := filepath.Join(t.TempDir(), "ab")
	if err := os.MkdirAll(prefixDir, 0o755); err != nil {
		t.Fatal(err)
	}
	removed, scanned := sweepFanout(context.Background(), prefixDir, "ab", map[string]struct{}{})
	if removed != 0 || scanned != 0 {
		t.Fatalf("sweepFanout(empty dir) = (removed=%d, scanned=%d), want (0, 0)", removed, scanned)
	}
	if _, err := os.Stat(prefixDir); err != nil {
		t.Errorf("sweepFanout with removed==0 removed the empty prefix dir %q (err=%v); it must survive", prefixDir, err)
	}
}

// Removing the last blob in a prefix directory also removes the now-empty
// directory.
func TestSweepFanout_RemovesEmptiedDir(t *testing.T) {
	t.Parallel()
	blobsDir := t.TempDir()
	hash := "ef" + strings.Repeat("2", 62)
	plantBlob(t, blobsDir, hash, time.Now().Add(-10*time.Minute)) // old, unreferenced
	prefixDir := filepath.Join(blobsDir, "ef")
	removed, scanned := sweepFanout(context.Background(), prefixDir, "ef", map[string]struct{}{})
	if removed != 1 || scanned != 1 {
		t.Fatalf("sweepFanout(one removable blob) = (removed=%d, scanned=%d), want (1, 1)", removed, scanned)
	}
	if _, err := os.Stat(prefixDir); !os.IsNotExist(err) {
		t.Errorf("sweepFanout emptied the dir but did not remove %q (stat err=%v); want it removed", prefixDir, err)
	}
}

// An unreadable chat log must abort the whole collection so the sweep never
// proceeds with an incomplete referenced-set and mass-deletes live blobs. An
// oversized events.jsonl (past the size cap) is the trigger; the operator
// breadcrumb must be logged.
func TestCollectReferencedBlobs_AbortsOnUnreadableChatLog(t *testing.T) {
	// NOT parallel: captures the global slog default.
	chatsDir := t.TempDir()
	chatDir := filepath.Join(chatsDir, "chat-bad")
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Sparse file one byte past the cap: streamEventSHAs rejects it, which
	// must propagate as a collection error rather than a silent skip.
	f, err := os.Create(filepath.Join(chatDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxEventLogBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	gc := &Coordinator{
		chatsDir:   chatsDir,
		eventsFile: "events.jsonl",
		cached:     func() map[string]BlobRefer { return nil },
	}
	var refs map[string]struct{}
	var collectErr error
	out := captureSlog(t, func() {
		refs, collectErr = gc.collectReferencedBlobs(context.Background(), nil)
	})
	if collectErr == nil {
		t.Errorf("collectReferencedBlobs with an oversized chat log: err = nil, want non-nil (sweep must abort)")
	}
	if refs != nil {
		t.Errorf("collectReferencedBlobs on error returned non-nil refs %v, want nil", refs)
	}
	if !strings.Contains(out, "blocked by unreadable chat log") {
		t.Errorf("collectReferencedBlobs on unreadable log: log = %q, want the safety breadcrumb", out)
	}
}

// captureSlog swaps the global slog default for a buffer-backed handler, runs
// fn synchronously, restores the previous default, and returns the captured
// text. Callers MUST NOT use t.Parallel(): the swap touches the process-global
// slog default. Non-parallel tests run during the serial phase, so no
// concurrent logging races the swap.
func captureSlog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

// writeNonDirFile writes content to a fresh file under a temp dir and returns
// the path. Used to make a non-directory path so os.ReadDir fails with
// ENOTDIR (a non-IsNotExist error).
func writeNonDirFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
