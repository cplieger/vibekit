package translate

import (
	"context"
	"time"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// This file is the package's dependency contracts: the role interfaces the
// Translator reads its host through. Each is shaped by what this package
// invokes, which keeps a test stub small enough to be obviously correct.
//
// Each role names one collaborator, and Roles is flat. A composite
// spanning several stores can only be satisfied by something holding all
// of them, which is how a host grows into a god object.

// BufferAccess is where a frame's content folds: the open turn's buffer.
// Keyed by the open turn rather than by chat, since a buffer keyed by chat
// would outlive the turn that filled it.
type BufferAccess interface {
	// TurnFoldTarget returns the chat's open turn's buffer, opening a turn of the
	// given source when none is open. Never nil.
	//
	// The source is the CALLER's statement about the frame in hand — a workflow
	// step's frames fold here too, and a turn opened for one is the RUN's rather
	// than this chat's. It is read only on the open, so a frame folding into a
	// turn that is already open cannot change what that turn is.
	TurnFoldTarget(ctx context.Context, chatID vibekit.ChatID, source vibekit.TurnOpenSource) *buffer.Buffer
}

// TurnBoundary is the wire's own turn bracket, which KAS emits for every
// turn, agent-initiated included.
type TurnBoundary interface {
	// WireTurnStart binds the bracket to the pending pre-open, or closes a
	// turn whose own end never arrived and opens one the engine started.
	WireTurnStart(ctx context.Context, chatID vibekit.ChatID)
	// WireTurnEnd closes the chat's open turn with the wire's own
	// outcome. A no-op when no turn is open.
	WireTurnEnd(ctx context.Context, chatID vibekit.ChatID, stop vibekit.StopReason)
	// ReviseTurnBinding undoes a provisional binding on a frame carrying
	// agentInitiated: the started turn was the agent's, and the pre-open
	// is still owed its own bracket.
	ReviseTurnBinding(ctx context.Context, chatID vibekit.ChatID)
}

// LineRecorder is the consumer-side interface for line tracking, narrowed
// to the one method translate needs.
type LineRecorder interface {
	RecordFromDiffs(chatID vibekit.ChatID, diffs []vibekit.ToolDiff, turn int, kind string)
}

// SteerOrigins answers whose words a mid-turn steer carries.
//
// A role rather than a decode because the wire cannot say: the user's corrections
// and a workflow's reports arrive on one buffer identically, so the host's ledger
// of what vibekit itself sent is the discriminator. Total by construction, so no
// handler has to decide what an unknown id means.
type SteerOrigins interface {
	SteerOrigin(chatID vibekit.ChatID, steerID string) vibekit.SteerOrigin
}

// ChatRecords is the chat store as this package uses it: read a chat,
// mutate it, append a message, upsert the turn's plan row.
//
// Every write through it is a LATE write: it lands after the frame that
// caused it, on a chat the user may already have deleted. So
// chat.ErrTombstoned is the designed outcome here rather than a fault, and
// each site matches it and returns instead of logging an error.
type ChatRecords interface {
	// Get returns the full chat at id, or false if it does not exist.
	Get(ctx context.Context, id vibekit.ChatID) (*vibekit.Chat, bool)
	// Mutate is the single write primitive: load, apply, save, broadcast.
	Mutate(ctx context.Context, id vibekit.ChatID, mutate func(c *vibekit.Chat, exists bool) bool) error
	// AppendMessage appends msg to the chat's messages.
	AppendMessage(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error
	// UpsertTurnPlan overwrites the turn's existing plan row, or appends msg when
	// the turn carries none. One row per turn rather than one per plan frame.
	UpsertTurnPlan(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error
}

// Roles is the wiring-time role set: the host names which of its
// interfaces answers each role once, at construction, and the Translator
// keeps them as separate fields so every method names the one role it
// uses.
//
// A plain struct rather than an interface: nothing in the package takes
// this type, so it never widens as the host grows. Taken by pointer.
type Roles struct {
	// Bus is the event fan-out, the busiest role by far (35 call sites).
	Bus Broadcaster
	// Chats is the chat store at 4 methods.
	Chats ChatRecords
	// Buffers is where a frame's content folds: the open turn's buffer.
	Buffers BufferAccess
	// Turns is the wire's own turn bracket.
	Turns TurnBoundary
	// Lines is the changed-line tracker at 1 method.
	Lines LineRecorder
	// Steers answers whose words a steer carries: the ledger of what this
	// server sent.
	Steers SteerOrigins
	// PendingPerms registers an unanswered decision for reconnect replay.
	PendingPerms PendingPermAdder
	// Respond answers a server-to-client request on the chat's bridge.
	Respond Responder
	// Push sends a web-push notification.
	Push Pusher
	// Sessions resolves a chat's parent ACP session, to tell a subagent's
	// frame from its parent's.
	Sessions SessionResolver
	// Terminals reads an agent terminal's rendered output.
	Terminals TerminalReader
	// HookStatus reports whether hook status display is on.
	HookStatus HookStatusReader
	// Catalog is where a live config_option_update's model list lands: the
	// workspace's ONE copy, not a field on the chat that happened to see it.
	Catalog    ModelCatalog
	MCP        MCPRecorder
	Governance GovernanceAccess
	RunOrigin  RunOriginAccess
	RunBounds  RunBoundsAccess
	// TurnInterrupt ends a turn kiro-cli abandoned without answering.
	TurnInterrupt TurnInterruptAccess
	// Metering is the per-turn accounting a turn_completion frame writes.
	Metering TurnMetering
	// WorkDir is the workspace root. A value: it is a process constant,
	// so there is nothing to substitute. Last for fieldalignment.
	WorkDir string
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

// Responder answers a server-to-client ACP request on the chat's bridge.
//
// The one role here that writes to the wire rather than the event bus: a
// request vibekit declines to process still has to be answered, since
// KAS's sendRequest carries no timeout and an unanswered ask strands the
// tool batch until process teardown.
//
// A chat with no bridge is not an error — a response has nowhere to go —
// so an implementation reports nil for that case rather than a failure.
type Responder interface {
	BridgeRespond(ctx context.Context, chatID vibekit.ChatID, requestID int64, result any, err error) error
}

// Pusher delivers a web-push notification for a chat.
type Pusher interface {
	NotifyPush(ctx context.Context, body string, kind vibekit.PushKind, chatID vibekit.ChatID)
}

// SessionResolver answers a chat's parent ACP session id, or "" when no
// bridge is running. Used to short-circuit notifications whose top-level
// sessionId belongs to a subagent rather than the parent chat.
type SessionResolver interface {
	ParentACPSession(chatID vibekit.ChatID) string
}

// HookStatusReader reports whether hook status display is enabled; when
// off, tool calls of kind "hook" are suppressed from the transcript.
type HookStatusReader interface {
	IsHookStatusEnabled() bool
}

// ModelCatalog is the workspace's model vocabulary, which a live
// config_option_update updates.
//
// One method, and deliberately WRITE-only from here: this package folds frames
// and has no reason to read the catalog back. It reports whether the list
// changed because the caller's contract is the same — the chat store only
// persists and broadcasts on a change, so a repeated frame has to answer false.
type ModelCatalog interface {
	SetModels(models []vibekit.SessionModel) bool
}

// TerminalReader returns an agent terminal's rendered output: plain text
// with escapes parsed off, plus the spans styling it.
//
// ok reports whether the terminal is known, not whether it printed
// anything: a registered terminal that produced no output answers
// ("", nil, true).
//
// This is what makes the tool card the durable home of a command's
// output: KAS puts none of it on the tool call, so before this every
// finished command persisted an empty output and the bytes lived only in
// an ephemeral SSE stream a page reload discarded.
type TerminalReader interface {
	Output(terminalID string) (text string, spans []vibekit.TextSpan, ok bool)
}

// GovernanceAccess caches the latest account/workspace governance state so
// GET /api/governance can serve it with no chat open.
type GovernanceAccess interface {
	// SetGovernance replaces the cached governance state.
	SetGovernance(state vibekit.GovernanceStatePayload)
}

// MCPRecorder groups MCP server state tracking methods.
type MCPRecorder interface {
	// RecordConnected marks a server connected and records what it
	// advertises (from _kiro/mcp/status): its tool names, prompts and
	// resources. All three may be nil and all three arrive together —
	// the record they land in is replaced wholesale here.
	RecordConnected(ctx context.Context, serverName string, tools []string, prompts []vibekit.MCPPromptInfo, resources []vibekit.MCPResourceInfo)
	RecordOAuth(ctx context.Context, serverName, oauthURL string)
	RecordInitFailure(ctx context.Context, serverName, errMsg string)
	// RecordDisabled reports a server KAS says is off. Kept only when
	// vibekit never configured it — a configured server's off state is
	// already on its config row, and this call must not resurrect one
	// the user switched off.
	RecordDisabled(ctx context.Context, serverName string)
	SignalReady()
}

// Translator holds stateful translate logic extracted from Runtime. Each
// role is its own field, so a handler method's reach is the field it
// names.
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
	// Fed from the wire (node_start) and from an inspect read.
	steps *stepRegistry
	// suppressed holds the tool-call ids of dropped internal-tool
	// frames, so the follow-up tool_call_update is dropped before
	// TurnFoldTarget can open a wire turn for it.
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

// withIDGenerator overrides the default message ID generator. Unexported:
// only this package's own tests need deterministic message IDs;
// production always takes the ids.NewMessageID default.
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

// deriveSubSession returns the sessionID when it belongs to a subagent,
// and "" for the launching chat itself or for a workflow step.
//
// A step returning "" is the point: a step's frames arrive on the chat's
// connection with their own session id, and every caller of this function
// treats a non-empty result as "a subagent did this". The step case is
// answered where it is known (ClassifyFrame).
func (t *Translator) deriveSubSession(chatID vibekit.ChatID, sessionID string) string {
	if t.ClassifyFrame(chatID, sessionID, false) == OwnerSubagent {
		return sessionID
	}
	return ""
}

// RunOriginAccess answers whether a workflow run was launched by a
// schedule. Keyed by workflow id rather than chat id, since a parentless
// run's frames carry no topic.
//
// In-memory on the host, so a run that outlives a restart reports false
// afterwards — a missing start signal on a resume, not a wrong one.
type RunOriginAccess interface {
	IsScheduled(workflowID string) bool
}

// RunBoundsAccess reports a workflow step that blew its turn cap.
//
// Takes the breach rather than asking permission for it: counting
// belongs here (the step's tool frames pass through this package), while
// enforcement belongs on the host (it owns the bridges and the only stop
// verb, which is run-scoped). The host is expected to cancel the whole
// run, since no per-step stop verb exists on the wire.
type RunBoundsAccess interface {
	StepTurnCapExceeded(workflowID, nodeID string, turns int)
}

// TurnInterruptAccess ends a turn kiro-cli has abandoned without
// answering.
//
// Same split as RunBoundsAccess: detection belongs here (the sentinel
// arrives as an assistant text chunk) and termination belongs on the
// host, which owns the bridges and the in-flight prompt's cancel func.
//
// reason travels because the host cannot derive it — the divider needs
// the attribution, and only the detector knows which sentinel matched.
//
// Advisory in one direction only: the host may decline (no turn in
// flight, or a user cancel already claimed this one).
type TurnInterruptAccess interface {
	InterruptTurn(chatID vibekit.ChatID, reason string)
}

// TurnMetering is the per-turn accounting a turn_completion frame writes,
// split into two operations because the frame carries two facts with
// different owners: a workflow step's credits are spent by the chat that
// launched it and accumulate there, while the conversation turn count and
// duration are the conversation's and a step must not move them.
type TurnMetering interface {
	// AccumulateSpend adds a turn_completion's credit spend, cumulatively,
	// for every frame — step or not.
	AccumulateSpend(ctx context.Context, chatID vibekit.ChatID, credits float64)
	// StageConversationTurnSummary records a conversation turn's reported
	// duration, accumulating rather than overwriting so several frames
	// for one turn sum.
	StageConversationTurnSummary(ctx context.Context, chatID vibekit.ChatID, elapsedMs float64)
}
