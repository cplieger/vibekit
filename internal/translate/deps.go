package translate

import (
	"context"
	"time"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// This file is the package's dependency contracts: the role interfaces the
// Translator reads its host through. Each is shaped by what THIS package
// invokes, which keeps a test stub small enough to be obviously correct and
// documents each handler file's actual dependency footprint. Deps remains the
// composite the Hub satisfies, and the narrow ones are embedded into it so the
// compiler verifies the decomposition.
//
// The chat-store width arithmetic: this package reads and writes chats through
// 3 of the 9 methods *chat.Store offers. It never lists chats, never builds a
// history transcript, never deletes one, never touches a draft and never
// registers a route.
//
// The accessor is ChatRecords(), not ChatStore(), and the difference in NAME is
// load-bearing rather than cosmetic. internal/command also reads the store off
// the same *hub.Hub, through its own 5-method contract, and Go matches an
// interface method by exact signature: one accessor cannot return two different
// narrow types. Two consumers with two contracts therefore need two accessors.

// BufferAccess is the consumer-side interface for buffer store access.
// Narrows the coupling: translate only needs GetOrInit.
type BufferAccess interface {
	GetOrInit(chatID vibekit.ChatID) *buffer.Buffer
}

// LineRecorder is the consumer-side interface for line tracking.
// Narrows the coupling: translate only needs RecordFromDiffs.
type LineRecorder interface {
	RecordFromDiffs(chatID vibekit.ChatID, diffs []vibekit.ToolDiff, turn int, kind string)
}

// ChatRecords is the chat store as this package uses it: read a chat, mutate it,
// append a message. 3 of the 9 methods *chat.Store offers.
//
// Exported because Deps is exported and *hub.Hub has to name this as its
// ChatRecords() return type.
type ChatRecords interface {
	// Get returns the full chat at id, or false if it does not exist.
	Get(ctx context.Context, id vibekit.ChatID) (*vibekit.Chat, bool)
	// Mutate is the single write primitive: load, apply, save, broadcast.
	Mutate(ctx context.Context, id vibekit.ChatID, mutate func(c *vibekit.Chat, exists bool) bool) error
	// AppendMessage appends msg to the chat's messages.
	AppendMessage(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error
}

// Deps abstracts the Hub methods that stateful translate handlers need.
// Hub satisfies this interface, allowing the Translator to operate
// without importing the hub package.
type Deps interface {
	StreamingAccess
	PermissionAccess
	RunOriginAccess
	RunBoundsAccess
	// MCPRecorder returns the MCP state recorder sub-interface.
	MCPRecorder() MCPRecorder
	// SetGovernance caches the latest account/workspace governance state so
	// GET /api/governance can serve it with no chat open (see hub/governance.go).
	SetGovernance(state vibekit.GovernanceStatePayload)
}

// MCPRecorder groups MCP server state tracking methods.
// Extracted from Deps to narrow the interface (21→17 methods) and
// allow independent stubbing in tests.
type MCPRecorder interface {
	// RecordConnected marks a server connected and records what it advertises
	// (from _kiro/mcp/status): its tool names, prompts and resources. All three
	// may be nil, and all three arrive together — there is no separate
	// SetKnownTools, because a second call would be a second write of the same
	// notification and the record it lands in is replaced wholesale here.
	RecordConnected(ctx context.Context, serverName string, tools []string, prompts []vibekit.MCPPromptInfo, resources []vibekit.MCPResourceInfo)
	RecordOAuth(ctx context.Context, serverName, oauthURL string)
	RecordInitFailure(ctx context.Context, serverName, errMsg string)
	// RecordDisabled reports a server KAS says is off. The recorder keeps it
	// only when vibekit never configured it — a configured server's off state is
	// already on its config row, and this call must not resurrect one the user
	// switched off. See hub's recordDisabled for the rule.
	RecordDisabled(ctx context.Context, serverName string)
	SignalReady()
}

// Translator holds stateful translate logic extracted from Hub.
// It delegates Hub access through Deps.
type Translator struct {
	deps     Deps
	newMsgID func() string
	// steps maps a workflow step's ACP session id to its run and node. Fed from
	// the wire (`node_start`) and from an `inspect` read; see workflow_steps.go.
	steps *stepRegistry
}

// New constructs a Translator with the given Hub dependency surface.
func New(deps Deps, opts ...Option) *Translator {
	t := &Translator{
		deps:  deps,
		steps: newStepRegistry(),
	}
	for _, o := range opts {
		o(t)
	}
	if t.newMsgID == nil {
		t.newMsgID = ids.NewMessageID
	}
	return t
}

// Option configures a Translator.
type Option func(*Translator)

// withIDGenerator overrides the default message ID generator. Unexported:
// the only caller is this package's own tests, which need deterministic
// message IDs; production always takes the ids.NewMessageID default.
func withIDGenerator(fn func() string) Option {
	return func(t *Translator) { t.newMsgID = fn }
}

// newEventMessage constructs a standard event message with a fresh ID,
// RoleEvent, and the current timestamp. Eliminates 5-site boilerplate.
func (t *Translator) newEventMessage(kind vibekit.EventKind, content string) vibekit.Message {
	return vibekit.Message{
		ID:        t.newMsgID(),
		Role:      vibekit.RoleEvent,
		Ts:        time.Now().UnixMilli(),
		EventKind: kind,
		Content:   content,
	}
}

// deriveSubSession returns the sessionID when it belongs to a SUBAGENT, and ""
// for the launching chat itself OR for a workflow step.
//
// A step returning "" is the point, not a loss: a step's frames arrive on the
// chat's connection with their own session id, and every caller of this function
// treats a non-empty result as "a subagent did this" — three by dropping the
// frame and three by stamping SubSessionID. Neither is true of a step, so the
// step case is answered where it is known (ClassifyFrame) and this function
// keeps its one narrow meaning. See workflow_steps.go for the whole problem.
func (t *Translator) deriveSubSession(chatID vibekit.ChatID, sessionID string) string {
	if t.ClassifyFrame(chatID, sessionID, false) == OwnerSubagent {
		return sessionID
	}
	return ""
}

// MCP returns the MCP state recorder sub-interface.
func (t *Translator) MCP() MCPRecorder {
	return t.deps.MCPRecorder()
}

// Compile-time assertion: Deps satisfies ChatStoreDeps (not embedded).
var _ ChatStoreDeps = Deps(nil)

// StreamingAccess provides the methods needed by streaming_content.go /
// streaming_tools.go for content buffering, partial file recovery, and
// line tracking.
type StreamingAccess interface {
	Broadcast(ctx context.Context, evt vibekit.ServerEvent)
	BufferStore() BufferAccess
	LineTracker() LineRecorder
	IsHookStatusEnabled() bool
	ChatRecords() ChatRecords
	ParentACPSession(chatID vibekit.ChatID) string
	WorkDir() string
	// TerminalOutput returns an agent terminal's rendered output: plain text
	// with escapes parsed off, plus the spans styling it.
	//
	// ok reports whether the terminal is KNOWN, not whether it printed
	// anything: a registered terminal that produced no output answers
	// ("", nil, true). The distinction is the whole value of the boolean —
	// adoption logs a miss on false, and a silent command reporting as missing
	// would file that warning on every turn.
	//
	// This is what makes the tool CARD the durable home of a command's output.
	// KAS puts none of it on the tool call — a successful terminal-backed
	// command's tool_call_update carries no output field at all, so before this
	// every finished command persisted an empty output and the bytes lived only
	// in an ephemeral SSE stream that a page reload discarded.
	TerminalOutput(terminalID string) (text string, spans []vibekit.TextSpan, ok bool)
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
	Broadcast(ctx context.Context, evt vibekit.ServerEvent)
	ChatRecords() ChatRecords
	NotifyPush(ctx context.Context, body string, kind vibekit.PushKind, chatID vibekit.ChatID)
	ParentACPSession(chatID vibekit.ChatID) string
	PendingPermsAdd(requestID int64, evt vibekit.ServerEvent)
}

// ChatStoreDeps provides the minimal interface needed by handlers that
// only require chat store access and broadcast (init_errors).
type ChatStoreDeps interface {
	Broadcast(ctx context.Context, evt vibekit.ServerEvent)
	ChatRecords() ChatRecords
	ParentACPSession(chatID vibekit.ChatID) string
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
