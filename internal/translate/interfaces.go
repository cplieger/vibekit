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
	NotifyPush(ctx context.Context, body string, kind api.PushKind, chatID api.ChatID)
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

// RunOriginAccess answers whether a workflow run was launched by a SCHEDULE.
//
// One method, because that is the whole question workflow.go asks. The fact lives
// on the host: the scheduler's launch path carries a schedule id and marks the run
// with it, and nothing on the ACP wire distinguishes a scheduled run from a manual
// one (both are parentless, both arrive with an empty chat id). Keyed by workflow
// id rather than chat id for the same reason — a parentless run's frames carry no
// topic.
//
// In-memory on the host, so a run that OUTLIVES a restart reports false
// afterwards: the mark is gone, and vibekit genuinely no longer knows the run was
// scheduled. That is a missing start signal on a resume, not a wrong one.
type RunOriginAccess interface {
	IsScheduledRun(workflowID string) bool
}

// RunBoundsAccess reports a workflow STEP that blew its turn cap.
//
// One method, and it takes the breach rather than asking permission for it,
// because the two halves of the cap live in different places on purpose:
// COUNTING belongs here (the step's tool frames pass through this package, and
// `_meta.kiro.workflow` is what identifies them), while ENFORCEMENT belongs on
// the host (it owns the bridges and the only stop verb, which is run-scoped).
//
// The host is expected to cancel the whole RUN, since no per-step stop verb
// exists on the wire; that is the host's decision to state, not this package's to
// assume, which is why the name says what happened rather than what to do.
type RunBoundsAccess interface {
	StepTurnCapExceeded(workflowID, nodeID string, turns int)
}
