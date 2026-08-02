package hub

// Internal hub methods for chat state cleanup. These are called by
// command_deps.go to satisfy command.Dependencies.

import (
	"context"

	"github.com/cplieger/vibekit/internal/api"
)

// cleanupChatState tears down every in-memory bookkeeping entry for a
// chat. reapDurable controls whether the chat's durable per-chat state —
// its checkpoint (file-restore/undo) history AND its kiro-cli/KAS session
// state — is destroyed: true for a permanent delete (hard delete / promote
// / discard), false for the ARCHIVE path. Archive is reversible: a restored
// chat must keep its checkpoints and be able to session/load its KAS
// session, so both are reaped only at delete/purge, never on archive.
// Everything else (flush the in-flight turn via CloseBridge, kill agent
// terminals, clear pending perms + supervised trust, close+remove the
// assistant buffer) runs on both paths.
func (h *Hub) cleanupChatState(ctx context.Context, chatID api.ChatID, reapDurable bool) {
	h.flushPendingForChat(ctx, chatID, api.ClearReasonChatDeleted)
	h.perm.supervised.ClearTrust(chatID, api.ClearReasonChatDeleted)
	h.clearPendingPermsForChat(chatID)
	h.coord.CloseBridge(chatID)
	h.agentTerms.KillForChat(chatID)
	h.lifecycle.mu.Lock()
	h.bridge.assistantBufs.Delete(chatID)
	h.lifecycle.mu.Unlock()
	if reapDurable {
		h.reapChatSession(ctx, chatID)
	}
	h.lines.Clear(chatID)
}

// reapChatSession removes the chat's on-disk kiro-cli/KAS session state on
// permanent delete. cleanupChatState runs before the chat file is removed, so
// the session chain is still readable. No-op when the reaper is unwired
// (tests), the chat is already gone, or it never started a session.
//
// Reaps the whole CHAIN, not just the current session: a chat that changed
// session (failed session/load, model-switch fallback) has state under every
// id it ever held, and leaving the retired ones behind makes them orphans the
// hourly sweep has to find later.
func (h *Hub) reapChatSession(ctx context.Context, chatID api.ChatID) {
	if h.sessionReaper == nil {
		return
	}
	c, ok := h.chatStore.Get(ctx, chatID)
	if !ok {
		return
	}
	for _, id := range c.SessionChain() {
		h.sessionReaper.Reap(id)
	}
}

// clearPendingPermsForChat drops every unresolved permission_needed
// entry owned by chatID.
func (h *Hub) clearPendingPermsForChat(chatID api.ChatID) {
	h.sse.pendingPerms.ClearForChat(chatID)
}
