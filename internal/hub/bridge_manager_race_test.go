package hub

import (
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// TestBridgeManager_ConcurrentGetOrInsertClose exercises the race
// between getOrInsert (creates a new bridge for a chatID) and close
// (removes + stops a bridge for the same chatID). Under -race this
// catches any missed synchronization on the bridges map.
func TestBridgeManager_ConcurrentGetOrInsertClose(t *testing.T) {
	factory := func() api.ACPBridge { return newNoopBridge() }
	bm := newBridgeManager(factory)

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

// TestBridgeManager_CloseConcurrentDrain verifies that per-chat close
// and drain don't interfere when run concurrently (e.g. a tab close
// racing with Shutdown).
func TestBridgeManager_CloseConcurrentDrain(t *testing.T) {
	factory := func() api.ACPBridge { return newNoopBridge() }
	bm := newBridgeManager(factory)

	// Seed bridges.
	for i := range 20 {
		chatID := api.ChatID("drain-" + string(rune('A'+i)))
		bm.getOrInsert(chatID)
	}

	var wg sync.WaitGroup

	wg.Go(func() {
		for _, id := range []api.ChatID{"drain-A", "drain-B", "drain-C"} {
			bm.close(id)
		}
	})

	wg.Go(func() {
		_ = bm.drain()
	})

	wg.Wait()
}
