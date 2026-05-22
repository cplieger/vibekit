package hub

import (
	"context"
	"fmt"
	"log/slog"

	"vibekit/internal/api"
)

const contentTypeText = "text"

// primeReason is the reason a bridge needs priming on the next prompt.
// Only switch currently needs it; restart-recovery uses session/load
// (kiro-cli restores context itself). The unset zero value is safe to
// interpret as "no priming needed".
type primeReason string

const (
	primeReasonNone   primeReason = ""
	primeReasonSwitch primeReason = "switch" // fresh bridge after switch_model
	modelAuto                     = "auto"
)

// getOrCreateBridge returns an existing bridge for chatID, or creates one.
// If the chat's ACP session id is non-empty, the bridge is loaded from
// that session; otherwise a new session is started. The resulting ACP
// session id is written back to the chat.
//
// Concurrency contract: on the create path we hold sb.mu from insert
// into h.bridge.mgr through bridge.Start's return. Concurrent callers
// that fetch the same sb from the map observe the locked mutex and
// will 409-busy on their own TryLock — they cannot race with a
// half-initialized bridge whose SessionID() still returns "" and
// whose readLoop is not yet forwarding. Start failure removes sb
// from the map BEFORE releasing sb.mu so the next caller re-enters
// the create branch with a clean slate.
func (h *Hub) getOrCreateBridge(ctx context.Context, chatID api.ChatID, agentOverride, modelOverride string) (*sharedBridge, error) {
	sb, existed := h.bridge.mgr.getOrInsert(chatID)
	if existed {
		return sb, nil
	}

	// sb is new: state=bridgeStarting, sb.mu is locked.
	// Every early return below MUST unlock sb.mu AND remove sb from
	// the map. setupErr wraps both steps once so no branch forgets.
	setupErr := func(err error) error {
		h.bridge.mgr.removeIfSame(chatID, sb)
		sb.state = bridgeIdle
		sb.mu.Unlock()
		return err
	}

	chat, exists := h.chatStore.Get(ctx, chatID)
	if !exists {
		return nil, setupErr(fmt.Errorf("chat %s not found", chatID))
	}

	agent := chat.Agent
	if agentOverride != "" {
		agent = agentOverride
	}
	model := chat.Model
	if modelOverride != "" && modelOverride != modelAuto {
		model = modelOverride
	}

	// Resolve permission flags once per bridge spawn. Same args are used
	// for both the session/load attempt and a fresh fallback — a process
	// can't change its --trust-* flag mid-life.
	permArgs := h.permArgsFn()

	// Resolve the MCP server set once per spawn, for the same reason —
	// a bridge cannot reconfigure its MCP servers after session/new. The
	// user sees a "Restart to apply" affordance when the persisted set
	// drifts from what the running bridge was started with.
	var mcpServers []map[string]any
	if h.mcpConfig != nil {
		mcpServers = h.mcpConfig.ACPServers(ctx)
	}

	// Try to load the existing ACP session first. If that fails (kiro-cli
	// cleaned it up, machine rebooted, etc.), fall back to a new session.
	if chat.ACPSessionID != "" {
		if h.tryLoadSession(ctx, chatID, sb, chat.ACPSessionID, agent, model, permArgs, mcpServers) {
			// sb.mu was released inside tryLoadSession on success.
			go h.forward(chatID, sb.bridge)
			return sb, nil
		}
		// Load failed: sb.bridge was reset; fall through to fresh start.
	}

	// Fresh session.
	if err := sb.bridge.Start(ctx, &api.StartOpts{Agent: agent, Model: model, ExtraArgs: permArgs, MCPServers: mcpServers}); err != nil {
		return nil, setupErr(err)
	}
	h.persistNewSessionMetadata(ctx, chatID, sb.bridge)

	// A fresh bridge from compress/switch still needs priming. Normal
	// fresh-session bridges (user just started a new chat) are implicitly
	// primed because they have no history yet; BuildHistory returns "" and
	// primeIfNeeded no-ops. sb.mu has been held since insert into
	// h.bridge.mgr, so the primed assignment is naturally serialized
	// with any concurrent caller that sees sb via the map.
	sb.primed = false
	sb.state = bridgeIdle
	sb.mu.Unlock()

	go h.forward(chatID, sb.bridge)
	return sb, nil
}

