package hub

// Deps interface implementation for translate.Translator.
// Hub satisfies translate.Deps so the Translator can access
// Hub internals without importing the hub package.

import (
	"context"

	"vibekit/internal/api"
	"vibekit/internal/buffer"
	"vibekit/internal/command"
	"vibekit/internal/ids"
	"vibekit/internal/permissions"
	"vibekit/internal/translate"
)

var _ translate.Deps = (*Hub)(nil)

// NewMessageID returns a new UUIDv7 message ID.
func (h *Hub) NewMessageID() string { return ids.NewMessageID() }

// ChatStore returns the hub's chat store.
func (h *Hub) ChatStore() api.ChatStore { return h.chatStore }

// ParentACPSession returns the parent ACP session ID for a chat.
func (h *Hub) ParentACPSession(chatID api.ChatID) string {
	return h.parentACPSession(chatID)
}

// WorkDir returns the workspace root directory.
func (h *Hub) WorkDir() string { return h.lifecycle.workDir }

// BridgeNotify sends a notification to the bridge for the given chat.
func (h *Hub) BridgeNotify(ctx context.Context, chatID api.ChatID, method string, params map[string]any) error {
	sb := h.bridge.mgr.get(chatID)
	if sb == nil {
		return nil
	}
	return sb.bridge.Notify(ctx, method, command.SessionParams(sb, params))
}

// BridgeRespond sends a response to the bridge for the given chat.
func (h *Hub) BridgeRespond(ctx context.Context, chatID api.ChatID, requestID int64, result any, err error) error {
	sb := h.bridge.mgr.get(chatID)
	if sb == nil {
		return nil
	}
	return sb.bridge.Respond(ctx, requestID, result, err)
}

// MCPRecorder returns the Hub's MCP state recorder.
func (h *Hub) MCPRecorder() translate.MCPRecorder {
	return &hubMCPRecorder{h: h}
}

// hubMCPRecorder adapts Hub's MCP internals to the MCPRecorder interface.
type hubMCPRecorder struct{ h *Hub }

func (r *hubMCPRecorder) RecordConnected(ctx context.Context, serverName string) {
	r.h.mcpRegistry.recordConnected(ctx, serverName)
}
func (r *hubMCPRecorder) RecordOAuth(ctx context.Context, serverName, oauthURL string) {
	r.h.mcpRegistry.recordOAuth(ctx, serverName, oauthURL)
}
func (r *hubMCPRecorder) RecordInitFailure(ctx context.Context, serverName, errMsg string) {
	r.h.mcpRegistry.recordInitFailure(ctx, serverName, errMsg)
}
func (r *hubMCPRecorder) SignalReady() {
	r.h.mcpRegistry.signalReady()
}
func (r *hubMCPRecorder) SetKnownTools(ctx context.Context, name string, tools []string) {
	if r.h.mcpConfig != nil {
		r.h.mcpConfig.SetKnownTools(ctx, name, tools)
	}
}

// PendingPermsAdd tracks a pending permission event for SSE replay.
func (h *Hub) PendingPermsAdd(requestID int64, evt api.ServerEvent) {
	h.sse.pendingPerms.Add(requestID, evt)
}

// PendingPermsRemove removes a pending permission by request ID.
func (h *Hub) PendingPermsRemove(requestID int64) {
	h.sse.pendingPerms.Remove(requestID)
}

// NotifyPush sends a push notification.
func (h *Hub) NotifyPush(ctx context.Context, body string, kind api.PushKind) {
	h.notifyPush(ctx, body, kind)
}

// PermissionRules returns the shell command rules.
func (h *Hub) PermissionRules() *permissions.CommandRules {
	return h.perm.rules
}

// BufferStore returns the buffer store for streaming handlers.
func (h *Hub) BufferStore() translate.BufferAccess {
	return h.bridge.assistantBufs
}

// LineTracker returns the line tracker for file-change recording.
func (h *Hub) LineTracker() translate.LineRecorder {
	return h.lines
}

// OpenPartialFile opens the partial recovery file for a chat.
func (h *Hub) OpenPartialFile(ctx context.Context, chatID api.ChatID, buf *buffer.Buffer) {
	h.openPartialFile(ctx, chatID, buf)
}

// IsHookStatusEnabled returns whether hook status display is enabled.
func (h *Hub) IsHookStatusEnabled() bool {
	return h.isHookStatusEnabled()
}
