package hub

// Unit tests for bridge_manager.go: the conditional remove helpers and
// closeAndStop. The concurrent/race coverage lives in
// bridge_manager_race_test.go.

import (
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// newTestBridgeManager builds a bridgeManager whose factory returns a
// fresh fakeBridge each call, tracking Stop goroutines on wg.
func newTestBridgeManager(wg *sync.WaitGroup) *bridgeManager {
	return newBridgeManager(func() api.ACPBridge { return newFakeBridge() }, wg)
}

// removeIfSame removes the entry only when the stored bridge IS the
// given one: a mismatch is a no-op, a match removes and reports true.
func TestBridgeManager_RemoveIfSame(t *testing.T) {
	var wg sync.WaitGroup
	bm := newTestBridgeManager(&wg)
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
	var wg sync.WaitGroup
	bm := newTestBridgeManager(&wg)
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

// closeAndStop removes the listed ids and returns the culled entries;
// a nil input returns a nil slice (not an empty non-nil one).
func TestBridgeManager_CloseAndStop(t *testing.T) {
	var wg sync.WaitGroup
	bm := newTestBridgeManager(&wg)
	bm.getOrInsert("c1")

	culled := bm.closeAndStop([]api.ChatID{"c1"})
	wg.Wait() // let the Stop goroutines finish before the test ends.
	if len(culled) != 1 {
		t.Fatalf("closeAndStop([c1]) returned %d entries, want 1", len(culled))
	}
	if culled[0].chatID != "c1" {
		t.Errorf("culled[0].chatID = %q, want %q", culled[0].chatID, "c1")
	}
	if bm.get("c1") != nil {
		t.Errorf("closeAndStop([c1]) did not remove c1 from the map")
	}

	// Empty input returns nil, never a non-nil empty slice.
	if got := bm.closeAndStop(nil); got != nil {
		t.Errorf("closeAndStop(nil) = %v (len %d), want nil", got, len(got))
	}
}
