package command

// Role-based consumer interfaces decomposed from the Dependencies
// god-interface. Each interface is shaped by what its consumers
// actually need, enabling minimal stub surfaces in tests and
// documenting each handler's actual dependency footprint.
//
// The Dependencies interface remains as the composite that Hub
// satisfies. These narrow interfaces are embedded into Dependencies
// for compile-time verification.

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

// InfraDeps provides shared infrastructure operations (workspace,
// lifecycle, dedup, MCP readiness) used across multiple handlers.
type InfraDeps interface {
	WorkDir() string
	ConfigDir() string
	// TurnContext returns the context an in-flight turn runs under, plus the
	// teardown its handler defers. It replaced a ShutdownCtx() accessor that
	// handed out the hub's raw lifetime context and left every consumer to
	// derive a turn context correctly; the one consumer wanted the derivation,
	// not the lifetime.
	TurnContext(reqCtx context.Context) (context.Context, context.CancelFunc)
	InflightAdd(delta int)
	InflightDone()
	MCPWaitForReady(ctx context.Context, timeout time.Duration) bool
	ResolveInsideWorkDir(rel string) (string, error)
	IsEmptyTurn(resp *api.RPCResponse, chatID api.ChatID) bool
	EmitTurnEndedWithStats(ctx context.Context, chatID api.ChatID, resp *api.RPCResponse, creditsDelta, elapsedMs float64)
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
