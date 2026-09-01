package agent

import (
	"context"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// cleanupChatState tears down every in-memory bookkeeping entry for a chat.
// reapDurable is true for a permanent delete (destroys checkpoint history and
// KAS session state); false for the archive path, which must stay reversible.
func (rt *Runtime) cleanupChatState(ctx context.Context, chatID vibekit.ChatID, reapDurable bool) {
	rt.bus.ClearPendingPermsForChat(chatID)
	// waiting_on_user survives ClearAtTurnEnd past turn end; the chat going away
	// must clear it too, or a reconnect replays a status for a chat that's gone.
	rt.bus.chatStatus.Clear(chatID)
	rt.coord.CloseBridge(chatID)
	rt.agentTerms.KillForChat(chatID)
	rt.coord.turns.forget(chatID)
	if reapDurable {
		rt.reapChatSession(ctx, chatID)
	}
	rt.lines.Clear(chatID)
}

// reapChatSession removes the chat's on-disk KAS session state on permanent
// delete, reaping the whole session CHAIN rather than just the current id —
// a chat that changed session (failed load, model-switch fallback) leaves
// state under every id it held.
func (rt *Runtime) reapChatSession(ctx context.Context, chatID vibekit.ChatID) {
	c, ok := rt.chatStore.Get(ctx, chatID)
	if !ok {
		return
	}
	rt.reapSessions(c.SessionChain())
}

// reapSessions removes each session's on-disk KAS state from a captured
// chain, for callers whose chat record is already gone.
func (rt *Runtime) reapSessions(chain []string) {
	if rt.sessionReaper == nil {
		return
	}
	for _, id := range chain {
		rt.sessionReaper.Reap(id)
	}
}
