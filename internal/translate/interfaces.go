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

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/permissions"
)

// Compile-time assertion: Deps satisfies ChatStoreDeps (not embedded).
var _ ChatStoreDeps = Deps(nil)

// StreamingAccess provides the methods needed by streaming_content.go /
// streaming_tools.go for content buffering, partial file recovery, and
// line tracking.
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

// ChatStoreDeps provides the minimal interface needed by handlers that
// only require chat store access and broadcast (init_errors).
type ChatStoreDeps interface {
	Broadcast(ctx context.Context, evt api.ServerEvent)
	ChatStore() api.ChatStore
	ParentACPSession(chatID api.ChatID) string
}
