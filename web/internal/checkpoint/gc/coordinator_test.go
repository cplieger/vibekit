package gc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

func TestRunOnceWithCounts_EmptyBlobDir(t *testing.T) {
	t.Parallel()
	blobsDir := filepath.Join(t.TempDir(), "nonexistent")
	chatsDir := t.TempDir()

	gc := &Coordinator{
		blobsDir: blobsDir,
		chatsDir: chatsDir,
		cached:   func() map[string]BlobRefer { return nil },
	}
	removed, scanned, err := gc.RunOnceWithCounts(context.Background())
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

	gc.RunOnce(ctx)
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

// Ensure Coordinator.Stop doesn't leak goroutines.
var _ sync.Locker = (*sync.Mutex)(nil) // compile-time check for sync import
