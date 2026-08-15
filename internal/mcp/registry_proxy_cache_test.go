package mcp

// registryCache eviction coverage + cache micro-benchmarks for
// RegistryProxy. Split out of registry_proxy_http_test.go (the HTTP
// request/response path) to keep each file focused and under the
// ~800-line ceiling.

import (
	"fmt"
	"strings"
	"testing"
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
