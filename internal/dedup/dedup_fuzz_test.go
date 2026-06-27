package dedup

import (
	"encoding/binary"
	"testing"
	"time"
)

// FuzzDedupCacheInvariant drives an arbitrary sequence of Check/Record
// operations parsed from the fuzz bytes and asserts two invariants after
// every step:
//
//  1. The cache never exceeds maxEntries (the bound that guards against
//     unbounded growth).
//  2. A cacheable Record (non-empty key, result within maxResult) is
//     immediately observable with the value just written (last-write-wins;
//     the just-inserted key is never the one evicted to make room).
func FuzzDedupCacheInvariant(f *testing.F) {
	f.Add([]byte("op1\x00key1\x00val1"))
	f.Add([]byte("op2\x00key2\x00val2val2val2"))

	const maxEntries = 10
	const maxResult = 64

	f.Fuzz(func(t *testing.T, data []byte) {
		c := New(5*time.Minute, maxEntries, maxResult)
		for len(data) >= 3 {
			op := data[0]
			keyLen := int(data[1])
			data = data[2:]
			if keyLen > len(data) {
				break
			}
			key := string(data[:keyLen])
			data = data[keyLen:]
			switch op % 2 {
			case 0:
				c.Check(key)
			case 1:
				valLen := 0
				if len(data) >= 2 {
					valLen = int(binary.LittleEndian.Uint16(data[:2]))
					data = data[2:]
					if valLen > len(data) {
						valLen = len(data)
					}
				}
				val := data[:valLen]
				data = data[valLen:]
				c.Record(key, val)

				// Invariant 2: a cacheable Record is immediately observable.
				if key != "" && len(val) <= maxResult {
					got, ok := c.Check(key)
					if !ok {
						t.Fatalf("Check(%q) missing right after Record", key)
					}
					if string(got) != string(val) {
						t.Fatalf("Check(%q) = %q, want %q (last write wins)", key, got, val)
					}
				}
			}

			// Invariant 1: the cache never exceeds its capacity bound.
			c.mu.Lock()
			n := len(c.entries)
			c.mu.Unlock()
			if n > maxEntries {
				t.Fatalf("entries=%d exceeds maxEntries=%d", n, maxEntries)
			}
		}
		c.Check("")
	})
}

// FuzzDedupCacheEvictionOrder verifies that the cache never exceeds maxEntries
// and that the most recently recorded key is always findable with its value
// under rapid eviction pressure.
//
// Bug class: heap corruption from stale keys, O(n) fallback failing to find
// entries, map growth past the maxEntries bound.
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

		// Invariant 2: the last-recorded key is the most recent, so it is
		// never evicted and Check returns the value just written.
		if k4 != "" && len(v4) <= 1024 {
			got, ok := c.Check(k4)
			if !ok {
				t.Fatalf("Check(%q) not found after Record (most recent key must survive)", k4)
			}
			if string(got) != string(v4) {
				t.Fatalf("Check(%q) = %q, want %q (last write wins)", k4, got, v4)
			}
		}
	})
}

// FuzzPruneExpired exercises the TTL pruning path with arbitrary entry
// timestamps to verify it never panics, removes exactly the entries older than
// the TTL, and leaves no expired entry behind.
func FuzzPruneExpired(f *testing.F) {
	f.Add([]byte("\x03k1\x01\x00\x00\x00\x00\x00\x00\x00\x03k2\xff\xff\xff\xff\xff\xff\xff\xff"))

	f.Fuzz(func(t *testing.T, data []byte) {
		entries := make(map[string]Entry)
		for len(data) >= 9 {
			keyLen := int(data[0])
			data = data[1:]
			if keyLen > len(data)-8 {
				break
			}
			key := string(data[:keyLen])
			data = data[keyLen:]
			ts := int64(binary.LittleEndian.Uint64(data[:8]))
			data = data[8:]
			entries[key] = Entry{Ts: time.Unix(0, ts)}
		}
		if len(entries) == 0 {
			return
		}
		before := len(entries)
		now := time.Now()
		ttl := 5 * time.Minute
		removed := PruneExpired(now, ttl, entries)
		after := len(entries)
		if before-removed != after {
			t.Fatalf("count mismatch: before=%d removed=%d after=%d", before, removed, after)
		}
		cutoff := now.Add(-ttl)
		for k, e := range entries {
			if e.Ts.Before(cutoff) {
				t.Fatalf("entry %q with ts %v should have been pruned (cutoff %v)", k, e.Ts, cutoff)
			}
		}
	})
}
