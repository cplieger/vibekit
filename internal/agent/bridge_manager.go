package agent

import (
	"log/slog"
	"maps"
	"sync"

	"github.com/cplieger/vibekit/internal/vibekit"
	"golang.org/x/sync/singleflight"
)

// bridgeManager owns the per-chat bridge map and provides thread-safe
// access to bridge lifecycle operations. Runtime composes this type,
// reducing Runtime's direct field count and clarifying the ownership
// boundary: bridgeManager owns the map; Runtime owns dispatch.
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

// getOrInsert returns an existing bridge for chatID if present.
// If not, it creates a new sharedBridge via the factory, inserts it
// into the map, and returns (newBridge, false). With singleflight in
// GetOrCreateBridge, concurrent callers coalesce so the new bridge
// no longer needs to be returned locked.
func (bm *bridgeManager) getOrInsert(chatID vibekit.ChatID) (sb *sharedBridge, existed bool) {
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

// insert registers an ALREADY-STARTED bridge under chatID. The run-host path:
// a run bridge's map key is its workflow id, which only `workflow/new`'s reply
// knows, so the bridge is started first and registered once the key exists —
// the inverse of getOrInsert's create-then-start. Replacing an existing entry
// would orphan a live process, so insert refuses instead (the caller launched
// the same run twice, which the single-run guard should have stopped).
func (bm *bridgeManager) insert(chatID vibekit.ChatID, sb *sharedBridge) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if _, exists := bm.bridges[chatID]; exists {
		return false
	}
	bm.bridges[chatID] = sb
	slog.Info("bridge registered", "chat_id", chatID)
	return true
}

// remove deletes chatID from the map and returns the removed bridge
// (nil if not present). Does NOT call Stop.
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
// INSTANCE as bridge.
//
// The parameter is an identity, not a capability: 0 of the 14 methods are
// called on it. It stays the full ACPBridge anyway, because the comparison is
// only meaningful against what the map holds, and a narrower type here would
// let a caller pass something that could never have been registered.
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

// promptingChatIDs returns the chats whose bridge currently holds the
// prompt slot (state == bridgePrompting) — i.e. exactly the chats a
// new prompt would 409 on, which is the authoritative "busy" set the
// connect-time turn_state replay synthesizes from. Locking shape:
// bm.mu for the map, per-bridge mu for state.
func (bm *bridgeManager) promptingChatIDs() []vibekit.ChatID {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	var out []vibekit.ChatID
	for id, sb := range bm.bridges {
		sb.mu.Lock()
		prompting := sb.state == bridgePrompting
		sb.mu.Unlock()
		if prompting {
			out = append(out, id)
		}
	}
	return out
}

// count returns the number of active bridges.
func (bm *bridgeManager) count() int {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	return len(bm.bridges)
}

// all returns a snapshot of all bridges. Used for iteration patterns
// that need to inspect every bridge (e.g. Shutdown teardown, model
// snapshot collection).
func (bm *bridgeManager) all() map[vibekit.ChatID]*sharedBridge {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	cp := make(map[vibekit.ChatID]*sharedBridge, len(bm.bridges))
	maps.Copy(cp, bm.bridges)
	return cp
}

// drain removes all bridges from the map and returns them for
// teardown. Used during Shutdown.
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
