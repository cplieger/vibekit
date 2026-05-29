package hub

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"vibekit/internal/api"
)

func TestSelectIdleBridges(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	idle := 30 * time.Minute
	bridges := map[api.ChatID]*sharedBridge{
		"fresh":     {lastActiveAt: now.Add(-5 * time.Minute)},
		"at_cutoff": {lastActiveAt: now.Add(-idle)}, // strict-before: kept
		"past":      {lastActiveAt: now.Add(-idle - time.Second)},
		"deep":      {lastActiveAt: now.Add(-24 * time.Hour)},
		"never":     {}, // zero lastActiveAt: skipped
	}
	got := selectIdleBridges(now, idle, bridges)
	slices.Sort(got)
	want := []api.ChatID{"deep", "past"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("selectIdleBridges = %v, want %v", got, want)
	}
}

func TestSelectIdleBridges_emptyMap(t *testing.T) {
	got := selectIdleBridges(time.Now(), 30*time.Minute, map[api.ChatID]*sharedBridge{})
	if len(got) != 0 {
		t.Errorf("selectIdleBridges(empty) = %v, want []", got)
	}
}

func TestSelectIdleBridges_allZeroTimesSkipped(t *testing.T) {
	now := time.Now()
	got := selectIdleBridges(now, time.Minute, map[api.ChatID]*sharedBridge{
		"a": {},
		"b": {},
	})
	if len(got) != 0 {
		t.Errorf("zero-time bridges should never cull, got %v", got)
	}
}



func BenchmarkBridgeManagerGetOrInsert(b *testing.B) {
	factory := func() api.ACPBridge { return newNoopBridge() }
	bm := newBridgeManager(factory, nil)

	// Pre-populate with some bridges so "exists" path is exercised.
	for i := range 100 {
		sb, existed := bm.getOrInsert(api.ChatID(fmt.Sprintf("chat-%d", i)))
		if !existed {
			sb.state = bridgeIdle
		}
	}

	b.Run("exists", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				bm.getOrInsert(api.ChatID(fmt.Sprintf("chat-%d", i%100)))
				i++
			}
		})
	})

	b.Run("create", func(b *testing.B) {
		// Use a separate manager so creates don't accumulate unboundedly.
		bm2 := newBridgeManager(factory, nil)
		var mu sync.Mutex
		var counter int

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				mu.Lock()
				counter++
				id := fmt.Sprintf("new-%d", counter)
				mu.Unlock()

				sb, existed := bm2.getOrInsert(api.ChatID(id))
				if !existed {
					sb.state = bridgeIdle
				}
			}
		})
	})
}

func BenchmarkBridgeManagerSelectIdle(b *testing.B) {
	now := time.Now()
	idle := 30 * time.Minute

	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("bridges_%d", n), func(b *testing.B) {
			bridges := make(map[api.ChatID]*sharedBridge, n)
			for i := range n {
				// Half idle, half active.
				var t time.Time
				if i%2 == 0 {
					t = now.Add(-idle - time.Duration(i)*time.Second)
				} else {
					t = now.Add(-5 * time.Minute)
				}
				bridges[api.ChatID(fmt.Sprintf("chat-%d", i))] = &sharedBridge{lastActiveAt: t}
			}

			b.ResetTimer()
			for b.Loop() {
				selectIdleBridges(now, idle, bridges)
			}
		})
	}
}

func BenchmarkBridgeManagerDrain(b *testing.B) {
	factory := func() api.ACPBridge { return newNoopBridge() }

	for _, n := range []int{10, 50, 100} {
		b.Run(fmt.Sprintf("bridges_%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				bm := newBridgeManager(factory, nil)
				for i := range n {
					sb, existed := bm.getOrInsert(api.ChatID(fmt.Sprintf("chat-%d", i)))
					if !existed {
						sb.state = bridgeIdle
						sb.mu.Unlock()
					}
				}
				_ = bm.drain()
			}
		})
	}
}
