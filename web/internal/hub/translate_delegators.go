package hub

import (
	"context"
	"encoding/json"

	"vibekit/internal/api"
)

// --- Translate delegators ---
//
// Thin forwarding methods from Hub to h.translator. Consolidated from
// per-method files (translate_agent_switch.go, translate_commands.go, etc.)
// into a single file for navigation convenience.

func (h *Hub) handleAgentSwitched(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.translator.HandleAgentSwitched(ctx, chatID, msg)
}

func (h *Hub) handleCommandsAvailable(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.translator.HandleCommandsAvailable(ctx, chatID, msg)
}

func (h *Hub) handleCompactionStatus(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.translator.HandleCompactionStatus(ctx, chatID, msg)
}

func (h *Hub) handleMetadata(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.translator.HandleMetadata(ctx, chatID, msg)
}

func (h *Hub) handlePermissionRequest(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.translator.HandlePermissionRequest(ctx, chatID, msg)
}

func (h *Hub) handleCrewUpdate(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.translator.HandleCrewUpdate(ctx, chatID, msg)
}

func (h *Hub) handleMCPInitialized(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.translator.HandleMCPInitialized(ctx, chatID, msg)
}

func (h *Hub) handleMCPOAuth(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.translator.HandleMCPOAuth(ctx, chatID, msg)
}

func (h *Hub) handleMCPInitFailure(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.translator.HandleMCPInitFailure(ctx, chatID, msg)
}

func (h *Hub) handleAgentNotFound(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.translator.HandleAgentNotFound(ctx, chatID, msg)
}

func (h *Hub) handleModelNotFound(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.translator.HandleModelNotFound(ctx, chatID, msg)
}

func (h *Hub) handleAgentConfigError(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.translator.HandleAgentConfigError(ctx, chatID, msg)
}

func (h *Hub) handleRateLimit(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.translator.HandleRateLimit(ctx, chatID, msg)
}

func (h *Hub) handleSessionRetry(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.translator.HandleSessionRetry(ctx, chatID, msg)
}

func (h *Hub) handleSessionActivity(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.translator.HandleSessionActivity(ctx, chatID, msg)
}

func (h *Hub) handleSessionListUpdate(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.translator.HandleSessionListUpdate(ctx, chatID, msg)
}

func (h *Hub) handleInboxNotification(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.translator.HandleInboxNotification(ctx, chatID, msg)
}

func (h *Hub) handleExtSessionUpdate(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	h.translator.HandleExtSessionUpdate(ctx, chatID, msg)
}

func (h *Hub) handleAssistantChunk(ctx context.Context, chatID api.ChatID, raw json.RawMessage, isReasoning bool) {
	h.translator.HandleAssistantChunk(ctx, chatID, raw, isReasoning)
}

func (h *Hub) handleToolCall(ctx context.Context, chatID api.ChatID, raw json.RawMessage, subSessionID string) {
	h.translator.HandleToolCall(ctx, chatID, raw, subSessionID)
}

func (h *Hub) handleToolCallUpdate(ctx context.Context, chatID api.ChatID, raw json.RawMessage, subSessionID string) {
	h.translator.HandleToolCallUpdate(ctx, chatID, raw, subSessionID)
}

func (h *Hub) handlePlan(ctx context.Context, chatID api.ChatID, raw json.RawMessage) {
	h.translator.HandlePlan(ctx, chatID, raw)
}

func (h *Hub) handleModeUpdate(ctx context.Context, chatID api.ChatID, raw json.RawMessage) {
	h.translator.HandleModeUpdate(ctx, chatID, raw)
}

func (h *Hub) handleSteeringInclusion(ctx context.Context, chatID api.ChatID, raw json.RawMessage) {
	h.translator.HandleSteeringInclusion(ctx, chatID, raw)
}
