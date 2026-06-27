// Package dedup provides a bounded, TTL-based idempotency cache for
// deduplicating repeated requests by request ID.
package dedup

import (
	"container/heap"
	"sync"
	"time"
)

// DefaultTTL is the default time-to-live for cache entries.
const DefaultTTL = 5 * time.Minute

// DefaultMaxEntries caps the request_id→result cache.
const DefaultMaxEntries = 10_000

// DefaultMaxResult skips caching replies larger than this.
const DefaultMaxResult = 64 * 1024

// Entry is a single cached result with its insertion timestamp.
type Entry struct {
	Ts     time.Time
	Result []byte
}

// Cache deduplicates repeated command requests by caching
// request_id → result for a bounded TTL. It owns its own mutex so
// check/record operations don't contend with other subsystems.
type Cache struct {
	entries    map[string]Entry
	evictHeap  evictionHeap
	ttl        time.Duration
	maxEntries int
	maxResult  int
	mu         sync.Mutex
}

// evictionItem tracks a cache entry's timestamp for heap-based eviction.
type evictionItem struct {
	ts  time.Time
	key string
}

// evictionHeap is a min-heap ordered by timestamp (oldest first).
type evictionHeap []evictionItem

func (h evictionHeap) Len() int           { return len(h) }
func (h evictionHeap) Less(i, j int) bool { return h[i].ts.Before(h[j].ts) }
func (h evictionHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *evictionHeap) Push(x any)        { *h = append(*h, x.(evictionItem)) } //nolint:errcheck // heap interface contract
func (h *evictionHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// New creates a Cache with the given TTL, max entries, and max result size.
func New(ttl time.Duration, maxEntries, maxResult int) *Cache {
	c := &Cache{
		entries:    make(map[string]Entry),
		ttl:        ttl,
		maxEntries: maxEntries,
		maxResult:  maxResult,
	}
	heap.Init(&c.evictHeap)
	return c
}

// Check returns the cached result for reqID if present and not expired.
func (c *Cache) Check(reqID string) ([]byte, bool) {
	if reqID == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[reqID]; ok {
		return e.Result, true
	}
	return nil, false
}

// Record stores a request_id → result mapping. Enforces two bounds:
// results over maxResult are not cached, and when the map hits
// maxEntries the oldest entry is evicted via a min-heap in O(log n).
// Falls back to an O(n) scan if the heap is empty.
func (c *Cache) Record(reqID string, result []byte) {
	if reqID == "" || len(result) > c.maxResult {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if _, exists := c.entries[reqID]; !exists && len(c.entries) >= c.maxEntries {
		c.evictOldestLocked()
	}
	c.entries[reqID] = Entry{Ts: now, Result: result}
	heap.Push(&c.evictHeap, evictionItem{key: reqID, ts: now})
}

// evictOldestLocked removes one entry to make room. The caller must hold c.mu.
// It pops the min-heap in O(log n), skipping stale keys (already deleted), and
// deletes the first live key it finds. If the heap drains without a hit it
// falls back to an O(n) scan.
func (c *Cache) evictOldestLocked() {
	for c.evictHeap.Len() > 0 {
		oldest := heap.Pop(&c.evictHeap).(evictionItem) //nolint:errcheck // heap returns evictionItem
		if _, ok := c.entries[oldest.key]; ok {
			delete(c.entries, oldest.key)
			return
		}
	}
	c.evictOldestByScanLocked()
}

// evictOldestByScanLocked deletes the entry with the oldest timestamp via a
// linear scan. The caller must hold c.mu. Used as the fallback when the
// eviction heap is empty (e.g. entries seeded directly, or the heap drained
// of stale keys).
func (c *Cache) evictOldestByScanLocked() {
	var oldestKey string
	var oldestTS time.Time
	for k, v := range c.entries {
		if oldestKey == "" || v.Ts.Before(oldestTS) {
			oldestKey, oldestTS = k, v.Ts
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

// StartCleaner runs a periodic sweep that removes expired entries.
// Exits when done is closed.
func (c *Cache) StartCleaner(done <-chan struct{}) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			c.mu.Lock()
			PruneExpired(time.Now(), c.ttl, c.entries)
			for c.evictHeap.Len() > 0 {
				top := c.evictHeap[0]
				if _, ok := c.entries[top.key]; ok {
					break
				}
				heap.Pop(&c.evictHeap)
			}
			c.mu.Unlock()
		}
	}
}

// PruneExpired removes entries older than ttl relative to now.
// Returns the number of entries removed.
func PruneExpired(now time.Time, ttl time.Duration, entries map[string]Entry) int {
	cutoff := now.Add(-ttl)
	n := 0
	for k, v := range entries {
		if v.Ts.Before(cutoff) {
			delete(entries, k)
			n++
		}
	}
	return n
}
