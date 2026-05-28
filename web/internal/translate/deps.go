package translate

import (
	"context"

	"vibekit/internal/api"
	"vibekit/internal/buffer"
	"vibekit/internal/permissions"
)

// BufferAccess is the consumer-side interface for buffer store access.
// Narrows the coupling: translate only needs GetOrInit.
type BufferAccess interface {
	GetOrInit(chatID api.ChatID) *buffer.Buffer
}

// LineRecorder is the consumer-side interface for line tracking.
// Narrows the coupling: translate only needs RecordFromDiffs.
type LineRecorder interface {
	RecordFromDiffs(chatID api.ChatID, diffs []api.ToolDiff, turn int, kind string)
}

// Deps abstracts the Hub methods that stateful translate handlers need.
// Hub satisfies this interface, allowing the Translator to operate
// without importing the hub package.
type Deps interface {
	Broadcast(ctx context.Context, evt api.ServerEvent)
	ChatStore() api.ChatStore
	// NewMessageID returns a new UUIDv7 message ID. Injected via the
	// interface so tests can control generated IDs.
	NewMessageID() string
	ParentACPSession(chatID api.ChatID) string
	WorkDir() string
	// BridgeNotify sends a notification to the bridge for the given chat.
	BridgeNotify(ctx context.Context, chatID api.ChatID, method string, params map[string]any) error
	// BridgeRespond sends a response to the bridge for the given chat.
	BridgeRespond(ctx context.Context, chatID api.ChatID, requestID int64, result any, err error) error
	// MCPRecorder returns the MCP state recorder sub-interface.
	MCPRecorder() MCPRecorder
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
	BufferStore() BufferAccess
	// LineTracker returns the line tracker for file-change recording.
	LineTracker() LineRecorder
	// OpenPartialFile opens the partial recovery file for a chat.
	OpenPartialFile(ctx context.Context, chatID api.ChatID, buf *buffer.Buffer)
	// IsHookStatusEnabled returns whether hook status display is enabled.
	IsHookStatusEnabled() bool
}

// MCPRecorder groups MCP server state tracking methods.
// Extracted from Deps to narrow the interface (21→17 methods) and
// allow independent stubbing in tests.
type MCPRecorder interface {
	RecordConnected(ctx context.Context, serverName string)
	RecordOAuth(ctx context.Context, serverName, oauthURL string)
	RecordInitFailure(ctx context.Context, serverName, errMsg string)
	SignalReady()
	SetKnownTools(name string, tools []string)
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
