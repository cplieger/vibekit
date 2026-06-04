package hub

import (
	"sync"
	"testing"

	"vibekit/internal/api"
)

// TestBridgeManager_ConcurrentGetOrInsertClose exercises the race
// between getOrInsert (creates a new bridge for a chatID) and close
// (removes + stops a bridge for the same chatID). Under -race this
// catches any missed synchronization on the bridges map.
func TestBridgeManager_ConcurrentGetOrInsertClose(t *testing.T) {
	factory := func() api.ACPBridge { return newNoopBridge() }
	bm := newBridgeManager(factory, &sync.WaitGroup{})

	const N = 100
	var wg sync.WaitGroup

	// Inserters.
	wg.Go(func() {
		for i := range N {
			chatID := api.ChatID("chat-" + string(rune('A'+i%5)))
			bm.getOrInsert(chatID)
		}
	})

	// Closers.
	wg.Go(func() {
		for i := range N {
			chatID := api.ChatID("chat-" + string(rune('A'+i%5)))
			bm.close(chatID)
		}
	})

	// Readers.
	wg.Go(func() {
		for i := range N {
			chatID := api.ChatID("chat-" + string(rune('A'+i%5)))
			_ = bm.get(chatID)
		}
	})

	// Count.
	wg.Go(func() {
		for range N {
			_ = bm.count()
		}
	})

	wg.Wait()
}

// TestBridgeManager_CloseAndStopConcurrentDrain verifies that
// closeAndStop and drain don't interfere when run concurrently
// (e.g. idle culling racing with Shutdown).
func TestBridgeManager_CloseAndStopConcurrentDrain(t *testing.T) {
	factory := func() api.ACPBridge { return newNoopBridge() }
	var inflight sync.WaitGroup
	bm := newBridgeManager(factory, &inflight)

	// Seed bridges.
	for i := range 20 {
		chatID := api.ChatID("drain-" + string(rune('A'+i)))
		bm.getOrInsert(chatID)
	}

	var wg sync.WaitGroup

	wg.Go(func() {
		ids := []api.ChatID{"drain-A", "drain-B", "drain-C"}
		bm.closeAndStop(ids)
	})

	wg.Go(func() {
		_ = bm.drain()
	})

	wg.Wait()
	inflight.Wait()
}
