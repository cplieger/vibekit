package hub

// Unit tests for bridge_manager.go: the conditional remove helpers.
// The concurrent/race coverage lives in bridge_manager_race_test.go.

import (
	"fmt"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// newTestBridgeManager builds a bridgeManager whose factory returns a
// fresh fakeBridge each call.
func newTestBridgeManager() *bridgeManager {
	return newBridgeManager(func() ACPBridge { return newFakeBridge() })
}

// removeIfSame removes the entry only when the stored bridge IS the
// given one: a mismatch is a no-op, a match removes and reports true.
func TestBridgeManager_RemoveIfSame(t *testing.T) {
	bm := newTestBridgeManager()
	sb1, _ := bm.getOrInsert("c1")
	sb2, _ := bm.getOrInsert("c2")

	// Mismatch must NOT remove and must return false.
	if removed := bm.removeIfSame("c1", sb2); removed {
		t.Errorf("removeIfSame(c1, other) = true, want false")
	}
	if bm.get("c1") == nil {
		t.Errorf("removeIfSame(c1, other) wrongly removed c1")
	}

	// Match must remove and return true.
	if removed := bm.removeIfSame("c1", sb1); !removed {
		t.Errorf("removeIfSame(c1, c1) = false, want true")
	}
	if bm.get("c1") != nil {
		t.Errorf("removeIfSame(c1, c1) did not remove c1")
	}
}

// removeIfBridge removes the entry only when the stored bridge instance
// matches the given one.
func TestBridgeManager_RemoveIfBridge(t *testing.T) {
	bm := newTestBridgeManager()
	sb, _ := bm.getOrInsert("c1")
	stored := sb.bridge
	other := newFakeBridge()

	// Mismatched bridge instance must NOT remove.
	if removed := bm.removeIfBridge("c1", other); removed {
		t.Errorf("removeIfBridge(c1, other) = true, want false")
	}
	if bm.get("c1") == nil {
		t.Errorf("removeIfBridge(c1, other) wrongly removed c1")
	}

	// Matching bridge instance must remove.
	if removed := bm.removeIfBridge("c1", stored); !removed {
		t.Errorf("removeIfBridge(c1, stored) = false, want true")
	}
	if bm.get("c1") != nil {
		t.Errorf("removeIfBridge(c1, stored) did not remove c1")
	}
}

func BenchmarkBridgeManagerGetOrInsert(b *testing.B) {
	factory := func() ACPBridge { return newNoopBridge() }
	bm := newBridgeManager(factory)

	// Pre-populate with some bridges so "exists" path is exercised.
	for i := range 100 {
		sb, existed := bm.getOrInsert(vibekit.ChatID(fmt.Sprintf("chat-%d", i)))
		if !existed {
			sb.state = bridgeIdle
		}
	}

	b.Run("exists", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				bm.getOrInsert(vibekit.ChatID(fmt.Sprintf("chat-%d", i%100)))
				i++
			}
		})
	})

	b.Run("create", func(b *testing.B) {
		// Use a separate manager so creates don't accumulate unboundedly.
		bm2 := newBridgeManager(factory)
		var mu sync.Mutex
		var counter int

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				mu.Lock()
				counter++
				id := fmt.Sprintf("new-%d", counter)
				mu.Unlock()

				sb, existed := bm2.getOrInsert(vibekit.ChatID(id))
				if !existed {
					sb.state = bridgeIdle
				}
			}
		})
	})
}
