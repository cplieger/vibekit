package hub

// Unit tests for hook_status.go: the cachedBoolField staleness check.

import (
	"os"
	"path/filepath"
	"testing"
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
	// Prime the cache to claim value=true with a matching mtime but a
	// DIFFERENT size, isolating the size term in the cache-hit check.
	c.value = true
	c.valid = true
	c.mtime = info.ModTime()
	c.size = info.Size() + 1

	if got := c.get(); got != false {
		t.Errorf("cachedBoolField.get() = %v, want false (size mismatch must invalidate the cache)", got)
	}
}
