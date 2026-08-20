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
// documents each handler file's actual dependency footprint.
//
// Each role names ONE collaborator, and Roles is flat. It used to bundle them
// into StreamingAccess (8 members) and PermissionAccess (5), and that shape is
// what built the host's god object: a composite spanning the chat store, the
// event bus, the buffer store, the line tracker, the terminal registry and the
// bridge lookup can only be satisfied by something holding all six, so the host
// grew ten forwarding methods and became the one type that qualified. A role per
// collaborator is satisfied by the collaborator itself.
//
// Two consequences. There is no ChatRecords()/BufferStore()/LineTracker()
// GETTER any more — a method returning an interface was a second indirection
// for nothing, and Roles carries the interface directly. And WorkDir is a
// string, not a method: it is a process constant, so nothing is substituted and
// no host should be in the middle of reading it.
//
// The chat-store width arithmetic still holds: this package reads and writes
// chats through 3 of the 9 methods *chat.Store offers. It never lists chats,
// never builds a history transcript, never deletes one, never touches a draft
// and never registers a route. The old note about needing a differently-NAMED
// accessor than internal/command's is obsolete with the getters gone: both
// packages now name *chat.Store directly through their own narrow interface,
// and an interface is satisfied without either side knowing the other exists.

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
type ChatRecords interface {
	// Get returns the full chat at id, or false if it does not exist.
	Get(ctx context.Context, id vibekit.ChatID) (*vibekit.Chat, bool)
	// Mutate is the single write primitive: load, apply, save, broadcast.
	Mutate(ctx context.Context, id vibekit.ChatID, mutate func(c *vibekit.Chat, exists bool) bool) error
	// AppendMessage appends msg to the chat's messages.
	AppendMessage(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error
}

// Roles is the wiring-time role set: the host names which of its interfaces
// answers each role once, at construction, and the Translator keeps them as
// separate fields so every method names the one role it uses.
//
// A plain struct rather than an interface, deliberately. Nothing in the package
// takes this type, so no method can reach the host through it, and it never
// widens as the host grows. Taken by pointer: six interface fields is 96 bytes,
// and New copies them into its own fields anyway.
type Roles struct {
	// Bus is the event fan-out, and the busiest role by far (35 call sites).
	// It was a Broadcast member on BOTH composites, which is what made every
	// split of the host drag the event bus along with it.
	Bus Broadcaster
	// Chats is the chat store at 3 methods.
	Chats ChatRecords
	// Buffers is the in-flight assistant-message store at 1 method.
	Buffers BufferAccess
	// Lines is the changed-line tracker at 1 method.
	Lines LineRecorder
	// PendingPerms registers an unanswered decision for reconnect replay.
	PendingPerms PendingPermAdder
	// Push sends a web-push notification.
	Push Pusher
	// Sessions resolves a chat's parent ACP session, to tell a subagent's frame
	// from its parent's.
	Sessions SessionResolver
	// Terminals reads an agent terminal's rendered output.
	Terminals TerminalReader
	// HookStatus reports whether hook status display is on.
	HookStatus HookStatusReader
	// WorkDir is the workspace root. A VALUE: it is a process constant, so there
	// is nothing to substitute and no reason for a method.
	WorkDir string

	MCP        MCPRecorder
	Governance GovernanceAccess
	RunOrigin  RunOriginAccess
	RunBounds  RunBoundsAccess
}

// Broadcaster publishes a domain event to every connected client.
type Broadcaster interface {
	Broadcast(ctx context.Context, evt vibekit.ServerEvent)
}

// PendingPermAdder registers an unanswered decision so a reconnecting client
// gets it replayed.
type PendingPermAdder interface {
	PendingPermsAdd(requestID int64, evt vibekit.ServerEvent)
}

// Pusher delivers a web-push notification for a chat.
type Pusher interface {
	NotifyPush(ctx context.Context, body string, kind vibekit.PushKind, chatID vibekit.ChatID)
}

// SessionResolver answers a chat's parent ACP session id, or "" when no bridge
// is running. Used to short-circuit notifications whose top-level sessionId
// belongs to a subagent rather than the parent chat.
type SessionResolver interface {
	ParentACPSession(chatID vibekit.ChatID) string
}

// HookStatusReader reports whether hook status display is enabled; when it is
// off, tool calls of kind "hook" are suppressed from the transcript.
type HookStatusReader interface {
	IsHookStatusEnabled() bool
}

// TerminalReader returns an agent terminal's rendered output: plain text with
// escapes parsed off, plus the spans styling it.
//
// ok reports whether the terminal is KNOWN, not whether it printed anything: a
// registered terminal that produced no output answers ("", nil, true). The
// distinction is the whole value of the boolean — adoption logs a miss on false,
// and a silent command reporting as missing would file that warning every turn.
//
// This is what makes the tool CARD the durable home of a command's output. KAS
// puts none of it on the tool call — a successful terminal-backed command's
// tool_call_update carries no output field at all, so before this every finished
// command persisted an empty output and the bytes lived only in an ephemeral SSE
// stream that a page reload discarded.
type TerminalReader interface {
	TerminalOutput(terminalID string) (text string, spans []vibekit.TextSpan, ok bool)
}

// GovernanceAccess caches the latest account/workspace governance state so GET
// /api/governance can serve it with no chat open (see hub/governance.go).
//
// One method, and its own name rather than a loose method on a composite,
// because it is the only thing this package asks of that cache.
type GovernanceAccess interface {
	// SetGovernance replaces the cached governance state.
	SetGovernance(state vibekit.GovernanceStatePayload)
}

// MCPRecorder groups MCP server state tracking methods, so the MCP frames can
// be stubbed independently of the rest of the host.
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

// Translator holds stateful translate logic extracted from Runtime. Each role is
// its own field, so a handler method's reach is the field it names.
type Translator struct {
	bus          Broadcaster
	chats        ChatRecords
	buffers      BufferAccess
	lines        LineRecorder
	pendingPerms PendingPermAdder
	push         Pusher
	sessions     SessionResolver
	terminals    TerminalReader
	hookStatus   HookStatusReader
	workDir      string
	mcp          MCPRecorder
	governance   GovernanceAccess
	runOrigin    RunOriginAccess
	runBounds    RunBoundsAccess
	newMsgID     func() string
	// steps maps a workflow step's ACP session id to its run and node. Fed from
	// the wire (`node_start`) and from an `inspect` read; see workflow_steps.go.
	steps *stepRegistry
}

// New constructs a Translator over the roles the host supplies.
func New(r *Roles, opts ...Option) *Translator {
	t := &Translator{
		bus:          r.Bus,
		chats:        r.Chats,
		buffers:      r.Buffers,
		lines:        r.Lines,
		pendingPerms: r.PendingPerms,
		push:         r.Push,
		sessions:     r.Sessions,
		terminals:    r.Terminals,
		hookStatus:   r.HookStatus,
		workDir:      r.WorkDir,
		mcp:          r.MCP,
		governance:   r.Governance,
		runOrigin:    r.RunOrigin,
		runBounds:    r.RunBounds,
		steps:        newStepRegistry(),
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
	return t.mcp
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
	IsScheduled(workflowID string) bool
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
