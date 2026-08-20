package agent

// command role implementation methods for Runtime.
// These expose Runtime internals to the command package handlers.

import (
	"context"

	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// Runtime satisfies each of the command package's role interfaces; the assertions
// are the ones the Roles literal in command_dispatch.go already forces, so they
// are not repeated here. There is no assertion for an envelope seam any more:
// the dispatcher takes no collaborator at all now that idempotency is the
// header middleware's.

// Bridge returns the active bridge for a chat, or nil.
func (rt *Runtime) Bridge(chatID vibekit.ChatID) command.Bridge {
	sb := rt.coord.Bridge(chatID)
	if sb == nil {
		return nil
	}
	return sb
}

// OpenBridge ensures a bridge exists for the chat.
func (rt *Runtime) OpenBridge(ctx context.Context, chatID vibekit.ChatID, model string) (command.Bridge, error) {
	sb, err := rt.coord.OpenBridge(ctx, chatID, model)
	if err != nil {
		return nil, err
	}
	return sb, nil
}

// CloseBridge tears down the bridge for a chat.
func (rt *Runtime) CloseBridge(chatID vibekit.ChatID) {
	rt.coord.CloseBridge(chatID)
}

// DeleteChatState tears down all in-memory state for a chat being permanently
// deleted, cancelling its runs first and reaping its durable KAS session too.
func (rt *Runtime) DeleteChatState(ctx context.Context, chatID vibekit.ChatID) {
	rt.runs.CancelForChat(ctx, chatID)
	rt.cleanupChatState(ctx, chatID, true)
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
func (rt *Runtime) CloseChatState(ctx context.Context, chatID vibekit.ChatID) {
	rt.runs.CancelForChat(ctx, chatID)
	rt.cleanupChatState(ctx, chatID, false)
}

// PrimeIfNeeded primes the chat's session with history if it needs it.
func (rt *Runtime) PrimeIfNeeded(ctx context.Context, chatID vibekit.ChatID) {
	rt.coord.PrimeIfNeeded(ctx, chatID)
}

// PrimeFromChat notes that a chat's first session should be primed with another
// chat's transcript — the tangent's fork-refused fallback.
func (rt *Runtime) PrimeFromChat(chatID, sourceChatID vibekit.ChatID) {
	rt.coord.PrimeFromChat(chatID, sourceChatID)
}

// IsEmptyTurn checks if a prompt response is an empty turn.
func (rt *Runtime) IsEmptyTurn(resp *vibekit.RPCResponse, chatID vibekit.ChatID) bool {
	return rt.isEmptyTurn(resp, chatID)
}

// EmitTurnEndedWithStats broadcasts turn_ended with usage stats, and closes
// the chat's terminal-attribution turn: terminals created after this belong
// to the NEXT turn.
func (rt *Runtime) EmitTurnEndedWithStats(ctx context.Context, chatID vibekit.ChatID, resp *vibekit.RPCResponse, stats command.TurnStats) {
	rt.coord.EmitTurnEndedWithStats(ctx, chatID, resp, stats)
	rt.agentTerms.AdvanceTurn(chatID)
}

// AbandonInFlightTurn finalizes a turn that failed before it could end, and
// closes its terminal-attribution turn on the way out. It mirrors
// EmitTurnEndedWithStats' AdvanceTurn call for the same reason: the terminals
// this turn created must not be attributed to the next one.
func (rt *Runtime) AbandonInFlightTurn(ctx context.Context, chatID vibekit.ChatID) {
	rt.coord.AbandonInFlightTurn(ctx, chatID)
	rt.agentTerms.AdvanceTurn(chatID)
}
