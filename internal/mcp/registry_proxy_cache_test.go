package mcp

// registryCache eviction coverage + cache micro-benchmarks for
// RegistryProxy. Split out of registry_proxy_http_test.go (the HTTP
// request/response path) to keep each file focused and under the
// ~800-line ceiling.

import (
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestEvictLocked_dropsExpiredFirst(t *testing.T) {
	p := NewRegistryProxy()

	now := time.Now()
	// 32 expired + 32 fresh = 64; at-cap path runs.
	for i := range 32 {
		p.cache.entries[fmt.Sprintf("exp%d", i)] = registryCacheEntry{
			insertedAt: now.Add(-time.Hour), body: []byte("x"),
		}
	}
	for i := range 32 {
		p.cache.entries[fmt.Sprintf("fresh%d", i)] = registryCacheEntry{
			insertedAt: now, body: []byte("x"),
		}
	}

	buf := captureSlog(t)
	p.cache.mu.Lock()
	p.cache.evictLocked()
	p.cache.mu.Unlock()

	// All 32 expired dropped; 32 fresh remain (under-cap, so no
	// further eviction).
	if got := len(p.cache.entries); got != 32 {
		t.Errorf("after evict len(cache) = %d, want 32 (all expired dropped)", got)
	}
	for k, entry := range p.cache.entries {
		if time.Since(entry.insertedAt) >= registryCacheTTL {
			t.Errorf("expired entry %q survived: insertedAt=%v", k, entry.insertedAt)
		}
	}
	// Eviction happened, so the count is logged. Pins the evicted++ /
	// evicted>0 logic whose only observable effect is this log line.
	if !strings.Contains(buf.String(), "registry cache evicted") {
		t.Errorf("expired eviction occurred but no evict log emitted; log=%q", buf.String())
	}
}

func TestEvictLocked_whenAllFresh_dropsOldest(t *testing.T) {
	p := NewRegistryProxy()

	now := time.Now()
	// 64 fresh entries, staggered in time (k00 is oldest).
	for i := range maxCacheEntries {
		p.cache.entries[fmt.Sprintf("k%02d", i)] = registryCacheEntry{
			insertedAt: now.Add(time.Duration(i) * time.Millisecond),
			body:       []byte("x"),
		}
	}

	buf := captureSlog(t)
	p.cache.mu.Lock()
	p.cache.evictLocked()
	p.cache.mu.Unlock()

	if got := len(p.cache.entries); got != maxCacheEntries-1 {
		t.Errorf("after evict len(cache) = %d, want %d", got, maxCacheEntries-1)
	}
	if _, ok := p.cache.entries["k00"]; ok {
		t.Error("oldest entry k00 should have been evicted")
	}
	// At-cap eviction happened, so the count is logged.
	if !strings.Contains(buf.String(), "registry cache evicted") {
		t.Errorf("at-cap eviction occurred but no evict log emitted; log=%q", buf.String())
	}
}

func TestEvictLocked_underCap_isNoop(t *testing.T) {
	p := NewRegistryProxy()
	now := time.Now()
	for i := range 10 {
		p.cache.entries[fmt.Sprintf("k%d", i)] = registryCacheEntry{
			insertedAt: now,
		}
	}

	buf := captureSlog(t)
	p.cache.mu.Lock()
	p.cache.evictLocked()
	p.cache.mu.Unlock()

	if got := len(p.cache.entries); got != 10 {
		t.Errorf("under-cap evict dropped entries: len=%d, want 10", got)
	}
	// Nothing was evicted (evicted == 0), so no log fires. Pins the
	// evicted>0 guard against a >=0 mutation that would log spuriously.
	if strings.Contains(buf.String(), "registry cache evicted") {
		t.Errorf("no eviction happened but evict log emitted; log=%q", buf.String())
	}
}

func BenchmarkRegistryCacheGetOrFetch(b *testing.B) {
	payload := []byte(`{"servers":[{"name":"test","url":"http://example.com"}]}`)

	b.Run("hit_same_key", func(b *testing.B) {
		cache := newRegistryCache(maxCacheEntries)
		// Pre-seed.
		cache.mu.Lock()
		cache.entries["bench-key"] = registryCacheEntry{insertedAt: time.Now(), body: payload}
		cache.mu.Unlock()

		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_, _, _ = cache.GetOrFetch(b.Context(), "bench-key", func() ([]byte, error) {
					return payload, nil
				})
			}
		})
	})

	b.Run("miss_same_key_singleflight", func(b *testing.B) {
		cache := newRegistryCache(maxCacheEntries)
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_, _, _ = cache.GetOrFetch(b.Context(), "sf-key", func() ([]byte, error) {
					return payload, nil
				})
			}
		})
	})

	b.Run("miss_different_keys", func(b *testing.B) {
		cache := newRegistryCache(10000)
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				key := fmt.Sprintf("key-%d", i)
				i++
				_, _, _ = cache.GetOrFetch(b.Context(), key, func() ([]byte, error) {
					return payload, nil
				})
			}
		})
	})
}

// The TTL edge belongs to the expired side, and the reader and the evictor have
// to agree on that. They ask the question in opposite directions — the reader
// asks whether an entry is still fresh, the evictor whether it has expired — so
// an entry sitting exactly on the edge is where the two can disagree, and a
// disagreement means an entry the reader keeps serving that the evictor has
// already decided is gone.
//
// A synthetic clock rather than a real one: the boundary is one instant, and no
// real-clock test can land on it.
func TestRegistryCache_theTTLEdgeIsExpired(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := NewRegistryProxy()
		fetches := 0
		fetch := func() ([]byte, error) {
			fetches++
			return []byte("upstream"), nil
		}

		p.cache.entries["k"] = registryCacheEntry{insertedAt: time.Now(), body: []byte("cached")}

		// A nanosecond short of the TTL is still fresh, so nothing is fetched.
		synctest.Sleep(registryCacheTTL - time.Nanosecond)
		body, cached, err := p.cache.GetOrFetch(t.Context(), "k", fetch)
		if err != nil {
			t.Fatalf("GetOrFetch a nanosecond inside the TTL: %v", err)
		}
		if !cached || string(body) != "cached" {
			t.Errorf("GetOrFetch a nanosecond inside the TTL = (%q, cached=%v), want the cached body",
				body, cached)
		}
		if fetches != 0 {
			t.Errorf("a fresh entry caused %d upstream fetches, want 0", fetches)
		}

		// On the edge it is stale, so the reader goes upstream.
		synctest.Sleep(time.Nanosecond)
		body, _, err = p.cache.GetOrFetch(t.Context(), "k", fetch)
		if err != nil {
			t.Fatalf("GetOrFetch exactly on the TTL: %v", err)
		}
		if string(body) != "upstream" {
			t.Errorf("GetOrFetch exactly on the TTL = %q, want a fresh fetch", body)
		}
		if fetches != 1 {
			t.Errorf("an entry exactly on the TTL caused %d upstream fetches, want 1", fetches)
		}

		// And the evictor agrees: the entry it just replaced is collectable the
		// instant it reaches the same age.
		p.cache.entries["old"] = registryCacheEntry{insertedAt: time.Now(), body: []byte("x")}
		synctest.Sleep(registryCacheTTL)
		p.cache.mu.Lock()
		p.cache.evictLocked()
		_, survived := p.cache.entries["old"]
		p.cache.mu.Unlock()
		if survived {
			t.Errorf("an entry exactly %v old survived eviction; the evictor and the reader "+
				"disagree about the TTL edge", registryCacheTTL)
		}
	})
}
