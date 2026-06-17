package dedup

// Mutant-killing tests for unit vibekit-u28 (internal/dedup).
// All new identifiers are prefixed gk_vibekit_u28_ to avoid colliding
// with sibling units that may share this package.

import (
	"container/heap"
	"testing"
	"time"
)

// --- dedup.go:55 evictionHeap.Pop `*h = old[:n-1]` (ARITHMETIC_BASE / INVERT_NEGATIVES) ---

// Pop must return the smallest-timestamp item and shrink the heap by
// exactly one. Any mutation of the `n-1` slice bound either panics
// (out-of-range) or leaves the heap one element too long, which the
// strict Len() assertions catch.
func Test_gk_vibekit_u28_EvictionHeapPopOrderAndShrink(t *testing.T) {
	h := evictionHeap{}
	heap.Init(&h)
	base := time.Now()
	heap.Push(&h, evictionItem{key: "c", ts: base.Add(2 * time.Millisecond)})
	heap.Push(&h, evictionItem{key: "a", ts: base})
	heap.Push(&h, evictionItem{key: "b", ts: base.Add(1 * time.Millisecond)})
	if h.Len() != 3 {
		t.Fatalf("Len after 3 pushes = %d, want 3", h.Len())
	}

	if got := heap.Pop(&h).(evictionItem); got.key != "a" {
		t.Errorf("first Pop key = %q, want %q (oldest)", got.key, "a")
	}
	if h.Len() != 2 {
		t.Fatalf("Len after first Pop = %d, want 2", h.Len())
	}
	if got := heap.Pop(&h).(evictionItem); got.key != "b" {
		t.Errorf("second Pop key = %q, want %q", got.key, "b")
	}
	if h.Len() != 1 {
		t.Fatalf("Len after second Pop = %d, want 1", h.Len())
	}
	if got := heap.Pop(&h).(evictionItem); got.key != "c" {
		t.Errorf("third Pop key = %q, want %q", got.key, "c")
	}
	if h.Len() != 0 {
		t.Fatalf("Len after third Pop = %d, want 0", h.Len())
	}
}

// --- dedup.go:89 Record `len(result) > c.maxResult` (CONDITIONALS_BOUNDARY) ---

// A result of length exactly maxResult must be cached: `>` keeps it,
// the mutant `>=` skips it.
func Test_gk_vibekit_u28_RecordCachesResultExactlyMaxResult(t *testing.T) {
	const maxResult = 4
	c := New(time.Minute, 10, maxResult)
	c.Record("id", []byte("abcd")) // len 4 == maxResult

	got, ok := c.Check("id")
	if !ok {
		t.Fatalf("Check(id) ok = false, want true (len==maxResult must be cached)")
	}
	if string(got) != "abcd" {
		t.Errorf("Check(id) = %q, want %q", got, "abcd")
	}
}

// --- dedup.go:95 Record `len(c.entries) >= c.maxEntries` (CONDITIONALS_NEGATION) ---

// Eviction fires only when adding a NEW key would meet/exceed the cap.
// Original `>=`: id1,id2 fill the cap (no eviction), id3 evicts the
// oldest (id1) so id2 survives. The mutant `<` evicts while still under
// the cap, so id2 is dropped too.
func Test_gk_vibekit_u28_RecordEvictsOldestAtCapacity(t *testing.T) {
	c := New(time.Minute, 2, 1024) // maxEntries=2
	c.Record("id1", []byte("v1"))
	time.Sleep(2 * time.Millisecond)
	c.Record("id2", []byte("v2"))
	time.Sleep(2 * time.Millisecond)
	c.Record("id3", []byte("v3"))

	if _, ok := c.Check("id1"); ok {
		t.Errorf("Check(id1) ok = true, want false (oldest, evicted at capacity)")
	}
	if _, ok := c.Check("id2"); !ok {
		t.Errorf("Check(id2) ok = false, want true (must survive: only oldest is evicted)")
	}
	if _, ok := c.Check("id3"); !ok {
		t.Errorf("Check(id3) ok = false, want true (just recorded)")
	}
}

// --- dedup.go:97 Record `for c.evictHeap.Len() > 0` (CONDITIONALS_BOUNDARY / CONDITIONALS_NEGATION) ---

// With entries at capacity but an empty eviction heap, the documented
// O(n) fallback must evict the oldest entry. Original `> 0` skips the
// loop body (Len()==0) and runs the fallback. Both mutants (`>= 0` and
// `<= 0`) make `0 ... 0` true, entering the loop and calling heap.Pop
// on an empty heap, which panics — failing the test.
func Test_gk_vibekit_u28_RecordEvictsViaFallbackWhenHeapEmpty(t *testing.T) {
	c := New(time.Minute, 1, 1024) // maxEntries=1
	// Seed an entry directly so evictHeap stays empty, forcing the
	// fallback path on the next Record.
	c.entries["old"] = Entry{Ts: time.Now(), Result: []byte("old-v")}

	c.Record("new", []byte("new-v")) // new key at capacity, heap empty

	if _, ok := c.Check("new"); !ok {
		t.Errorf("Check(new) ok = false, want true (new entry cached)")
	}
	if _, ok := c.Check("old"); ok {
		t.Errorf("Check(old) ok = true, want false (evicted via O(n) fallback)")
	}
}

// --- dedup.go:149 PruneExpired `cutoff := now.Add(-ttl)` (ARITHMETIC_BASE / INVERT_NEGATIVES) ---

// cutoff must be now-ttl: a fresh entry (Ts==now) is kept, an old entry
// (Ts==now-2*ttl) is removed. Flipping the sign (now+ttl) prunes
// everything, including the fresh entry.
func Test_gk_vibekit_u28_PruneExpiredKeepsFreshRemovesOld(t *testing.T) {
	now := time.Now()
	ttl := time.Minute
	entries := map[string]Entry{
		"fresh": {Ts: now, Result: []byte("f")},
		"old":   {Ts: now.Add(-2 * time.Minute), Result: []byte("o")},
	}

	removed := PruneExpired(now, ttl, entries)

	if removed != 1 {
		t.Errorf("PruneExpired removed = %d, want 1", removed)
	}
	if _, ok := entries["fresh"]; !ok {
		t.Errorf("fresh entry pruned; want kept (cutoff must be now-ttl, not now+ttl)")
	}
	if _, ok := entries["old"]; ok {
		t.Errorf("old entry survived; want pruned")
	}
}
