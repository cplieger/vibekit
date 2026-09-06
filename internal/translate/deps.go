package translate

import (
	"context"
	"time"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// BufferAccess resolves the buffer a frame's content folds into.
type BufferAccess interface {
	// TurnFoldTarget returns the chat's open turn's buffer, opening one of the
	// given source when none is open. Never nil; source is read only on the open.
	TurnFoldTarget(ctx context.Context, chatID vibekit.ChatID, source vibekit.TurnOpenSource) *buffer.Buffer
}

// TurnBoundary is the wire's own turn bracket, emitted for every turn.
type TurnBoundary interface {
	// WireTurnStart binds the bracket to the pending pre-open, or closes a turn
	// whose own end never arrived.
	WireTurnStart(ctx context.Context, chatID vibekit.ChatID)
	// WireTurnEnd closes the chat's open turn. A no-op when none is open.
	WireTurnEnd(ctx context.Context, chatID vibekit.ChatID, stop vibekit.StopReason)
	// ReviseTurnBinding undoes a provisional binding on an agent-initiated frame.
	ReviseTurnBinding(ctx context.Context, chatID vibekit.ChatID)
}

// LineRecorder records the changed lines a frame's diffs describe.
type LineRecorder interface {
	RecordFromDiffs(chatID vibekit.ChatID, diffs []vibekit.ToolDiff, turn int, kind string)
}

// SteerOrigins answers whose words a mid-turn steer carries. Total by
// construction: an unknown id still gets an answer.
type SteerOrigins interface {
	SteerOrigin(chatID vibekit.ChatID, steerID string) vibekit.SteerOrigin
}

// ChatRecords is the chat store as this package uses it. Every write lands after
// the frame that caused it, so chat.ErrTombstoned is an expected outcome here.
type ChatRecords interface {
	// Get returns the full chat at id, or false if it does not exist.
	Get(ctx context.Context, id vibekit.ChatID) (*vibekit.Chat, bool)
	// Mutate is the single write primitive: load, apply, save, broadcast.
	Mutate(ctx context.Context, id vibekit.ChatID, mutate func(c *vibekit.Chat, exists bool) bool) error
	// AppendMessage appends msg to the chat's messages.
	AppendMessage(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error
	// UpsertTurnPlan overwrites the turn's plan row, or appends msg when it has none.
	UpsertTurnPlan(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error
}

// Roles is the wiring-time role set: the host names which of its interfaces
// answers each role once, at construction.
type Roles struct {
	// Bus is the event fan-out.
	Bus Broadcaster
	// Chats is the chat store.
	Chats ChatRecords
	// Buffers is the open turn's buffer.
	Buffers BufferAccess
	// Turns is the wire's own turn bracket.
	Turns TurnBoundary
	// Lines is the changed-line tracker.
	Lines LineRecorder
	// Steers answers whose words a steer carries.
	Steers SteerOrigins
	// PendingPerms registers an unanswered decision for reconnect replay.
	PendingPerms PendingPermAdder
	// Respond answers a server-to-client request on the chat's bridge.
	Respond Responder
	// Push sends a web-push notification.
	Push Pusher
	// Sessions resolves a chat's parent ACP session.
	Sessions SessionResolver
	// Terminals reads an agent terminal's rendered output.
	Terminals TerminalReader
	// HookStatus reports whether hook status display is on.
	HookStatus HookStatusReader
	// Catalog is where a live config_option_update's model list lands.
	Catalog    ModelCatalog
	MCP        MCPRecorder
	Governance GovernanceAccess
	RunOrigin  RunOriginAccess
	RunBounds  RunBoundsAccess
	// TurnInterrupt ends a turn kiro-cli abandoned without answering.
	TurnInterrupt TurnInterruptAccess
	// Metering is the per-turn accounting a turn_completion frame writes.
	Metering TurnMetering
	// WorkDir is the workspace root. Last for fieldalignment.
	WorkDir string
}

// Broadcaster publishes a domain event to every connected client.
type Broadcaster interface {
	Broadcast(ctx context.Context, evt vibekit.ServerEvent)
}

// PendingPermAdder registers an unanswered decision for reconnect replay.
type PendingPermAdder interface {
	PendingPermsAdd(requestID int64, evt vibekit.ServerEvent)
}

// Responder answers a server-to-client ACP request on the chat's bridge. Every
// ask must be answered — KAS's sendRequest carries no timeout, so an unanswered
// one strands the tool batch — and a chat with no bridge reports nil, not error.
type Responder interface {
	BridgeRespond(ctx context.Context, chatID vibekit.ChatID, requestID int64, result any, err error) error
}

// Pusher delivers a web-push notification for a chat.
type Pusher interface {
	NotifyPush(ctx context.Context, body string, kind vibekit.PushKind, chatID vibekit.ChatID)
}

// SessionResolver answers a chat's parent ACP session id, or "" when no bridge
// is running.
type SessionResolver interface {
	ParentACPSession(chatID vibekit.ChatID) string
}

// HookStatusReader reports whether hook status display is enabled.
type HookStatusReader interface {
	IsHookStatusEnabled() bool
}

// ModelCatalog is the workspace's model vocabulary, which a live
// config_option_update updates. SetModels reports whether the list changed.
type ModelCatalog interface {
	SetModels(models []vibekit.SessionModel) bool
}

// TerminalReader returns an agent terminal's rendered output: plain text with
// escapes parsed off, plus the spans styling it. ok reports whether the terminal
// is known, not whether it printed anything.
type TerminalReader interface {
	Output(terminalID string) (text string, spans []vibekit.TextSpan, ok bool)
}

// GovernanceAccess caches the latest governance state so GET /api/governance can
// serve it with no chat open.
type GovernanceAccess interface {
	// SetGovernance replaces the cached governance state.
	SetGovernance(state vibekit.GovernanceStatePayload)
}

// MCPRecorder groups MCP server state tracking methods.
type MCPRecorder interface {
	// RecordConnected marks a server connected and replaces what it advertises
	// (tools, prompts, resources) wholesale; any of the three may be nil.
	RecordConnected(ctx context.Context, serverName string, tools []string, prompts []vibekit.MCPPromptInfo, resources []vibekit.MCPResourceInfo)
	RecordOAuth(ctx context.Context, serverName, oauthURL string)
	RecordInitFailure(ctx context.Context, serverName, errMsg string)
	// RecordDisabled reports a server KAS says is off. Kept only when vibekit
	// never configured it, so it cannot resurrect one the user switched off.
	RecordDisabled(ctx context.Context, serverName string)
	SignalReady()
}

// Translator holds the package's stateful translate logic, one field per role.
type Translator struct {
	bus           Broadcaster
	chats         ChatRecords
	buffers       BufferAccess
	turns         TurnBoundary
	lines         LineRecorder
	steers        SteerOrigins
	pendingPerms  PendingPermAdder
	respond       Responder
	push          Pusher
	sessions      SessionResolver
	terminals     TerminalReader
	hookStatus    HookStatusReader
	catalog       ModelCatalog
	mcp           MCPRecorder
	governance    GovernanceAccess
	runOrigin     RunOriginAccess
	runBounds     RunBoundsAccess
	turnInterrupt TurnInterruptAccess
	metering      TurnMetering
	newMsgID      func() string
	// steps maps a workflow step's ACP session id to its run and node.
	steps *stepRegistry
	// suppressed holds the tool-call ids of dropped internal-tool frames.
	suppressed *suppressedTools
	workDir    string // last for fieldalignment, as in Roles
}

// New constructs a Translator over the roles the host supplies.
func New(r *Roles, opts ...Option) *Translator {
	t := &Translator{
		bus:           r.Bus,
		chats:         r.Chats,
		buffers:       r.Buffers,
		turns:         r.Turns,
		lines:         r.Lines,
		steers:        r.Steers,
		pendingPerms:  r.PendingPerms,
		respond:       r.Respond,
		push:          r.Push,
		sessions:      r.Sessions,
		terminals:     r.Terminals,
		hookStatus:    r.HookStatus,
		catalog:       r.Catalog,
		workDir:       r.WorkDir,
		mcp:           r.MCP,
		governance:    r.Governance,
		runOrigin:     r.RunOrigin,
		runBounds:     r.RunBounds,
		turnInterrupt: r.TurnInterrupt,
		metering:      r.Metering,
		steps:         newStepRegistry(),
		suppressed:    newSuppressedTools(),
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

// withIDGenerator overrides the default message ID generator.
func withIDGenerator(fn func() string) Option {
	return func(t *Translator) { t.newMsgID = fn }
}

// newEventMessage constructs an event message with a fresh ID, RoleEvent and the
// current timestamp.
func (t *Translator) newEventMessage(kind vibekit.EventKind, content string) vibekit.Message {
	return vibekit.Message{
		ID:        t.newMsgID(),
		Role:      vibekit.RoleEvent,
		Ts:        time.Now().UnixMilli(),
		EventKind: kind,
		Content:   content,
	}
}

// deriveSubSession returns the sessionID when it belongs to a subagent, and ""
// for the launching chat itself or for a workflow step.
func (t *Translator) deriveSubSession(chatID vibekit.ChatID, sessionID string) string {
	if t.ClassifyFrame(chatID, sessionID, false) == OwnerSubagent {
		return sessionID
	}
	return ""
}

// RunOriginAccess answers whether a workflow run was launched by a schedule,
// keyed by workflow id because a parentless run's frames carry no topic.
// In-memory on the host: a run that outlives a restart reports false afterwards.
type RunOriginAccess interface {
	IsScheduled(workflowID string) bool
}

// RunBoundsAccess reports a workflow step that blew its turn cap. The host is
// expected to cancel the whole run, since the wire has no per-step stop verb.
type RunBoundsAccess interface {
	StepTurnCapExceeded(workflowID, nodeID string, turns int)
}

// TurnInterruptAccess ends a turn kiro-cli has abandoned without answering.
// reason travels because only the detector knows which sentinel matched.
// Advisory: the host may decline when no turn is in flight.
type TurnInterruptAccess interface {
	InterruptTurn(chatID vibekit.ChatID, reason string)
}

// TurnMetering is the per-turn accounting a turn_completion frame writes, split
// in two because a step's credits belong to the launching chat while the
// conversation turn count and duration are the conversation's.
type TurnMetering interface {
	// AccumulateSpend adds a turn_completion's credit spend, step frames included.
	AccumulateSpend(ctx context.Context, chatID vibekit.ChatID, credits float64)
	// StageConversationTurnSummary accumulates a conversation turn's reported
	// duration, so several frames for one turn sum.
	StageConversationTurnSummary(ctx context.Context, chatID vibekit.ChatID, elapsedMs float64)
}
