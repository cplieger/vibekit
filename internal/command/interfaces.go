package command

// Role-based consumer interfaces decomposed from the Dependencies
// god-interface. Each interface is shaped by what its consumers
// actually need, enabling minimal stub surfaces in tests and
// documenting each handler's actual dependency footprint.
//
// The Dependencies interface remains as the composite that Hub
// satisfies. These narrow interfaces are embedded into Dependencies
// for compile-time verification.
//
// InfraDeps used to sit at the bottom of this file as the outlier: ten
// unrelated methods behind one name, in a file whose other members were already
// correctly segregated. Its own doc comment named the seams (workspace,
// lifecycle, MCP readiness) so it is split along them, plus a fourth its comment
// omitted — the four turn-outcome methods. The "dedup" seam that comment also
// named had already left for Dependencies, which is how long the description had
// been drifting from the members.

import (
	"context"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// BridgeAccess provides bridge lifecycle operations needed by prompt,
// cancel, subagent, slash, and permission handlers.
type BridgeAccess interface {
	GetBridge(chatID api.ChatID) Bridge
	GetOrCreateBridge(ctx context.Context, chatID api.ChatID, model string) (Bridge, error)
	CloseBridge(chatID api.ChatID)
	PrimeIfNeeded(ctx context.Context, chatID api.ChatID, b Bridge)
	// PrimeFromChat notes that chatID's FIRST session should be primed with
	// another chat's transcript. The tangent's fallback (command/fork.go): a
	// refused session/fork leaves the new chat with no inherited session, so the
	// parent's history has to reach the model some other way.
	//
	// A note rather than a field on the chat record, because it describes one
	// session's launch and not the chat: it is consumed by the next spawn and
	// does not survive a restart, which is correct — by then the tangent has its
	// own conversation and is owed nothing from its parent.
	PrimeFromChat(chatID, sourceChatID api.ChatID)
}

// ChatAccess provides chat store and broadcast operations needed by
// create, delete, rewind, and supervised-mode handlers.
type ChatAccess interface {
	ChatStore() api.ChatStore
	Broadcast(ctx context.Context, evt api.ServerEvent)
	CleanupChatState(ctx context.Context, chatID api.ChatID)
	// CloseChatState is the close path's teardown: same in-memory cleanup,
	// but it leaves the chat's durable KAS session on disk so the chat can be
	// reopened and so History can still list it.
	CloseChatState(ctx context.Context, chatID api.ChatID)
	// CancelChatRuns cancels every non-terminal workflow run this chat's
	// sessions launched. Part of the tab-close contract: closing the tab kills
	// the work, and a run is durable state a dead process does NOT stop — KAS
	// reconciles it to paused and a later read revives it, so it must be told
	// to cancel, per run, before the bridge goes.
	CancelChatRuns(ctx context.Context, chatID api.ChatID)
}

// PendingPermAccess provides the pending-PERMISSION bookkeeping handlers need:
// an unanswered request is replayed to a reconnecting client, so answering or
// abandoning one has to retire the entry.
//
// It was SupervisedAccess, and the rename is the point. The pending-CHANGE half
// went with internal/pending — vibekit no longer holds writes back, so there is
// no staging queue to resolve, trust past or flush, and KAS's turn approval
// arrives as an ordinary permission request. Nothing supervised is left here,
// and a `ChatInSupervisedMode` reader with no consumer went with the gate.
type PendingPermAccess interface {
	ClearPendingPermsForChat(chatID api.ChatID)
	// TakePendingPerm claims the request before its answer is sent, and reports
	// false when another surface already answered it. It replaced a
	// remove-after-responding call: two tabs could each answer one request, and
	// the agent server discards the loser silently, so a handler must take the
	// request FIRST and give up when the take fails.
	//
	// settledBy travels with the claim because the winning take is broadcast to
	// the surfaces that lost, and their card says who answered. From a command
	// handler that is always api.SettledByUser — a person clicked something.
	TakePendingPerm(requestID int64, settledBy api.SettledBy) bool
}

// TerminalAccess is the interrupt's process half: a turn cancel must reach
// the turn's agent terminals or it strands them (§5.6 R3 — cancelling
// mid-`npm test` left the command running with no owner).
type TerminalAccess interface {
	// KillTurnTerminals kills the terminals the chat's CURRENT turn created,
	// and nothing else — a background command an earlier turn left running
	// on purpose is not the cancel's to kill.
	KillTurnTerminals(chatID api.ChatID)
}

// WorkspaceAccess provides the workspace and config paths handlers resolve
// against: the hook writer and the shell spawn need the working directory, the
// prompt reads a setting out of the config directory, and an attachment path is
// confined to the workspace before it is read.
type WorkspaceAccess interface {
	WorkDir() string
	ConfigDir() string
	// ResolveInsideWorkDir validates a path is inside the workspace. Passed as a
	// function value to BuildPromptBlocks, which is why it is here rather than
	// beside the turn methods: it is a path rule, not a turn rule.
	ResolveInsideWorkDir(rel string) (string, error)
}

// LifecycleAccess is the process-lifetime seam: the context a turn runs under,
// and the in-flight accounting that makes hub shutdown wait for it.
//
// The three belong together because they are one protocol. A handler that starts
// long-lived work takes a turn context, registers itself as in-flight, and
// deregisters on return; shutdown cancels the first and waits on the second.
type LifecycleAccess interface {
	// TurnContext returns the context an in-flight turn runs under, plus the
	// teardown its handler defers. It replaced a ShutdownCtx() accessor that
	// handed out the hub's raw lifetime context and left every consumer to
	// derive a turn context correctly; the one consumer wanted the derivation,
	// not the lifetime.
	TurnContext(reqCtx context.Context) (context.Context, context.CancelFunc)
	InflightAdd(delta int)
	InflightDone()
}

// MCPAccess is the MCP readiness gate a prompt waits on, so a first turn does
// not reach the model before the workspace's MCP servers have connected.
type MCPAccess interface {
	MCPWaitForReady(ctx context.Context, timeout time.Duration) bool
}

// TurnStats is a finished turn's two measurements.
//
// A struct because EmitTurnEndedWithStats took them as an adjacent
// `creditsDelta, elapsedMs float64` pair — on an interface METHOD, so the
// declaration, the two implementations and the call site could each be read
// without the others. A transposition compiles and is silent in both
// directions: the turn footer reports ~40,000 credits for a 40-second turn and
// 0.04 ms of elapsed time, and both values are persisted on the message, so the
// wrong numbers survive a reload with nothing to compare them against. No
// runtime guard is possible — every value either field can hold is legal in the
// other.
type TurnStats struct {
	// CreditsDelta is the chat's credit usage attributable to this turn.
	CreditsDelta float64
	// ElapsedMs is the turn's wall-clock duration in milliseconds.
	ElapsedMs float64
}

// TurnOutcomeAccess is what a prompt handler needs to finish a turn: classify
// it, record which model ran it, publish its stats, and abandon it when the
// bridge died mid-flight.
type TurnOutcomeAccess interface {
	IsEmptyTurn(resp *api.RPCResponse, chatID api.ChatID) bool
	EmitTurnEndedWithStats(ctx context.Context, chatID api.ChatID, resp *api.RPCResponse, stats TurnStats)
	AbandonInFlightTurn(ctx context.Context, chatID api.ChatID)
	// LatchTurnModel records which model this turn was DISPATCHED under, before
	// the prompt call can race a concurrent switch_model. First write wins, so
	// the value is immutable for the turn's lifetime.
	//
	// It exists because the turn buffer otherwise latches on the first assistant
	// FRAME, which can be seconds later: a fast in-session switch landing in that
	// window stamped the new model onto an answer the previous one produced.
	LatchTurnModel(chatID api.ChatID, model string)
}

// Typed accessors on Dispatcher for narrow interface access.
// These allow handler functions to document and use only the
// subset of Dependencies they actually need.

// Bridge returns the BridgeAccess subset of dependencies.
func (d *Dispatcher) Bridge() BridgeAccess { return d.deps }

// Chat returns the ChatAccess subset of dependencies.
func (d *Dispatcher) Chat() ChatAccess { return d.deps }

// PendingPerms returns the PendingPermAccess subset of dependencies.
func (d *Dispatcher) PendingPerms() PendingPermAccess { return d.deps }

// Terminals returns the TerminalAccess subset of dependencies.
func (d *Dispatcher) Terminals() TerminalAccess { return d.deps }

// Workspace returns the WorkspaceAccess subset of dependencies.
func (d *Dispatcher) Workspace() WorkspaceAccess { return d.deps }

// Lifecycle returns the LifecycleAccess subset of dependencies.
func (d *Dispatcher) Lifecycle() LifecycleAccess { return d.deps }

// MCP returns the MCPAccess subset of dependencies.
func (d *Dispatcher) MCP() MCPAccess { return d.deps }

// TurnOutcome returns the TurnOutcomeAccess subset of dependencies.
func (d *Dispatcher) TurnOutcome() TurnOutcomeAccess { return d.deps }
