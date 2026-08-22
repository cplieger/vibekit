package agent

// Unit tests for hook_status.go: the cachedBoolField staleness check.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v3"
)

// A cached value whose stored size differs from the on-disk size is a
// cache MISS even when the mtime matches: get() must re-read the fresh
// value rather than return the stale cached one.
func TestHookStatusCache_SizeTermInvalidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"k":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	c := newCachedBoolField(path, "k", false)
	// Prime the cache to claim value=true with a matching identity but a
	// DIFFERENT size, isolating the size term in the cache-hit check. The size
	// term is what catches an in-place rewrite that changes the length inside one
	// clock tick, which the mtime and os.SameFile legs both call unchanged.
	c.value = true
	c.id = atomicfile.Identify(info)
	c.size = info.Size() + 1

	if got := c.get(); got != false {
		t.Errorf("cachedBoolField.get() = %v, want false (size mismatch must invalidate the cache)", got)
	}
}

// TestHookStatusCache_EqualLengthRenamePublishInvalidates is the os.SameFile
// leg's own case: kiro-cli publishes cli.json by rename (a new inode per write,
// measured against the 2.19.0 binary), so a second generation of equal length
// landing on the same coarse-clock mtime differs only by inode. The (mtime, size)
// pair called that unchanged and served the previous answer indefinitely.
func TestHookStatusCache_EqualLengthRenamePublishInvalidates(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	path := filepath.Join(dir, "cli.json")
	fixed := time.Unix(1700000000, 0)

	const v1 = `{"k":true,"x":11}`
	const v2 = `{"k":false,"x":1}` // same length, so only the inode differs

	publish := func(content string) {
		t.Helper()
		if _, err := atomicfile.WriteFile(ctx, path, []byte(content), atomicfile.WithMode(0o600)); err != nil {
			t.Fatalf("publish %s: %v", content, err)
		}
		if err := os.Chtimes(path, fixed, fixed); err != nil {
			t.Fatalf("chtimes %s: %v", content, err)
		}
	}

	publish(v1)
	c := newCachedBoolField(path, "k", false)
	if got := c.get(); !got {
		t.Fatalf("cachedBoolField.get() with %s = false, want true", v1)
	}
	info1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat v1: %v", err)
	}

	publish(v2)
	info2, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat v2: %v", err)
	}
	// Guards against a vacuous pass: an in-place write or a moved mtime would be
	// caught by the size and mtime legs too.
	if os.SameFile(info1, info2) {
		t.Fatalf("setup: the second publish reused the inode; this case needs a rename publish")
	}
	if info1.Size() != info2.Size() || !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatalf("setup: generations differ in size (%d vs %d) or mtime (%v vs %v); only the inode may differ",
			info1.Size(), info2.Size(), info1.ModTime(), info2.ModTime())
	}

	if got := c.get(); got {
		t.Errorf("cachedBoolField.get() after an equal-length rename publish of %s = true, want false", v2)
	}
}
