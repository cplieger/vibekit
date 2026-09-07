package agent

import (
	"log/slog"
	"maps"
	"sync"

	"github.com/cplieger/vibekit/internal/vibekit"
	"golang.org/x/sync/singleflight"
)

// bridgeManager owns the per-chat bridge map and serializes access to bridge
// lifecycle operations. Runtime composes it and owns dispatch.
type bridgeManager struct {
	spawnSF singleflight.Group
	bridges map[vibekit.ChatID]*sharedBridge
	factory ACPBridgeFactory
	mu      sync.Mutex
}

func newBridgeManager(factory ACPBridgeFactory) *bridgeManager {
	return &bridgeManager{
		bridges: make(map[vibekit.ChatID]*sharedBridge),
		factory: factory,
	}
}

// get returns the bridge for chatID, or nil if none exists.
func (bm *bridgeManager) get(chatID vibekit.ChatID) *sharedBridge {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	return bm.bridges[chatID]
}

// orInsert returns the existing bridge for chatID, or creates one via the factory,
// inserts it, and returns (newBridge, false). OpenBridge's singleflight is what lets
// the new bridge be returned unlocked.
func (bm *bridgeManager) orInsert(chatID vibekit.ChatID) (sb *sharedBridge, existed bool) {
	bm.mu.Lock()
	if existing, ok := bm.bridges[chatID]; ok {
		bm.mu.Unlock()
		return existing, true
	}
	sb = &sharedBridge{bridge: bm.factory(), state: bridgeStarting}
	bm.bridges[chatID] = sb
	bm.mu.Unlock()
	slog.Info("bridge spawned", "chat_id", chatID)
	return sb, false
}

// insert registers an ALREADY-STARTED bridge under chatID: a run bridge's map key is
// its workflow id, which only `workflow/new`'s reply knows. Replacing an entry would
// orphan a live process, so insert refuses and answers the RESIDENT one, letting a
// loser reach the winner's bridge rather than holding one no map holds. See rehost.
func (bm *bridgeManager) insert(
	chatID vibekit.ChatID, sb *sharedBridge,
) (resident *sharedBridge, inserted bool) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if held, exists := bm.bridges[chatID]; exists {
		return held, false
	}
	bm.bridges[chatID] = sb
	slog.Info("bridge registered", "chat_id", chatID)
	return sb, true
}

// remove deletes chatID from the map and returns the removed bridge, or nil. Does NOT
// call Stop.
func (bm *bridgeManager) remove(chatID vibekit.ChatID) *sharedBridge {
	bm.mu.Lock()
	sb := bm.bridges[chatID]
	if sb != nil {
		delete(bm.bridges, chatID)
	}
	bm.mu.Unlock()
	return sb
}

// removeIfSame removes chatID only if the current entry matches sb.
func (bm *bridgeManager) removeIfSame(chatID vibekit.ChatID, sb *sharedBridge) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if cur, ok := bm.bridges[chatID]; ok && cur == sb {
		delete(bm.bridges, chatID)
		return true
	}
	return false
}

// removeIfBridge removes chatID only if the current entry's bridge is the SAME
// INSTANCE as bridge. The parameter is an identity, not a capability: it stays the
// full ACPBridge so a caller cannot pass something the map could never have held.
func (bm *bridgeManager) removeIfBridge(chatID vibekit.ChatID, bridge ACPBridge) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if sb, ok := bm.bridges[chatID]; ok && sb.bridge == bridge {
		delete(bm.bridges, chatID)
		return true
	}
	return false
}

// close removes the bridge for chatID and stops it. Idempotent.
func (bm *bridgeManager) close(chatID vibekit.ChatID) {
	sb := bm.remove(chatID)
	if sb != nil {
		sb.bridge.Stop()
	}
}

// count returns the number of active bridges.
func (bm *bridgeManager) count() int {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	return len(bm.bridges)
}

// all returns a snapshot of every bridge, for callers that must inspect them all.
func (bm *bridgeManager) all() map[vibekit.ChatID]*sharedBridge {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	cp := make(map[vibekit.ChatID]*sharedBridge, len(bm.bridges))
	maps.Copy(cp, bm.bridges)
	return cp
}

// drain removes every bridge from the map and returns them for teardown.
func (bm *bridgeManager) drain() []*sharedBridge {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	out := make([]*sharedBridge, 0, len(bm.bridges))
	for id, sb := range bm.bridges {
		out = append(out, sb)
		delete(bm.bridges, id)
	}
	return out
}
