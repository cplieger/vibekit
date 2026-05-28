package hub

import (
	"maps"
	"sync"
	"time"

	"vibekit/internal/api"
	"vibekit/internal/metrics"

	"golang.org/x/sync/singleflight"
)

// bridgeManager owns the per-chat bridge map and provides thread-safe
// access to bridge lifecycle operations. Hub composes this type,
// reducing Hub's direct field count and clarifying the ownership
// boundary: bridgeManager owns the map; Hub owns dispatch.
type bridgeManager struct {
	bridges  map[api.ChatID]*sharedBridge
	factory  api.ACPBridgeFactory
	mu       sync.Mutex
	spawnSF  singleflight.Group
	inflight *sync.WaitGroup
}

func newBridgeManager(factory api.ACPBridgeFactory, inflight *sync.WaitGroup) *bridgeManager {
	return &bridgeManager{
		bridges:  make(map[api.ChatID]*sharedBridge),
		factory:  factory,
		inflight: inflight,
	}
}

// get returns the bridge for chatID, or nil if none exists.
func (bm *bridgeManager) get(chatID api.ChatID) *sharedBridge {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	return bm.bridges[chatID]
}

// getOrInsert returns an existing bridge for chatID if present.
// If not, it creates a new sharedBridge via the factory, inserts it
// into the map, and returns (newBridge, false). With singleflight in
// GetOrCreateBridge, concurrent callers coalesce so the new bridge
// no longer needs to be returned locked.
func (bm *bridgeManager) getOrInsert(chatID api.ChatID) (sb *sharedBridge, existed bool) {
	bm.mu.Lock()
	if existing, ok := bm.bridges[chatID]; ok {
		bm.mu.Unlock()
		return existing, true
	}
	sb = &sharedBridge{bridge: bm.factory(), state: bridgeStarting}
	bm.bridges[chatID] = sb
	bm.mu.Unlock()
	metrics.BridgeSpawns.Inc()
	return sb, false
}

// remove deletes chatID from the map and returns the removed bridge
// (nil if not present). Does NOT call Stop.
func (bm *bridgeManager) remove(chatID api.ChatID) *sharedBridge {
	bm.mu.Lock()
	sb := bm.bridges[chatID]
	if sb != nil {
		delete(bm.bridges, chatID)
	}
	bm.mu.Unlock()
	return sb
}

// removeIfSame removes chatID only if the current entry matches sb.
func (bm *bridgeManager) removeIfSame(chatID api.ChatID, sb *sharedBridge) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if cur, ok := bm.bridges[chatID]; ok && cur == sb {
		delete(bm.bridges, chatID)
		return true
	}
	return false
}

// removeIfBridge removes chatID only if the current entry's bridge
// matches the given bridge instance.
func (bm *bridgeManager) removeIfBridge(chatID api.ChatID, bridge api.ACPBridge) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if sb, ok := bm.bridges[chatID]; ok && sb.bridge == bridge {
		delete(bm.bridges, chatID)
		return true
	}
	return false
}

// close removes the bridge for chatID and stops it. Idempotent.
func (bm *bridgeManager) close(chatID api.ChatID) {
	sb := bm.remove(chatID)
	if sb != nil {
		sb.bridge.Stop()
	}
}

// culledBridge pairs a removed bridge with its chat id so callers of
// closeAndStop can log eviction metadata (e.g. lastActiveAt) outside
// the manager's lock.
type culledBridge struct {
	sb     *sharedBridge
	chatID api.ChatID
}

// closeAndStop atomically removes the given chat IDs from the map
// (under bm.mu) and fires sb.bridge.Stop() for each in a goroutine.
// Returns the removed entries so callers can log eviction metadata
// outside the lock. Ids not present in the map are silently skipped.
// Used by Hub's idle-bridge culling path so Hub does not need to
// reach into bm.bridges or hold bm.mu directly.
func (bm *bridgeManager) closeAndStop(ids []api.ChatID) []culledBridge {
	if len(ids) == 0 {
		return nil
	}
	bm.mu.Lock()
	culled := make([]culledBridge, 0, len(ids))
	for _, id := range ids {
		if sb, ok := bm.bridges[id]; ok {
			delete(bm.bridges, id)
			culled = append(culled, culledBridge{chatID: id, sb: sb})
		}
	}
	bm.mu.Unlock()
	// Stop runs outside the lock so a slow Stop doesn't block other
	// bridgeMgr operations. Tracked via inflight WaitGroup so
	// Hub.Shutdown waits for all cull-triggered stops.
	for _, c := range culled {
		c := c
		if bm.inflight != nil {
			bm.inflight.Add(1)
			go func() {
				defer bm.inflight.Done()
				c.sb.bridge.Stop()
			}()
		} else {
			go c.sb.bridge.Stop()
		}
	}
	return culled
}

// selectIdleBridges returns the ids whose sharedBridge.lastActiveAt
// is non-zero and strictly before now-idleTimeout. Pure function; no
// hub state, no I/O, extracted so the cutoff boundary can be tested
// without driving a real ticker.
func selectIdleBridges(now time.Time, idleTimeout time.Duration, bridges map[api.ChatID]*sharedBridge) []api.ChatID {
	cutoff := now.Add(-idleTimeout)
	var out []api.ChatID
	for id, sb := range bridges {
		if !sb.lastActiveAt.IsZero() && sb.lastActiveAt.Before(cutoff) {
			out = append(out, id)
		}
	}
	return out
}

// selectIdle returns chat IDs whose bridges have been idle longer than
// the given timeout. Delegates to the pure selectIdleBridges function
// so the cutoff boundary can be exercised in isolation (see
// hub_cull_test.go); this method only adds locking around the map.
func (bm *bridgeManager) selectIdle(timeout time.Duration) []api.ChatID {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	return selectIdleBridges(time.Now(), timeout, bm.bridges)
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
func (bm *bridgeManager) all() map[api.ChatID]*sharedBridge {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	cp := make(map[api.ChatID]*sharedBridge, len(bm.bridges))
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
