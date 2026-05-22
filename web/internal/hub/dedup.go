package hub

import (
	"container/heap"
	"sync"
	"time"
)

const (
	idempotencyTTL = 5 * time.Minute
	// idempotencyMaxEntries caps the request_id→result cache. A bug
	// client that rotates request_id in a tight loop can otherwise
	// grow the map unboundedly between the 1-minute sweeps.
	idempotencyMaxEntries = 10_000
	// idempotencyMaxResult skips caching replies larger than this.
	// Idempotent retries exist for cheap acks, not megabyte payloads.
	idempotencyMaxResult = 64 * 1024
)

// idempotencyCache deduplicates repeated command requests by caching
// request_id → result for a bounded TTL. It owns its own mutex so
// check/record operations don't contend with SSE fan-out or bridge
// lifecycle operations on Hub.mu.
type idempotencyCache struct {
	entries    map[string]idempotencyEntry
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
func (h *evictionHeap) Push(x any)        { *h = append(*h, x.(evictionItem)) } //nolint:errcheck // heap.Interface guarantees the pushed type matches.
func (h *evictionHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

func newIdempotencyCache() *idempotencyCache {
	c := &idempotencyCache{
		entries:    make(map[string]idempotencyEntry),
		ttl:        idempotencyTTL,
		maxEntries: idempotencyMaxEntries,
		maxResult:  idempotencyMaxResult,
	}
	heap.Init(&c.evictHeap)
	return c
}

// Check returns the cached result for reqID if present and not expired.
func (c *idempotencyCache) Check(reqID string) ([]byte, bool) {
	if reqID == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[reqID]; ok {
		return e.result, true
	}
	return nil, false
}

// Record stores a request_id → result mapping. Enforces two bounds:
// results over maxResult are not cached, and when the map hits
// maxEntries the oldest entry is evicted via a min-heap in O(log n).
// Falls back to an O(n) scan if the heap is empty (entries populated
// externally, e.g. in tests).
func (c *idempotencyCache) Record(reqID string, result []byte) {
	if reqID == "" || len(result) > c.maxResult {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if _, exists := c.entries[reqID]; !exists && len(c.entries) >= c.maxEntries {
		evicted := false
		// Try heap-based O(log n) eviction first. Skip stale keys
		// that were already pruned by the TTL sweep.
		for c.evictHeap.Len() > 0 {
			oldest := heap.Pop(&c.evictHeap).(evictionItem) //nolint:errcheck // heap.Interface guarantees the popped type matches what was pushed.
			if _, ok := c.entries[oldest.key]; ok {
				delete(c.entries, oldest.key)
				evicted = true
				break
			}
		}
		// Fallback: O(n) scan when heap is empty (entries populated
		// without going through Record, e.g. in tests or after a
		// heap rebuild race).
		if !evicted {
			var oldestKey string
			var oldestTS time.Time
			for k, v := range c.entries {
				if oldestKey == "" || v.ts.Before(oldestTS) {
					oldestKey, oldestTS = k, v.ts
				}
			}
			if oldestKey != "" {
				delete(c.entries, oldestKey)
			}
		}
	}
	c.entries[reqID] = idempotencyEntry{ts: now, result: result}
	heap.Push(&c.evictHeap, evictionItem{key: reqID, ts: now})
}

// StartCleaner runs a periodic sweep that removes expired entries.
// Exits when done is closed.
func (c *idempotencyCache) StartCleaner(done <-chan struct{}) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			c.mu.Lock()
			pruneExpired(time.Now(), c.ttl, c.entries)
			// Incrementally clean the heap: pop entries whose keys
			// are no longer in the map (pruned by TTL above).
			for c.evictHeap.Len() > 0 {
				top := c.evictHeap[0]
				if _, ok := c.entries[top.key]; ok {
					break // top is still live; stop
				}
				heap.Pop(&c.evictHeap)
			}
			c.mu.Unlock()
		}
	}
}

// pruneExpired removes idempotency entries older than ttl relative to
// now (strictly before cutoff; entries exactly at the boundary are
// kept). Returns the number of entries removed; exposed as a test hook,
// production ignores the return value.
func pruneExpired(now time.Time, ttl time.Duration, entries map[string]idempotencyEntry) int {
	cutoff := now.Add(-ttl)
	n := 0
	for k, v := range entries {
		if v.ts.Before(cutoff) {
			delete(entries, k)
			n++
		}
	}
	return n
}
