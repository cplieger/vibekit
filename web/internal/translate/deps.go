package translate

import (
	"context"

	"vibekit/internal/api"
	"vibekit/internal/buffer"
	"vibekit/internal/permissions"
)

// Deps abstracts the Hub methods that stateful translate handlers need.
// Hub satisfies this interface, allowing the Translator to operate
// without importing the hub package.
type Deps interface {
	Broadcast(ctx context.Context, evt api.ServerEvent)
	ChatStore() api.ChatStore
	ParentACPSession(chatID api.ChatID) string
	WorkDir() string
	// BridgeNotify sends a notification to the bridge for the given chat.
	BridgeNotify(ctx context.Context, chatID api.ChatID, method string, params map[string]any) error
	// BridgeRespond sends a response to the bridge for the given chat.
	BridgeRespond(ctx context.Context, chatID api.ChatID, requestID int64, result any, err error) error
	// MCPRecordConnected records a successful MCP server connection.
	MCPRecordConnected(ctx context.Context, serverName string)
	// MCPRecordOAuth records an MCP OAuth request.
	MCPRecordOAuth(ctx context.Context, serverName, oauthURL string)
	// MCPRecordInitFailure records an MCP server init failure.
	MCPRecordInitFailure(ctx context.Context, serverName, errMsg string)
	// MCPSignalReady signals that MCP servers are ready.
	MCPSignalReady()
	// MCPSetKnownTools persists the tool list for a server so the UI
	// can show suggestions in the per-tool deny section.
	MCPSetKnownTools(name string, tools []string)
	// PendingPermsAdd tracks a pending permission event for SSE replay.
	PendingPermsAdd(requestID int64, evt api.ServerEvent)
	// PendingPermsRemove removes a pending permission by request ID.
	PendingPermsRemove(requestID int64)
	// NotifyPush sends a push notification.
	NotifyPush(ctx context.Context, body string, kind api.PushKind)
	// ConfigDir returns the configuration directory path.
	ConfigDir() string
	// PermissionRules returns the shell command rules.
	PermissionRules() *permissions.CommandRules
	// BufferStore returns the buffer store for streaming handlers.
	BufferStore() *buffer.Store
	// LineTracker returns the line tracker for file-change recording.
	LineTracker() *buffer.LineTracker
	// OpenPartialFile opens the partial recovery file for a chat.
	OpenPartialFile(chatID api.ChatID, buf *buffer.Buffer)
	// IsHookStatusEnabled returns whether hook status display is enabled.
	IsHookStatusEnabled() bool
	// NewMessageID returns a new unique message ID.
	NewMessageID() string
}

// Translator holds stateful translate logic extracted from Hub.
// It owns the crew cache and delegates Hub access through Deps.
type Translator struct {
	deps      Deps
	crewCache *crewCache
}

// New constructs a Translator with the given Hub dependency surface.
func New(deps Deps) *Translator {
	return &Translator{
		deps:      deps,
		crewCache: newCrewCache(),
	}
}
