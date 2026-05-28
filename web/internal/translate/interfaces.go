package translate

// Role-based consumer interfaces decomposed from the Deps god-interface.
// Each interface is shaped by what its consumer handler files actually
// need, enabling minimal stub surfaces in tests and documenting each
// handler's actual dependency footprint.
//
// The Deps interface remains as the composite that Hub satisfies.
// These narrow interfaces provide typed accessors on the Translator
// for handler methods to consume.

import (
	"context"

	"vibekit/internal/api"
	"vibekit/internal/buffer"
	"vibekit/internal/permissions"
)

// Compile-time assertions: Deps satisfies all role-based interfaces.
var (
	_ StreamingAccess  = (Deps)(nil)
	_ PermissionAccess = (Deps)(nil)
	_ BridgeComm       = (Deps)(nil)
	_ ChatStoreDeps    = (Deps)(nil)
)

// StreamingAccess provides the methods needed by session_streaming.go
// for content buffering, partial file recovery, and line tracking.
type StreamingAccess interface {
	Broadcast(ctx context.Context, evt api.ServerEvent)
	BufferStore() BufferAccess
	LineTracker() LineRecorder
	OpenPartialFile(ctx context.Context, chatID api.ChatID, buf *buffer.Buffer)
	IsHookStatusEnabled() bool
	ChatStore() api.ChatStore
	ParentACPSession(chatID api.ChatID) string
	WorkDir() string
}

// PermissionAccess provides the methods needed by permission_handler.go
// for permission request handling, auto-approval, and push notifications.
type PermissionAccess interface {
	Broadcast(ctx context.Context, evt api.ServerEvent)
	BridgeRespond(ctx context.Context, chatID api.ChatID, requestID int64, result any, err error) error
	ChatStore() api.ChatStore
	ConfigDir() string
	NotifyPush(ctx context.Context, body string, kind api.PushKind)
	ParentACPSession(chatID api.ChatID) string
	PendingPermsAdd(requestID int64, evt api.ServerEvent)
	PendingPermsRemove(requestID int64)
	PermissionRules() *permissions.CommandRules
}

// BridgeComm provides bridge communication methods needed by compact.go
// and permission_handler.go for sending notifications and responses.
type BridgeComm interface {
	BridgeNotify(ctx context.Context, chatID api.ChatID, method string, params map[string]any) error
	BridgeRespond(ctx context.Context, chatID api.ChatID, requestID int64, result any, err error) error
}

// MCPState provides MCP server state tracking methods needed by mcp.go
// and commands_handler.go. Alias for MCPRecorder.
type MCPState = MCPRecorder

// ChatStoreDeps provides the minimal interface needed by handlers that
// only require chat store access and broadcast (init_errors, metadata,
// agent_switch, crew_handler, subagent_session).
type ChatStoreDeps interface {
	Broadcast(ctx context.Context, evt api.ServerEvent)
	ChatStore() api.ChatStore
	ParentACPSession(chatID api.ChatID) string
}

// Typed accessors on Translator for narrow interface access.
// These allow handler methods to document and use only the
// subset of Deps they actually need.

// Streaming returns the StreamingAccess subset of deps.
func (t *Translator) Streaming() StreamingAccess { return t.deps }

// Permission returns the PermissionAccess subset of deps.
func (t *Translator) Permission() PermissionAccess { return t.deps }

// BridgeComms returns the BridgeComm subset of deps.
func (t *Translator) BridgeComms() BridgeComm { return t.deps }

// MCP returns the MCPState subset of deps.
func (t *Translator) MCP() MCPState { return t.deps.MCPRecorder() }

// ChatDeps returns the ChatStoreDeps subset of deps.
func (t *Translator) ChatDeps() ChatStoreDeps { return t.deps }
