package hub

// command.Dependencies implementation methods for Hub.
// These expose Hub internals to the command package handlers.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/command"
)

// Compile-time assertion: Hub satisfies command.Dependencies.
var _ command.Dependencies = (*Hub)(nil)

// GetBridge returns the active bridge for a chat, or nil.
func (h *Hub) GetBridge(chatID api.ChatID) command.Bridge {
	sb := h.coord.GetBridge(chatID)
	if sb == nil {
		return nil
	}
	return sb
}

// GetOrCreateBridge ensures a bridge exists for the chat.
func (h *Hub) GetOrCreateBridge(ctx context.Context, chatID api.ChatID, model string) (command.Bridge, error) {
	sb, err := h.coord.GetOrCreateBridge(ctx, chatID, model)
	if err != nil {
		return nil, err
	}
	return sb, nil
}

// CloseBridge tears down the bridge for a chat.
func (h *Hub) CloseBridge(chatID api.ChatID) {
	h.coord.CloseBridge(chatID)
}

// ClearPendingPermsForChat drops unresolved permission_needed entries.
func (h *Hub) ClearPendingPermsForChat(chatID api.ChatID) {
	h.clearPendingPermsForChat(chatID)
}

// TakePendingPerm claims an unanswered decision so exactly one surface can
// answer it, reporting false when something else got there first. The winning
// take also announces itself (decision_settled), which retires the card every
// OTHER surface is still showing.
//
// Every answer path goes through here, and the order is the contract: TAKE
// first, then send the answer to kiro-cli. A handler that responded first and
// retired the entry afterwards left a window in which a second tab read the same
// request as pending and answered it too, and the agent server drops the second
// answer without telling anyone — so which choice won was decided there, not
// here. A caller that loses the race must not send its answer at all.
//
// Taking and announcing are ONE function on purpose: they are the same fact
// ("this request is now settled") told to two audiences, and splitting them
// would let a new answer path claim a request while leaving every other surface
// showing a live card for it.
func (h *Hub) TakePendingPerm(requestID int64, settledBy api.SettledBy) bool {
	evt, ok := h.sse.pendingPerms.TakeIfPresent(requestID)
	if !ok {
		return false
	}
	kind, known := api.DecisionKindForEvent(evt.Type)
	if !known {
		// Only the three *_needed events are ever tracked, so this is a tracker
		// misuse rather than something off the wire. The claim still stands (the
		// caller may answer); what is skipped is an announcement whose kind no
		// client could act on.
		slog.Error("hub: tracked decision has no kind, cannot announce it",
			"type", evt.Type, "request_id", requestID)
		return true
	}
	h.emit(api.NewEvent(api.EventDecisionSettled, evt.ChatID, api.DecisionSettledPayload{
		RequestID: requestID,
		Kind:      kind,
		SettledBy: settledBy,
	}))
	return true
}

// ConfigDir returns the configuration directory.
func (h *Hub) ConfigDir() string {
	return h.lifecycle.configDir
}

// ShutdownCtx returns the context cancelled on shutdown.
func (h *Hub) ShutdownCtx() context.Context {
	return h.lifecycle.shutdownCtx
}

// InflightAdd increments the inflight counter.
func (h *Hub) InflightAdd(delta int) {
	h.lifecycle.inflight.Add(delta)
}

// InflightDone decrements the inflight counter.
func (h *Hub) InflightDone() {
	h.lifecycle.inflight.Done()
}

// CleanupChatState tears down all in-memory state for a chat that is being
// permanently deleted (the delete / promote / discard paths), reaping the
// chat's checkpoints too. The archive path uses OnChatArchiving, which runs
// the same teardown but preserves checkpoints (archive is reversible).
func (h *Hub) CleanupChatState(ctx context.Context, chatID api.ChatID) {
	h.cleanupChatState(ctx, chatID, true)
}

// MCPWaitForReady blocks until MCP servers are ready or timeout.
func (h *Hub) MCPWaitForReady(ctx context.Context, timeout time.Duration) bool {
	return h.mcpRegistry.WaitForReady(ctx, timeout)
}

// ResolveInsideWorkDir validates a path is inside the workspace.
func (h *Hub) ResolveInsideWorkDir(rel string) (string, error) {
	return h.resolveInsideWorkDir(rel)
}

// PrimeIfNeeded primes the bridge with history if needed.
func (h *Hub) PrimeIfNeeded(ctx context.Context, chatID api.ChatID, b command.Bridge) {
	sb, ok := b.(*sharedBridge)
	if !ok {
		slog.Error("hub: PrimeIfNeeded called with non-sharedBridge Bridge",
			"type", fmt.Sprintf("%T", b))
		return
	}
	h.coord.PrimeIfNeeded(ctx, chatID, sb)
}

// IsEmptyTurn checks if a prompt response is an empty turn.
func (h *Hub) IsEmptyTurn(resp *api.RPCResponse, chatID api.ChatID) bool {
	return h.isEmptyTurn(resp, chatID)
}

// EmitTurnEndedWithStats broadcasts turn_ended with usage stats, and closes
// the chat's terminal-attribution turn: terminals created after this belong
// to the NEXT turn.
func (h *Hub) EmitTurnEndedWithStats(ctx context.Context, chatID api.ChatID, resp *api.RPCResponse, creditsDelta, elapsedMs float64) {
	h.coord.EmitTurnEndedWithStats(ctx, chatID, resp, creditsDelta, elapsedMs)
	h.agentTerms.AdvanceTurn(chatID)
}

// AbandonInFlightTurn finalizes a turn that failed before it could end, and
// closes its terminal-attribution turn on the way out. It mirrors
// EmitTurnEndedWithStats' AdvanceTurn call for the same reason: the terminals
// this turn created must not be attributed to the next one.
func (h *Hub) AbandonInFlightTurn(ctx context.Context, chatID api.ChatID) {
	h.coord.AbandonInFlightTurn(ctx, chatID)
	h.agentTerms.AdvanceTurn(chatID)
}

// KillTurnTerminals kills the terminals the current turn created. The
// interrupt half of the tab-close contract: cancel stops the model, and this
// stops the processes the turn already spawned.
func (h *Hub) KillTurnTerminals(chatID api.ChatID) {
	h.agentTerms.KillForTurn(chatID)
}
