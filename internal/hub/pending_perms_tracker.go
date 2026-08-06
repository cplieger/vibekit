package hub

import (
	"sync"

	"github.com/cplieger/vibekit/internal/api"
)

// pendingPermsTracker tracks permission_needed events that haven't been
// resolved yet. Keyed by request_id. Replayed on every new SSE
// connection so permissions survive reconnects even if the ring buffer
// has wrapped. Owns its own mutex to avoid contending with Hub.mu.
type pendingPermsTracker struct {
	perms map[int64]api.ServerEvent
	mu    sync.Mutex
}

func newPendingPermsTracker() *pendingPermsTracker {
	return &pendingPermsTracker{perms: make(map[int64]api.ServerEvent)}
}

// Add records a permission_needed event.
func (t *pendingPermsTracker) Add(id int64, evt api.ServerEvent) {
	t.mu.Lock()
	t.perms[id] = evt
	t.mu.Unlock()
}

// Has reports whether a request is still unresolved. Read by the unattended
// floor before it answers for an absent user: the ordinary response path removes
// the entry, so absence means somebody already answered.
func (t *pendingPermsTracker) Has(id int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.perms[id]
	return ok
}

// Remove deletes a resolved permission event.
func (t *pendingPermsTracker) Remove(id int64) {
	t.mu.Lock()
	delete(t.perms, id)
	t.mu.Unlock()
}

// ClearForChat drops every unresolved permission_needed entry owned by chatID.
func (t *pendingPermsTracker) ClearForChat(chatID api.ChatID) {
	if chatID == "" {
		return
	}
	t.mu.Lock()
	for id, evt := range t.perms {
		if evt.ChatID == chatID {
			delete(t.perms, id)
		}
	}
	t.mu.Unlock()
}

// List returns a snapshot of all pending permission events, optionally
// filtered to a single chat.
func (t *pendingPermsTracker) List(chatFilter api.ChatID) []api.ServerEvent {
	t.mu.Lock()
	result := make([]api.ServerEvent, 0, len(t.perms))
	for _, evt := range t.perms {
		if chatFilter != "" && evt.ChatID != "" && evt.ChatID != chatFilter {
			continue
		}
		result = append(result, evt)
	}
	t.mu.Unlock()
	return result
}
