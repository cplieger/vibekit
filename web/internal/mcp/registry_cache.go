package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// registryCache is a TTL-bounded, singleflight-protected byte cache
// for upstream registry responses. It encapsulates the map, TTL,
// max-entries cap, and request coalescing that were previously inline
// on RegistryProxy.
type registryCache struct {
	sf      singleflight.Group
	entries map[string]registryCacheEntry
	ttl     time.Duration
	maxSize int
	mu      sync.Mutex
}

func newRegistryCache(maxSize int) *registryCache {
	return &registryCache{
		entries: make(map[string]registryCacheEntry),
		ttl:     registryCacheTTL,
		maxSize: maxSize,
	}
}

// GetOrFetch returns cached data for key if fresh, otherwise calls
// fetchFn (coalesced via singleflight) and caches the result.
// Followers bail early on ctx cancellation.
func (c *registryCache) GetOrFetch(ctx context.Context, key string, fetchFn func() ([]byte, error)) (body []byte, cached bool, err error) {
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && time.Since(entry.insertedAt) < c.ttl {
		body = entry.body
		c.mu.Unlock()
		return body, true, nil
	}
	c.mu.Unlock()

	// DoChan coalesces concurrent misses without a wrapper goroutine.
	ch := c.sf.DoChan(key, func() (any, error) {
		b, doErr := fetchFn()
		if doErr != nil {
			return nil, doErr
		}
		c.mu.Lock()
		if len(c.entries) >= c.maxSize {
			c.evictLocked()
		}
		c.entries[key] = registryCacheEntry{
			insertedAt: time.Now(),
			body:       b,
		}
		c.mu.Unlock()
		return b, nil
	})

	select {
	case res := <-ch:
		if res.Err != nil {
			return nil, false, res.Err
		}
		b, ok := res.Val.([]byte)
		if !ok {
			return nil, false, fmt.Errorf("registry_cache: fetcher returned %T, want []byte", res.Val)
		}
		return b, res.Shared, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

// evictLocked removes expired entries and, if still at capacity,
// evicts the oldest entry. Caller must hold c.mu.
func (c *registryCache) evictLocked() {
	evicted := 0
	for k, entry := range c.entries {
		if time.Since(entry.insertedAt) >= c.ttl {
			delete(c.entries, k)
			evicted++
		}
	}
	if len(c.entries) >= c.maxSize {
		var oldestKey string
		var oldestTime time.Time
		first := true
		for k, entry := range c.entries {
			if first || entry.insertedAt.Before(oldestTime) {
				oldestKey = k
				oldestTime = entry.insertedAt
				first = false
			}
		}
		delete(c.entries, oldestKey)
		evicted++
	}
	if evicted > 0 {
		slog.Debug("mcp: registry cache evicted",
			"count", evicted, "remaining", len(c.entries))
	}
}
