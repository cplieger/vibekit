package checkpoint

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlobStorePutGet(t *testing.T) {
	b := newBlobStore(t.TempDir())
	data := []byte("hello, checkpoint")
	hash, err := b.Put(context.Background(), data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if hash != hashOf(data) {
		t.Fatalf("hash mismatch: got %q, want %q", hash, hashOf(data))
	}
	got, err := b.Get(context.Background(), hash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("content mismatch: got %q, want %q", got, data)
	}
}

func TestBlobStoreDedup(t *testing.T) {
	// Putting identical content twice must produce one on-disk file
	// and the same hash. This is the core value-add over the old
	// git-per-chat approach: two chats editing the same file to the
	// same bytes share one blob.
	b := newBlobStore(t.TempDir())
	data := []byte("shared content")
	h1, err := b.Put(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := b.Put(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("dedup: hashes differ %q vs %q", h1, h2)
	}
	if !b.Exists(h1) {
		t.Errorf("Exists after Put = false")
	}
}

func TestBlobStoreEmptyContent(t *testing.T) {
	// Empty file contents are a real case — a fresh `touch foo` or
	// a tool that clears a file. Must have a stable hash and round
	// trip cleanly.
	b := newBlobStore(t.TempDir())
	h, err := b.Put(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Get(context.Background(), h)
	if err != nil {
		t.Fatalf("Get empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty blob read = %d bytes, want 0", len(got))
	}
}

func TestBlobStoreNotFound(t *testing.T) {
	b := newBlobStore(t.TempDir())
	// SHA-256 of the literal string "never-stored".
	if _, err := b.Get(context.Background(), "d19e8be9c55b21bd6c9e93bcaf0f1a2e3f0c4a0b1d2e3f4a5b6c7d8e9f0a1b2c3"); err != ErrBlobNotFound {
		t.Errorf("Get missing = %v, want ErrBlobNotFound", err)
	}
	if b.Exists("not-a-hash") {
		t.Errorf("Exists(bogus) = true, want false")
	}
}

func TestBlobStoreAtomicWrite(t *testing.T) {
	// After a successful Put, no temp files should be left in the
	// fanout dir. Atomicity hinges on the temp+rename pattern; a
	// bug that leaks the original temp would show up here.
	root := t.TempDir()
	b := newBlobStore(root)
	data := []byte("atomic-test")
	hash, err := b.Put(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	fanout := filepath.Join(blobsRoot(root), hash[:2])
	entries, err := os.ReadDir(fanout)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("fanout dir has %d entries, want 1 (real blob only, no leaked temps)", len(entries))
	}
	if entries[0].Name() != hash[2:] {
		t.Errorf("unexpected entry %q", entries[0].Name())
	}
}

func TestBlobStoreInvalidHash(t *testing.T) {
	// Empty or 1-2 char hashes are impossible in practice (SHA-256
	// is always 64 hex chars) but defensive handling matters because
	// a corrupted events.jsonl could feed garbage in. pathFor must
	// reject; Get must return ErrBlobNotFound; Exists must be false.
	b := newBlobStore(t.TempDir())
	if p := b.pathFor(""); p != "" {
		t.Errorf("pathFor empty = %q, want empty", p)
	}
	if _, err := b.Get(context.Background(), ""); err != ErrBlobNotFound {
		t.Errorf("Get empty hash = %v, want ErrBlobNotFound", err)
	}
	if b.Exists("ab") {
		t.Errorf("Exists(short) = true, want false")
	}
}

// TestBlobStoreGetRejectsOversizedBlob pins the CYCLE 1
// contentCap fix. A blob larger than 16 MiB on disk (corruption,
// manual operator copy, partial write, symlink'd large file)
// would previously be loaded into memory unbounded and OOM a
// vibekit process running under the 256 MiB mem_limit. Post-fix,
// blobStore.Get does a pre-Stat size check and returns an error
// without ever opening a read pipe.
//
// We can't produce a 17 MiB blob through the normal Put path
// because hashOf computes a deterministic hash; instead we write
// a file directly at the hash's on-disk location with enough
// bytes to trip the cap. The hash doesn't need to match the
// content — Get doesn't re-verify, and the cap fires before any
// content would be returned.
func TestBlobStoreGetRejectsOversizedBlob(t *testing.T) {
	root := t.TempDir()
	b := newBlobStore(root)
	// Pick any valid 64-hex SHA — the content of the file at
	// that path won't match, but Get's size check doesn't care
	// about hash verification; it just wants the size before the
	// allocator gets burned.
	hash := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	p := b.pathFor(hash)
	if p == "" {
		t.Fatal("pathFor returned empty for a valid hash — precondition failed")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	// 17 MiB file: over the 16 MiB cap, small enough to write
	// fast on CI tmpfs. Using WriteFile with a single buffer keeps
	// the cost bounded.
	oversized := make([]byte, (16<<20)+1)
	if err := os.WriteFile(p, oversized, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := b.Get(context.Background(), hash)
	if err == nil {
		t.Fatalf("Get(oversized blob) = (%d bytes, nil), want (nil, err) from 16 MiB cap", len(got))
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("Get(oversized) err = %v, want error mentioning size cap", err)
	}
	if got != nil {
		t.Errorf("Get(oversized) returned %d bytes, want nil", len(got))
	}
}

// BenchmarkBlobStorePut measures content-addressed write throughput at
// typical agent-written file sizes (1KB, 16KB, 256KB). Each iteration
// writes a unique blob (distinct content per b.N) so the dedup
// fast-path is NOT exercised — this isolates the full syscall chain:
// SHA-256 → MkdirAll → CreateTemp → Write → Sync → Close → Rename →
// parent-dir Sync.
func BenchmarkBlobStorePut(b *testing.B) {
	for _, size := range []int{1 << 10, 16 << 10, 256 << 10} {
		b.Run(formatSize(size), func(b *testing.B) {
			bs := newBlobStore(b.TempDir())
			// Pre-generate payloads so allocation isn't in the hot loop.
			payloads := make([][]byte, b.N)
			for i := range payloads {
				p := make([]byte, size)
				// Vary first 8 bytes to produce unique hashes.
				p[0] = byte(i)
				p[1] = byte(i >> 8)
				p[2] = byte(i >> 16)
				p[3] = byte(i >> 24)
				p[4] = byte(i >> 32)
				p[5] = byte(i >> 40)
				p[6] = byte(i >> 48)
				p[7] = byte(i >> 56)
				payloads[i] = p
			}
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := range b.N {
				if _, err := bs.Put(context.Background(), payloads[i]); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkBlobStorePutDedup measures the dedup fast-path: Put called
// with content that already exists on disk. This exercises only
// hashOf + Stat (the short-circuit) and should be significantly
// cheaper than the full write path.
func BenchmarkBlobStorePutDedup(b *testing.B) {
	for _, size := range []int{1 << 10, 16 << 10, 256 << 10} {
		b.Run(formatSize(size), func(b *testing.B) {
			bs := newBlobStore(b.TempDir())
			data := make([]byte, size)
			data[0] = 0xAB // deterministic content
			// Seed the store so the blob exists.
			if _, err := bs.Put(context.Background(), data); err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(size))
			b.ResetTimer()
			for range b.N {
				if _, err := bs.Put(context.Background(), data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func formatSize(n int) string {
	switch {
	case n >= 1<<10:
		return fmt.Sprintf("%dKB", n/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// TestBlobStoreGetRejectsSymlink pins the CYCLE 1 defense-in-depth
// fix: blobStore.Get rejects non-regular files so a planted
// symlink at a valid-looking blob hash path cannot serve
// arbitrary filesystem content to any chat whose event log
// references a matching SHA.
//
// Distinct from TestRestore_RefusesToFollowSymlinkAtStagingPath
// (manager_test.go) which guards the Restore/CheckoutFile WRITE
// pipeline. This one guards the blob READ pipeline — a different
// threat model (chat B probing chat A's content via guessed
// SHAs), a different code path (blobStore.Get vs os.Rename).
//
// Red-green: drop the `if !info.Mode().IsRegular()` guard in
// blobs.go and this test fails with non-nil content + nil err.
func TestBlobStoreGetRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	b := newBlobStore(root)

	// Victim file the symlink will point at. If the guard is
	// missing, Get would happily serve its contents.
	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "secret.txt")
	victimContent := []byte("contents of another chat's private blob")
	if err := os.WriteFile(victim, victimContent, 0o600); err != nil {
		t.Fatal(err)
	}

	// Any valid 64-hex SHA — content at the path needn't match,
	// and Get doesn't verify.
	hash := "cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe"
	p := b.pathFor(hash)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, p); err != nil {
		t.Skipf("symlink creation not supported on this platform: %v", err)
	}

	got, err := b.Get(context.Background(), hash)
	if err != ErrBlobNotFound {
		t.Errorf("Get(symlink) err = %v, want ErrBlobNotFound (non-regular files must be rejected)", err)
	}
	if got != nil {
		t.Errorf("Get(symlink) returned %d bytes, want nil (symlink must not be followed)", len(got))
	}
	// Verify the symlink itself is still there — we didn't just
	// delete the link.
	if _, err := os.Lstat(p); err != nil {
		t.Errorf("symlink was unexpectedly removed: %v", err)
	}
}

// TestBlobStore_GetAllowsExactlyContentCap pins the size boundary: a
// blob of exactly contentCap bytes is at the cap, not over it, so Get
// must return it rather than reject it as oversized.
func TestBlobStore_GetAllowsExactlyContentCap(t *testing.T) {
	ctx := context.Background()
	b := newBlobStore(t.TempDir())
	data := make([]byte, contentCap) // exactly at the cap (16 MiB)
	h, err := b.Put(ctx, data)
	if err != nil {
		t.Fatalf("Put(contentCap bytes): %v", err)
	}
	got, err := b.Get(ctx, h)
	if err != nil {
		t.Fatalf("Get(blob of exactly contentCap bytes) = err %v, want nil (size==cap is not over the cap)", err)
	}
	if len(got) != contentCap {
		t.Errorf("Get(cap-sized blob) returned %d bytes, want %d", len(got), contentCap)
	}
}

// TestBlobStore_GetNoIntegrityWarnOnValidBlob pins the integrity check:
// a blob whose content hashes to its own name is self-consistent, so
// Get must not emit the integrity-failure log on a clean read.
func TestBlobStore_GetNoIntegrityWarnOnValidBlob(t *testing.T) {
	has := captureLogs(t)
	ctx := context.Background()
	b := newBlobStore(t.TempDir())
	h, err := b.Put(ctx, []byte("valid-and-self-consistent"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := b.Get(ctx, h); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if has("integrity check FAILED") {
		t.Errorf("Get(valid blob) emitted an integrity-FAILED log; the check must fire only when the content hash mismatches the name")
	}
}

// TestSyncDir_NoLogWhenOpenFails pins that syncDir returns before
// attempting a Sync when the directory can't be opened (e.g. it does
// not exist), so it must not emit the "dir sync failed" breadcrumb.
func TestSyncDir_NoLogWhenOpenFails(t *testing.T) {
	has := captureLogs(t)
	syncDir(filepath.Join(t.TempDir(), "definitely-does-not-exist"))
	if has("dir sync failed") {
		t.Errorf("syncDir(missing dir) emitted 'dir sync failed'; it must return before Sync when Open fails")
	}
}

// TestSyncDir_NoLogOnSuccess pins that a successful directory fsync is
// silent: syncDir logs only when Sync errors.
func TestSyncDir_NoLogOnSuccess(t *testing.T) {
	dir := t.TempDir()
	// Precondition: directory fsync must succeed on this filesystem,
	// otherwise syncDir would legitimately log and the assertion is moot.
	probe, err := os.Open(dir)
	if err != nil {
		t.Skipf("cannot open temp dir: %v", err)
	}
	syncErr := probe.Sync()
	_ = probe.Close()
	if syncErr != nil {
		t.Skipf("directory fsync unsupported on this fs: %v", syncErr)
	}

	has := captureLogs(t)
	syncDir(dir)
	if has("dir sync failed") {
		t.Errorf("syncDir(valid dir, fsync ok) emitted 'dir sync failed'; it must log only when Sync errors")
	}
}
