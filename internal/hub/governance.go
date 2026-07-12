package hub

// Account/workspace governance state (_kiro/governance/state).
//
// KAS pushes the org/account feature-flag policy as an A→C notification on
// every session/new + session/load and re-pushes it on a prompt when it
// changes. The chat-bridge copy flows through the normal dispatcher →
// translate.HandleGovernanceState (broadcast + SetGovernance). The UTILITY
// bridge's copy does NOT flow through that dispatcher (it has its own forward
// loop), so it is captured via the onGovernanceState callback wired in
// ensureUtility → cacheGovernanceFromUtility here. Either path populates the
// same hub-side cache, so the state is available with no chat open.
//
// The cache is served at GET /api/governance (mirrors the account-usage /
// knowledge global-read pattern): a warm snapshot returns immediately; a cold
// one lazily starts the utility bridge (whose session/new triggers the
// notification) and waits briefly for the first push. Governance is
// account-global and slow-changing, so once warmed the cache persists across
// bridge recycles.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/translate"
)

// governanceWarmTimeout bounds the cold GET /api/governance path: lazily
// starting the utility bridge (session/new + auth handshake) then waiting for
// the first governance push. Generous because a cold utility-bridge spawn
// includes the kiro-cli subprocess start; a warm cache never waits.
const governanceWarmTimeout = 12 * time.Second

// governanceCache holds the latest observed governance state. state is nil
// until the first push; warm is closed exactly once (on that first push) so a
// cold reader can block for it without spinning.
type governanceCache struct {
	state *api.GovernanceStatePayload
	warm  chan struct{}
	mu    sync.RWMutex
	once  sync.Once
}

func newGovernanceCache() *governanceCache {
	return &governanceCache{warm: make(chan struct{})}
}

// set stores a copy of the latest state and closes warm on the first call.
func (c *governanceCache) set(p api.GovernanceStatePayload) {
	c.mu.Lock()
	cp := p
	c.state = &cp
	c.mu.Unlock()
	c.once.Do(func() { close(c.warm) })
}

// get returns the cached state and whether one has been observed.
func (c *governanceCache) get() (api.GovernanceStatePayload, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.state == nil {
		return api.GovernanceStatePayload{}, false
	}
	return *c.state, true
}

// SetGovernance caches the latest governance state. Satisfies translate.Deps
// (called by translate.HandleGovernanceState on the chat-bridge path).
func (h *Hub) SetGovernance(p api.GovernanceStatePayload) {
	h.governance.set(p)
}

// cacheGovernanceFromUtility captures the _kiro/governance/state copy the
// utility bridge receives (its notifications bypass the main dispatcher). It
// caches the state and broadcasts the same account-global governance_state SSE
// the chat path does, so a client that only ever triggered a utility-bridge
// task (e.g. opened Settings) still gets a live update.
func (h *Hub) cacheGovernanceFromUtility(raw json.RawMessage) {
	payload, ok := translate.DecodeGovernanceState(raw)
	if !ok {
		return
	}
	h.governance.set(payload)
	h.Broadcast(h.lifecycle.shutdownCtx, api.NewEvent(api.EventGovernanceState, "", payload))
}

// Governance returns the cached governance state. On a warm cache it returns
// immediately; on a cold cache it lazily starts the utility bridge (whose
// session/new pushes the notification) and waits up to governanceWarmTimeout
// for the first push. A failure to warm returns a Known=false snapshot so the
// client leaves governed affordances at their permissive default rather than
// reading the zero value as "everything disabled".
func (h *Hub) Governance(ctx context.Context) api.GovernanceStatePayload {
	if p, ok := h.governance.get(); ok {
		return p
	}
	ub := h.ensureUtility()
	warmCtx, cancel := context.WithTimeout(ctx, governanceWarmTimeout)
	defer cancel()
	if err := ub.ensureStarted(warmCtx); err != nil {
		slog.Warn("governance: utility bridge start failed", "error", err)
		return api.GovernanceStatePayload{}
	}
	select {
	case <-h.governance.warm:
	case <-warmCtx.Done():
	}
	if p, ok := h.governance.get(); ok {
		return p
	}
	return api.GovernanceStatePayload{}
}

// handleGovernance: GET /api/governance → the cached account/workspace
// governance state. Read-only; the flags are org-controlled, not user-settable.
func (h *Hub) handleGovernance(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, h.Governance(r.Context()))
}

// registerGovernanceRoutes wires the governance snapshot endpoint.
func (h *Hub) registerGovernanceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/governance", h.handleGovernance)
}
