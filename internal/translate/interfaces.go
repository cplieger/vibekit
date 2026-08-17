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
	IsHookStatusEnabled() bool
	ChatStore() api.ChatStore
	ParentACPSession(chatID api.ChatID) string
	WorkDir() string
	// TerminalOutput returns an agent terminal's rendered output: plain text
	// with escapes parsed off and secrets masked, plus the spans styling it.
	// ok is false when no terminal with that id is registered.
	//
	// This is what makes the tool CARD the durable home of a command's output.
	// KAS puts none of it on the tool call — a successful terminal-backed
	// command's tool_call_update carries no output field at all, so before this
	// every finished command persisted an empty output and the bytes lived only
	// in an ephemeral SSE stream that a page reload discarded.
	TerminalOutput(terminalID string) (text string, spans []api.TextSpan, ok bool)
}

// PermissionAccess provides the methods needed by permission_handler.go
// for permission request handling and push notifications. Shell-command
// authorization is owned by kiro-cli's native Cedar policy: any
// session/request_permission that reaches vibekit is a genuine ask, so
// there is no auto-approval surface here. Answering a request is NOT in
// this surface either: the user's choice arrives as a separate
// permission_response command and is forwarded to the bridge by
// internal/command, never from a translate handler.
type PermissionAccess interface {
	Broadcast(ctx context.Context, evt api.ServerEvent)
	ChatStore() api.ChatStore
	NotifyPush(ctx context.Context, body string, kind api.PushKind)
	ParentACPSession(chatID api.ChatID) string
	PendingPermsAdd(requestID int64, evt api.ServerEvent)
}

// ChatStoreDeps provides the minimal interface needed by handlers that
// only require chat store access and broadcast (init_errors).
type ChatStoreDeps interface {
	Broadcast(ctx context.Context, evt api.ServerEvent)
	ChatStore() api.ChatStore
	ParentACPSession(chatID api.ChatID) string
}
