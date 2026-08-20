package hub

// command role implementation methods for Hub.
// These expose Hub internals to the command package handlers.

import (
	"context"

	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// Hub satisfies each of the command package's role interfaces; the assertions
// are the ones the Roles literal in command_dispatch.go already forces, so they
// are not repeated here. There is no assertion for an envelope seam any more:
// the dispatcher takes no collaborator at all now that idempotency is the
// header middleware's.

// CancelChatRuns cancels every non-terminal run parented on any session in the
// chat's chain. It is the one place Hub still stands between a role and its
// owner, and deliberately so: command.ChatAccess is the CHAT lifecycle, and
// closing or deleting a chat has to reach both the chat record and the run
// surface. The alternative was a fourth role on two handlers to say "and also
// cancel this chat's runs", which spreads a single lifecycle decision across two
// declarations. CleanupChatState and CloseChatState on the same interface already
// span six fields for the same reason.
func (h *Hub) CancelChatRuns(ctx context.Context, chatID vibekit.ChatID) {
	h.runs.CancelChatRuns(ctx, chatID)
}

// ChatStore returns the hub's chat store as the command handlers use it (5 of
// its 9 methods). Beside ChatRecords() in translate_deps.go, which is the same
// store at 3 methods for the translator: Go matches an interface method by
// exact signature, so two consumers with two narrow contracts need two
// accessors rather than one that returns whichever is wider.
func (h *Hub) ChatStore() command.ChatStore { return h.chatStore }

// GetBridge returns the active bridge for a chat, or nil.
func (h *Hub) GetBridge(chatID vibekit.ChatID) command.Bridge {
	sb := h.coord.GetBridge(chatID)
	if sb == nil {
		return nil
	}
	return sb
}

// GetOrCreateBridge ensures a bridge exists for the chat.
func (h *Hub) GetOrCreateBridge(ctx context.Context, chatID vibekit.ChatID, model string) (command.Bridge, error) {
	sb, err := h.coord.GetOrCreateBridge(ctx, chatID, model)
	if err != nil {
		return nil, err
	}
	return sb, nil
}

// CloseBridge tears down the bridge for a chat.
func (h *Hub) CloseBridge(chatID vibekit.ChatID) {
	h.coord.CloseBridge(chatID)
}

// CleanupChatState tears down all in-memory state for a chat that is being
// permanently deleted (the delete path), reaping the chat's durable KAS session
// state too.
func (h *Hub) CleanupChatState(ctx context.Context, chatID vibekit.ChatID) {
	h.cleanupChatState(ctx, chatID, true)
}

// CloseChatState tears down a chat's in-memory state WITHOUT touching its
// durable KAS session.
//
// This is the close path, and the distinction is the whole point: closing a tab
// kills the process, not the history. Close used to share the delete path, so
// the × on a tab reaped the chat's whole session chain off disk — which
// contradicted its own contract ("the chat RECORD is untouched … reopening it
// session/loads everything back") and cost the user the transcript twice over:
// the reopened chat had no session to load, and the History page, which lists
// KAS's sessions, could only ever show chats that were still open.
func (h *Hub) CloseChatState(ctx context.Context, chatID vibekit.ChatID) {
	h.cleanupChatState(ctx, chatID, false)
}

// PrimeIfNeeded primes the chat's session with history if it needs it.
func (h *Hub) PrimeIfNeeded(ctx context.Context, chatID vibekit.ChatID) {
	h.coord.PrimeIfNeeded(ctx, chatID)
}

// PrimeFromChat notes that a chat's first session should be primed with another
// chat's transcript — the tangent's fork-refused fallback.
func (h *Hub) PrimeFromChat(chatID, sourceChatID vibekit.ChatID) {
	h.coord.PrimeFromChat(chatID, sourceChatID)
}

// IsEmptyTurn checks if a prompt response is an empty turn.
func (h *Hub) IsEmptyTurn(resp *vibekit.RPCResponse, chatID vibekit.ChatID) bool {
	return h.isEmptyTurn(resp, chatID)
}

// EmitTurnEndedWithStats broadcasts turn_ended with usage stats, and closes
// the chat's terminal-attribution turn: terminals created after this belong
// to the NEXT turn.
func (h *Hub) EmitTurnEndedWithStats(ctx context.Context, chatID vibekit.ChatID, resp *vibekit.RPCResponse, stats command.TurnStats) {
	h.coord.EmitTurnEndedWithStats(ctx, chatID, resp, stats)
	h.agentTerms.AdvanceTurn(chatID)
}

// AbandonInFlightTurn finalizes a turn that failed before it could end, and
// closes its terminal-attribution turn on the way out. It mirrors
// EmitTurnEndedWithStats' AdvanceTurn call for the same reason: the terminals
// this turn created must not be attributed to the next one.
func (h *Hub) AbandonInFlightTurn(ctx context.Context, chatID vibekit.ChatID) {
	h.coord.AbandonInFlightTurn(ctx, chatID)
	h.agentTerms.AdvanceTurn(chatID)
}
