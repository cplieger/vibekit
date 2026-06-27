package dedup

import (
	"container/heap"
	"testing"
	"time"
)

// Pop must return the smallest-timestamp item and shrink the heap by exactly
// one element on each call.
func TestEvictionHeap_PopReturnsOldestAndShrinks(t *testing.T) {
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

// A result whose length is exactly maxResult must be cached: the bound is a
// strict '>', so equality is kept.
func TestRecord_CachesResultAtExactlyMaxResult(t *testing.T) {
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

// A result one byte over maxResult must not be cached.
func TestRecord_SkipsResultOverMaxResult(t *testing.T) {
	const maxResult = 4
	c := New(time.Minute, 10, maxResult)
	c.Record("id", []byte("abcde")) // len 5 > maxResult

	if _, ok := c.Check("id"); ok {
		t.Errorf("Check(id) ok = true, want false (len>maxResult must be skipped)")
	}
}

// Eviction fires only when adding a NEW key would meet or exceed the cap, and
// it removes exactly the oldest entry, so a mid-age entry survives.
func TestRecord_EvictsOldestAtCapacity(t *testing.T) {
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

// With entries at capacity but an empty eviction heap, the O(n) fallback must
// still evict the oldest entry.
func TestRecord_EvictsViaFallbackWhenHeapEmpty(t *testing.T) {
	c := New(time.Minute, 1, 1024) // maxEntries=1
	// Seed an entry directly so the heap stays empty, forcing the fallback
	// scan on the next Record.
	c.entries["old"] = Entry{Ts: time.Now(), Result: []byte("old-v")}

	c.Record("new", []byte("new-v")) // new key at capacity, heap empty

	if _, ok := c.Check("new"); !ok {
		t.Errorf("Check(new) ok = false, want true (new entry cached)")
	}
	if _, ok := c.Check("old"); ok {
		t.Errorf("Check(old) ok = true, want false (evicted via O(n) fallback)")
	}
}

// PruneExpired keeps entries newer than now-ttl and removes the older ones.
func TestPruneExpired_KeepsFreshRemovesOld(t *testing.T) {
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
		t.Errorf("fresh entry pruned; want kept (cutoff is now-ttl)")
	}
	if _, ok := entries["old"]; ok {
		t.Errorf("old entry survived; want pruned")
	}
}

// Check returns a stored value on a hit, reports a miss for an absent key, and
// treats the empty request id as a miss.
func TestCheck_HitMissEmpty(t *testing.T) {
	c := New(time.Minute, 10, 1024)
	c.Record("present", []byte("value"))

	if got, ok := c.Check("present"); !ok || string(got) != "value" {
		t.Errorf("Check(present) = (%q, %v), want (%q, true)", got, ok, "value")
	}
	if _, ok := c.Check("absent"); ok {
		t.Errorf("Check(absent) ok = true, want false")
	}
	if _, ok := c.Check(""); ok {
		t.Errorf(`Check("") ok = true, want false (empty id is always a miss)`)
	}
}