// tryLoadSession attempts session/load against the stored ACP session id.
// On success: refreshes mode/model metadata, marks sb.primed=true, RELEASES
// sb.mu, and returns true. On failure: stops the bridge, clears the stale
// session id, resets sb.bridge via the factory, and returns false with
// sb.mu still held so the caller can fall back to a fresh session.
func (h *Hub) tryLoadSession(
	ctx context.Context, chatID api.ChatID, sb *sharedBridge, acpSessionID, agent, model string,
	permArgs []string, mcpServers []map[string]any,
) bool {
	if err := sb.bridge.Start(ctx, &api.StartOpts{SessionID: acpSessionID, Agent: agent, Model: model, ExtraArgs: permArgs, MCPServers: mcpServers}); err != nil {
		slog.Warn("session/load failed, starting new",
			"chat_id", chatID, "acp_session", acpSessionID, "error", err)
		sb.bridge.Stop()
		if mErr := h.chatStore.Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
			if !ex {
				return false
			}
			c.ACPSessionID = ""
			return true
		}); mErr != nil {
			slog.Error("clear stale acp_session_id", "chat_id", chatID, "error", mErr)
		}
		sb.bridge = h.bridge.mgr.factory()
		return false
	}
	// Successfully loaded. kiro-cli has the context. Refresh
	// modes/models from the loaded session so the UI reflects
	// what the agent actually supports right now (the values
	// may have changed since the chat was last open — e.g. a
	// kiro-cli update added new models).
	if mErr := h.chatStore.Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.CurrentModeID = sb.bridge.CurrentMode()
		c.AvailableModes = sb.bridge.Modes()
		c.AvailableModels = sb.bridge.Models()
		return true
	}); mErr != nil {
		slog.Error("refresh session metadata", "chat_id", chatID, "error", mErr)
	}
	// sb.mu is held from the insert above; set primed, transition
	// to idle, and release.
	sb.primed = true
	sb.state = bridgeIdle
	sb.mu.Unlock()
	return true
}

// persistNewSessionMetadata stores the ACP session id, model, and session-
// level metadata (modes + available models) into the chat after a fresh
// session/new call. This is the single place these values cross from
// kiro-cli into our state.
func (h *Hub) persistNewSessionMetadata(ctx context.Context, chatID api.ChatID, bridge api.ACPBridge) {
	newSessionID := bridge.SessionID()
	newModelID := bridge.ModelID()
	currentMode := bridge.CurrentMode()
	modes := bridge.Modes()
	models := bridge.Models()
	if err := h.chatStore.Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.ACPSessionID = string(newSessionID)
		if newModelID != "" {
			c.Model = string(newModelID)
		}
		c.CurrentModeID = currentMode
		c.AvailableModes = modes
		c.AvailableModels = models
		return true
	}); err != nil {
		slog.Error("persist new session metadata",
			"chat_id", chatID,
			"acp_session", newSessionID,
			"model", newModelID,
			"error", err)
	}
}

// getBridge returns the bridge for chatID, or nil.
func (h *Hub) getBridge(chatID api.ChatID) *sharedBridge {
	return h.bridge.mgr.get(chatID)
}

// closeBridge stops a bridge and removes it from the map.
func (h *Hub) closeBridge(chatID api.ChatID) {
	h.bridge.mgr.close(chatID)
}

// forward is the ACP notification → domain event translator, run as a
// goroutine per bridge. Every kiro-cli notification turns into zero or
// more chat store mutations and domain SSE events.
func (h *Hub) forward(chatID api.ChatID, bridge api.ACPBridge) {
	for msg := range bridge.NotifCh() {
		h.translateACPEvent(chatID, msg)
	}

	slog.Info("bridge exited", "chat_id", chatID)

	h.bridge.mgr.removeIfBridge(chatID, bridge)
	lastBridge := h.bridge.mgr.count() == 0

	// If this was the last live bridge, the MCP subprocesses spawned by
	// kiro-cli are gone too. Clear the runtime registry so the UI stops
	// showing stale "connected" dots. Individual-bridge exits don't
	// affect the registry because MCP servers are scoped to the hub
	// (every bridge sees the same user config) — only "no bridges left"
	// is a definite end-of-life signal.
	if lastBridge {
		h.mcpRegistry.clearAll()
	}
}

// primeIfNeeded sends the chat history as an ephemeral priming prompt on
// the current bridge. Used after compress/agent-switch. The priming turn
// is NOT rendered or persisted — it's a silent context restore. The
// history is built from the chat file.
func (h *Hub) primeIfNeeded(ctx context.Context, chatID api.ChatID, sb *sharedBridge) {
	history := h.chatStore.BuildHistory(ctx, chatID)
	if history == "" {
		return
	}

	var prime string
	switch sb.primeReason {
	case primeReasonSwitch:
		prime = "The context was just switched (new agent, new model, " +
			"or both). Below is the full conversation history. Read it " +
			"silently and reply with a single short line confirming " +
			"you're caught up.\n\n" + history
	default:
		return // restart recovery uses session/load, no priming needed
	}

	slog.Info("priming bridge", "chat_id", chatID, "reason", sb.primeReason)
	_, err := sb.bridge.Call(ctx, methodPrompt, sessionParams(sb, map[string]any{
		"prompt": []map[string]any{{"type": contentTypeText, contentTypeText: prime}},
	}))
	if err != nil {
		slog.Error("prime failed", "chat_id", chatID, "error", err)
	}
}
