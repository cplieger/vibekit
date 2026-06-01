package hub

// Internal hub methods for chat state cleanup. These are called by
// command_deps.go to satisfy command.Dependencies.

import (
	"context"

	"vibekit/internal/api"
)

// cleanupChatState tears down every in-memory bookkeeping entry for a
// chat that is about to be deleted.
func (h *Hub) cleanupChatState(ctx context.Context, chatID api.ChatID) {
	h.flushPendingForChat(ctx, chatID, api.ClearReasonChatDeleted)
	h.perm.supervised.ClearTrust(chatID, api.ClearReasonChatDeleted)
	h.clearPendingPermsForChat(chatID)
	h.coord.CloseBridge(chatID)
	h.agentTerms.KillForChat(chatID)
	h.lifecycle.mu.Lock()
	buf := h.bridge.assistantBufs.Get(chatID)
	h.translator.ClearCrewCache(chatID)
	h.bridge.assistantBufs.Delete(chatID)
	h.lifecycle.mu.Unlock()
	h.closeAndRemovePartial(ctx, chatID, buf)
	if h.checkpoints != nil {
		h.checkpoints.Cleanup(ctx, chatID)
	}
	h.lines.Clear(chatID)
}

// clearPendingPermsForChat drops every unresolved permission_needed
// entry owned by chatID.
func (h *Hub) clearPendingPermsForChat(chatID api.ChatID) {
	h.sse.pendingPerms.ClearForChat(chatID)
}
