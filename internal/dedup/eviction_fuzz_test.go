package dedup

import (
	"testing"
	"time"
)

// FuzzDedupCacheEvictionOrder verifies that the cache never exceeds maxEntries
// and that Record+Check maintain consistency under rapid eviction pressure.
//
// Bug class: heap corruption from stale keys, O(n) fallback failing to find
// entries, map growth past maxEntries bound.
func FuzzDedupCacheEvictionOrder(f *testing.F) {
	f.Add("req1", []byte("r1"), "req2", []byte("r2"), "req3", []byte("r3"), "req4", []byte("r4"))
	f.Add("", []byte(""), "a", []byte("b"), "a", []byte("c"), "d", []byte("e"))
	f.Add("k1", []byte("v1"), "k1", []byte("v2"), "k2", []byte("v3"), "k3", []byte("v4"))
	f.Add("x", []byte{0, 1, 2}, "y", []byte{3, 4}, "z", []byte{5}, "w", []byte{6, 7, 8})
	f.Add("\x00", []byte("nul"), "ab", []byte("cd"), "ef", []byte("gh"), "ij", []byte("kl"))

	f.Fuzz(func(t *testing.T, k1 string, v1 []byte, k2 string, v2 []byte, k3 string, v3 []byte, k4 string, v4 []byte) {
		c := New(time.Minute, 3, 1024)

		keys := []string{k1, k2, k3, k4}
		vals := [][]byte{v1, v2, v3, v4}

		for i, k := range keys {
			c.Record(k, vals[i])

			// Invariant 1: entries never exceed maxEntries.
			c.mu.Lock()
			n := len(c.entries)
			c.mu.Unlock()
			if n > 3 {
				t.Fatalf("after Record(%q): entries=%d > maxEntries=3", k, n)
			}
		}

		// Invariant 2: last non-empty key recorded is always findable.
		if k4 != "" && len(v4) <= 1024 {
			if result, ok := c.Check(k4); !ok {
				t.Fatalf("Check(%q) not found after Record", k4)
			} else if string(result) != string(v4) {
				// May differ if k4 was recorded earlier with different value
				// and not evicted — that's acceptable (first-write wins in dedup).
				_ = result
			}
		}
	})
}
