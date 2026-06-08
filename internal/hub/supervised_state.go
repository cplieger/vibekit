package hub

import (
	"context"
	"sync"

	"github.com/cplieger/vibekit/internal/api"
)

// supervisedState owns the per-turn trust set for Supervised mode. It has
// its own mutex so trust checks/mutations don't contend with unrelated Hub
// state. The broadcast callback is injected at construction so this type
// doesn't import the full Hub.
type supervisedState struct {
	trust     map[api.ChatID]struct{}
	broadcast func(context.Context, api.ServerEvent)
	mu        sync.Mutex
}

func newSupervisedState(broadcast func(context.Context, api.ServerEvent)) *supervisedState {
	return &supervisedState{
		trust:     make(map[api.ChatID]struct{}),
		broadcast: broadcast,
	}
}

// HasTrust returns whether the chat has per-turn trust enabled.
func (ss *supervisedState) HasTrust(chatID api.ChatID) bool {
	if chatID == "" {
		return false
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	_, ok := ss.trust[chatID]
	return ok
}

// SetTrust marks the chat as trusted for the remainder of the current
// turn. Idempotent: duplicate calls are no-ops and suppress the broadcast.
func (ss *supervisedState) SetTrust(chatID api.ChatID) {
	if chatID == "" {
		return
	}
	ss.mu.Lock()
	_, already := ss.trust[chatID]
	ss.trust[chatID] = struct{}{}
	ss.mu.Unlock()
	if already {
		return
	}
	ss.broadcast(context.Background(), api.NewEvent(api.EventPendingTrustEnabled, chatID, api.PendingTrustEnabledPayload{}))
}

// ClearTrust removes the chat from the trust set with the given reason.
// No-op if absent.
func (ss *supervisedState) ClearTrust(chatID api.ChatID, reason api.ClearReason) {
	if chatID == "" {
		return
	}
	ss.mu.Lock()
	_, had := ss.trust[chatID]
	delete(ss.trust, chatID)
	ss.mu.Unlock()
	if !had {
		return
	}
	ss.broadcast(context.Background(), api.NewEvent(api.EventPendingTrustCleared, chatID, api.PendingTrustClearedPayload{Reason: reason}))
}

// TrustedChatIDs returns a snapshot of chat IDs that currently have
// per-turn trust enabled, optionally filtered to a single chat.
func (ss *supervisedState) TrustedChatIDs(chatFilter api.ChatID) []api.ChatID {
	ss.mu.Lock()
	ids := make([]api.ChatID, 0, len(ss.trust))
	for id := range ss.trust {
		if chatFilter != "" && id != chatFilter {
			continue
		}
		ids = append(ids, id)
	}
	ss.mu.Unlock()
	return ids
}
