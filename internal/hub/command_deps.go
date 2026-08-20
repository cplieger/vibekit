package hub

// command role implementation methods for Hub.
// These expose Hub internals to the command package handlers.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// Hub satisfies each of the command package's role interfaces; the assertions
// are the ones the Roles literal in command_dispatch.go already forces, so they
// are not repeated here.
//
// The one method whose satisfaction is NOT forced by that literal is the
// envelope seam, because command.New takes it as a parameter rather than a
// field.
var _ command.DedupGate = (*Hub)(nil)

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

// ClearPendingPermsForChat drops unresolved permission_needed entries.
func (h *Hub) ClearPendingPermsForChat(chatID vibekit.ChatID) {
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
func (h *Hub) TakePendingPerm(requestID int64, settledBy vibekit.SettledBy) bool {
	evt, ok := h.sse.pendingPerms.TakeIfPresent(requestID)
	if !ok {
		return false
	}
	kind, known := vibekit.DecisionKindForEvent(evt.Type)
	if !known {
		// Only the three *_needed events are ever tracked, so this is a tracker
		// misuse rather than something off the wire. The claim still stands (the
		// caller may answer); what is skipped is an announcement whose kind no
		// client could act on.
		slog.Error("hub: tracked decision has no kind, cannot announce it",
			"type", evt.Type, "request_id", requestID)
		return true
	}
	h.emit(vibekit.NewEvent(vibekit.EventDecisionSettled, evt.ChatID, vibekit.DecisionSettledPayload{
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

// TurnContext returns the context a turn runs under, plus the teardown its
// handler must defer.
//
// It replaced an exported ShutdownCtx() accessor, and the replacement is the
// point: a command handler never wanted the hub's raw lifetime context, it
// wanted a turn context derived from it, and handing out the lifetime made every
// consumer responsible for deriving one correctly. The hub is what knows its own
// lifetime, so the derivation lives here.
//
// The turn is DETACHED from reqCtx's cancellation while keeping its values: the
// prompt POST's context dies when the handler returns, and a turn that died with
// it failed before it could finalize and persist the assistant buffer, even
// though kiro-cli kept running the turn to completion. Cancellation is
// re-attached to the hub's shutdown context via AfterFunc so the turn still dies
// on shutdown; the returned cancel tears it down on handler return and
// unregisters that AfterFunc so it cannot leak. Explicit user cancellation is
// unaffected — it goes through session/cancel (Notify), not this context.
//
// This mirrors the pattern in agent_terminal.go, which runs agent-spawned
// subprocesses under context.WithCancel(context.WithoutCancel(ctx)) +
// AfterFunc(shutdownCtx, cancel) for the same reason: a per-request ctx must not
// tear down longer-lived work.
func (h *Hub) TurnContext(reqCtx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(reqCtx))
	stop := context.AfterFunc(h.lifecycle.shutdownCtx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
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

// MCPWaitForReady blocks until MCP servers are ready or timeout.
func (h *Hub) MCPWaitForReady(ctx context.Context, timeout time.Duration) bool {
	return h.mcpRegistry.WaitForReady(ctx, timeout)
}

// ResolveInsideWorkDir validates a path is inside the workspace.
func (h *Hub) ResolveInsideWorkDir(rel string) (string, error) {
	return h.resolveInsideWorkDir(rel)
}

// PrimeIfNeeded primes the bridge with history if needed.
func (h *Hub) PrimeIfNeeded(ctx context.Context, chatID vibekit.ChatID, b command.Bridge) {
	sb, ok := b.(*sharedBridge)
	if !ok {
		slog.Error("hub: PrimeIfNeeded called with non-sharedBridge Bridge",
			"type", fmt.Sprintf("%T", b))
		return
	}
	h.coord.PrimeIfNeeded(ctx, chatID, sb)
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

// KillTurnTerminals kills the terminals the current turn created. The
// interrupt half of the tab-close contract: cancel stops the model, and this
// stops the processes the turn already spawned.
func (h *Hub) KillTurnTerminals(chatID vibekit.ChatID) {
	h.agentTerms.KillForTurn(chatID)
}
