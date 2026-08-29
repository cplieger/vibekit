package agent

// Internal runtime methods for chat state cleanup. These are called by
// command_deps.go to satisfy the command package's role interfaces.

import (
	"context"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// cleanupChatState tears down every in-memory bookkeeping entry for a
// chat. reapDurable controls whether the chat's durable per-chat state —
// its checkpoint (file-restore/undo) history AND its kiro-cli/KAS session
// state — is destroyed: true for a permanent delete (hard delete / promote
// / discard), false for the ARCHIVE path. Archive is reversible: a restored
// chat must keep its checkpoints and be able to session/load its KAS
// session, so both are reaped only at delete/purge, never on archive.
// Everything else (flush the in-flight turn via CloseBridge, kill agent
// terminals, clear pending perms, close+remove the assistant buffer) runs on both
// paths. There is no staging queue to flush and no per-turn trust to clear —
// both went with internal/pending.
func (rt *Runtime) cleanupChatState(ctx context.Context, chatID vibekit.ChatID, reapDurable bool) {
	rt.bus.ClearPendingPermsForChat(chatID)
	// The chat is going away, so no declared status can still be true of it —
	// including the waiting_on_user that ClearAtTurnEnd deliberately retains past
	// a turn's end. Without this, closing a chat that was waiting on an answer
	// left its amber status in the cache for the next connect to replay.
	rt.bus.chatStatus.Clear(chatID)
	rt.coord.CloseBridge(chatID)
	rt.agentTerms.KillForChat(chatID)
	// The turn records hold the buffers, so forgetting the chat's lifecycle drops
	// its in-flight content with it. There is no second store to clear.
	rt.coord.turns.forget(chatID)
	if reapDurable {
		rt.reapChatSession(ctx, chatID)
	}
	rt.lines.Clear(chatID)
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
func (rt *Runtime) reapChatSession(ctx context.Context, chatID vibekit.ChatID) {
	if rt.sessionReaper == nil {
		return
	}
	c, ok := rt.chatStore.Get(ctx, chatID)
	if !ok {
		return
	}
	for _, id := range c.SessionChain() {
		rt.sessionReaper.Reap(id)
	}
}
