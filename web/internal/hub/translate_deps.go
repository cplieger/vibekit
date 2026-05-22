package hub

// Deps interface implementation for translate.Translator.
// Hub satisfies translate.Deps so the Translator can access
// Hub internals without importing the hub package.

import (
	"context"

	"vibekit/internal/api"
	"vibekit/internal/buffer"
	"vibekit/internal/permissions"
	"vibekit/internal/translate"
)

var _ translate.Deps = (*Hub)(nil)

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
	return sb.bridge.Notify(ctx, method, sessionParams(sb, params))
}

// BridgeRespond sends a response to the bridge for the given chat.
func (h *Hub) BridgeRespond(ctx context.Context, chatID api.ChatID, requestID int64, result any, err error) error {
	sb := h.bridge.mgr.get(chatID)
	if sb == nil {
		return nil
	}
	return sb.bridge.Respond(ctx, requestID, result, err)
}

// MCPRecordConnected records a successful MCP server connection.
func (h *Hub) MCPRecordConnected(ctx context.Context, serverName string) {
	h.mcpRegistry.recordConnected(ctx, serverName)
}

// MCPRecordOAuth records an MCP OAuth request.
func (h *Hub) MCPRecordOAuth(ctx context.Context, serverName, oauthURL string) {
	h.mcpRegistry.recordOAuth(ctx, serverName, oauthURL)
}

// MCPRecordInitFailure records an MCP server init failure.
func (h *Hub) MCPRecordInitFailure(ctx context.Context, serverName, errMsg string) {
	h.mcpRegistry.recordInitFailure(ctx, serverName, errMsg)
}

// MCPSignalReady signals that MCP servers are ready.
func (h *Hub) MCPSignalReady() {
	h.mcpRegistry.signalReady()
}

// MCPSetKnownTools persists the tool list for a server.
func (h *Hub) MCPSetKnownTools(name string, tools []string) {
	if h.mcpConfig != nil {
		h.mcpConfig.SetKnownTools(name, tools)
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
func (h *Hub) BufferStore() *buffer.Store {
	return h.bridge.assistantBufs
}

// LineTracker returns the line tracker for file-change recording.
func (h *Hub) LineTracker() *buffer.LineTracker {
	return h.lines
}

// OpenPartialFile opens the partial recovery file for a chat.
func (h *Hub) OpenPartialFile(chatID api.ChatID, buf *buffer.Buffer) {
	h.openPartialFile(chatID, buf)
}

// IsHookStatusEnabled returns whether hook status display is enabled.
func (h *Hub) IsHookStatusEnabled() bool {
	return h.isHookStatusEnabled()
}

// NewMessageID returns a new unique message ID.
func (h *Hub) NewMessageID() string {
	return newMessageID()
}
