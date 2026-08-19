package command

import (
	"context"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// This file is the package's dependency contracts: the role interfaces the
// Dispatcher's dependencies are read through, and the per-chat bridge
// interfaces the handlers call. Each is shaped by what THIS package invokes,
// which is what keeps a test stub small enough to be obviously correct.
//
// Dependencies remains the composite the Hub satisfies, and the narrow role
// interfaces are embedded into it so the compiler verifies the decomposition.
//
// InfraDeps used to sit at the bottom of this file as the outlier: ten
// unrelated methods behind one name, in a file whose other members were already
// correctly segregated. Its own doc comment named the seams (workspace,
// lifecycle, MCP readiness) so it is split along them, plus a fourth its comment
// omitted — the four turn-outcome methods. The "dedup" seam that comment also
// named had already left for Dependencies, which is how long the description had
// been drifting from the members.

// The bridge interfaces below are declared HERE, at the consumer, rather than in
// a shared contract package. *hub.sharedBridge is what satisfies them, and the
// check is forced where hub calls SessionParams(sb, …) rather than by an
// assertion anybody has to remember to write.
//
// The width arithmetic runs from the inside out: SessionParams needs 1 of the
// bridge's 12 methods, the prompt retry needs 1, a rewind needs 2, and only a
// handler that holds the bridge for a whole turn needs all 12. A single Bridge
// parameter on every one of those made each helper claim the whole surface, so
// the seams are named and each signature takes the narrowest that fits.

// sessionScoped names the ACP session an RPC is addressed to. 1 of the 12
// methods the bridge offers, and the only one SessionParams — which every RPC
// helper on this path funnels through — has any use for.
type sessionScoped interface {
	// SessionID returns the current ACP session ID.
	SessionID() vibekit.SessionID
}

// bridgeCaller sends one request to kiro-cli and waits for its answer. 1 of 12:
// the prompt retry loop re-invokes exactly this and nothing else, so a wider
// parameter would let a future edit reach the prompt slot from inside a retry.
type bridgeCaller interface {
	// Call sends an RPC call to kiro-cli.
	Call(ctx context.Context, method string, params any) (*vibekit.RPCResponse, error)
}

// sessionCaller is the commonest shape on this path: one call, addressed to the
// bridge's own session. 2 of 12.
type sessionCaller interface {
	bridgeCaller
	sessionScoped
}

// bridgeRPC is the full JSON-RPC surface — request, notification, and the
// answer to an inbound request from kiro-cli. 4 of 12. It carries no prompt-slot
// method on purpose: sending a frame and owning the turn are different rights,
// and the cancel path sends a notification without holding the slot.
type bridgeRPC interface {
	sessionCaller

	// Notify sends a one-way notification to kiro-cli.
	Notify(ctx context.Context, method string, params any) error
	// Respond sends a permission response to kiro-cli.
	Respond(ctx context.Context, requestID int64, result any, err error) error
}

// promptSlot is the per-chat turn lock and its unresponsive-cancel budget. 6 of
// 12, and they are one protocol rather than six methods: acquire the slot,
// register the in-flight call's cancel func against a turn GENERATION, arm the
// grace budget for that generation, then release. The generation is what stops
// an expired budget cancelling a turn that started after the one it was armed
// for — time.Timer.Stop does not halt an already-running func.
type promptSlot interface {
	// TryAcquireForPrompt attempts to lock the bridge for prompting.
	TryAcquireForPrompt() bool
	// ReleaseAfterPrompt releases the prompt lock.
	ReleaseAfterPrompt()
	// BeginPromptCall registers the cancel func of the in-flight prompt's
	// context and returns the turn generation it belongs to. Paired with
	// EndPromptCall in the prompt handler's defer.
	BeginPromptCall(cancel context.CancelFunc) uint64
	// EndPromptCall forgets the in-flight prompt's cancel func.
	EndPromptCall()
	// ArmCancelGrace starts the unresponsive-cancel budget: if the turn
	// identified by gen is still in flight after d, the prompt's context is
	// cancelled so the blocked Call returns and the slot is released.
	// Reports false if there was no in-flight prompt to arm against.
	ArmCancelGrace(gen uint64, d time.Duration) bool
	// PromptGeneration returns the current turn generation.
	PromptGeneration() uint64
}

// bridgePriming is whether this bridge's session has already been given the
// chat's transcript. 2 of 12, read and written only by the prompt path.
type bridgePriming interface {
	// IsPrimed reports whether the bridge has been primed.
	IsPrimed() bool
	// SetPrimed marks the bridge as primed.
	SetPrimed()
}

// Bridge is the per-chat ACP bridge as a whole: all 12 methods, composed from
// the three seams above. Only a handler that owns a chat for the length of a
// turn needs it — the helpers take a narrower parameter.
//
// Exported because BridgeAccess returns it and hub's dispatcher wiring names it.
type Bridge interface {
	bridgeRPC
	promptSlot
	bridgePriming
}

// BridgeAccess provides bridge lifecycle operations needed by prompt,
// cancel, subagent, slash, and permission handlers.
type BridgeAccess interface {
	GetBridge(chatID vibekit.ChatID) Bridge
	GetOrCreateBridge(ctx context.Context, chatID vibekit.ChatID, model string) (Bridge, error)
	CloseBridge(chatID vibekit.ChatID)
	PrimeIfNeeded(ctx context.Context, chatID vibekit.ChatID, b Bridge)
	// PrimeFromChat notes that chatID's FIRST session should be primed with
	// another chat's transcript. The tangent's fallback (command/fork.go): a
	// refused session/fork leaves the new chat with no inherited session, so the
	// parent's history has to reach the model some other way.
	//
	// A note rather than a field on the chat record, because it describes one
	// session's launch and not the chat: it is consumed by the next spawn and
	// does not survive a restart, which is correct — by then the tangent has its
	// own conversation and is owed nothing from its parent.
	PrimeFromChat(chatID, sourceChatID vibekit.ChatID)
}

// ChatStore is the chat store as the command handlers use it: read a chat,
// mutate it, append a message, record a draft, delete it. 5 of the 9 methods
// *chat.Store offers.
//
// The 4 it excludes are each a capability a command handler must not have. It
// cannot List (that is the chats index endpoint's read, and a dispatcher
// enumerating every chat is a dispatcher that can leak one into another's
// response), it cannot BuildHistory (priming is the bridge coordinator's),
// it cannot UpdateMessage, and it cannot RegisterRoutes.
//
// Exported because ChatAccess is exported and *hub.Hub has to name this as its
// ChatStore() return type. internal/translate declares its own narrower
// ChatRecords for the same store — see its deps.go for why the two accessors
// cannot share one name.
type ChatStore interface {
	// Get returns the full chat at id, or false if it does not exist.
	Get(ctx context.Context, id vibekit.ChatID) (*vibekit.Chat, bool)
	// Mutate is the single write primitive: load, apply, save, broadcast
	// chat_created / chat_updated.
	Mutate(ctx context.Context, id vibekit.ChatID, mutate func(c *vibekit.Chat, exists bool) bool) error
	// AppendMessage appends msg to the chat's messages.
	AppendMessage(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error
	// SetDraft persists the chat's unsent composer text. Its own method rather
	// than a Mutate call because a draft save is not ACTIVITY and must not be
	// recorded as any: Mutate stamps UpdatedAt, the retention purge ages a chat
	// from exactly that field, and a debounced autosave would push the cutoff
	// out by a whole window each keystroke. A no-op for a chat that does not
	// exist — typing must not create one.
	SetDraft(ctx context.Context, id vibekit.ChatID, text string) error
	// Delete removes the chat file and broadcasts chat_deleted. cmdDeleteChat
	// is the ONLY caller in the build, and that is an invariant: bridge exits,
	// model switches and restarts never delete.
	Delete(ctx context.Context, id vibekit.ChatID) error
}

// ChatAccess provides chat store and broadcast operations needed by
// create, delete, rewind, and supervised-mode handlers.
type ChatAccess interface {
	ChatStore() ChatStore
	Broadcast(ctx context.Context, evt vibekit.ServerEvent)
	CleanupChatState(ctx context.Context, chatID vibekit.ChatID)
	// CloseChatState is the close path's teardown: same in-memory cleanup,
	// but it leaves the chat's durable KAS session on disk so the chat can be
	// reopened and so History can still list it.
	CloseChatState(ctx context.Context, chatID vibekit.ChatID)
	// CancelChatRuns cancels every non-terminal workflow run this chat's
	// sessions launched. Part of the tab-close contract: closing the tab kills
	// the work, and a run is durable state a dead process does NOT stop — KAS
	// reconciles it to paused and a later read revives it, so it must be told
	// to cancel, per run, before the bridge goes.
	CancelChatRuns(ctx context.Context, chatID vibekit.ChatID)
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
	ClearPendingPermsForChat(chatID vibekit.ChatID)
	// TakePendingPerm claims the request before its answer is sent, and reports
	// false when another surface already answered it. It replaced a
	// remove-after-responding call: two tabs could each answer one request, and
	// the agent server discards the loser silently, so a handler must take the
	// request FIRST and give up when the take fails.
	//
	// settledBy travels with the claim because the winning take is broadcast to
	// the surfaces that lost, and their card says who answered. From a command
	// handler that is always vibekit.SettledByUser — a person clicked something.
	TakePendingPerm(requestID int64, settledBy vibekit.SettledBy) bool
}

// TerminalAccess is the interrupt's process half: a turn cancel must reach
// the turn's agent terminals or it strands them (§5.6 R3 — cancelling
// mid-`npm test` left the command running with no owner).
type TerminalAccess interface {
	// KillTurnTerminals kills the terminals the chat's CURRENT turn created,
	// and nothing else — a background command an earlier turn left running
	// on purpose is not the cancel's to kill.
	KillTurnTerminals(chatID vibekit.ChatID)
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
	IsEmptyTurn(resp *vibekit.RPCResponse, chatID vibekit.ChatID) bool
	EmitTurnEndedWithStats(ctx context.Context, chatID vibekit.ChatID, resp *vibekit.RPCResponse, stats TurnStats)
	AbandonInFlightTurn(ctx context.Context, chatID vibekit.ChatID)
	// LatchTurnModel records which model this turn was DISPATCHED under, before
	// the prompt call can race a concurrent switch_model. First write wins, so
	// the value is immutable for the turn's lifetime.
	//
	// It exists because the turn buffer otherwise latches on the first assistant
	// FRAME, which can be seconds later: a fast in-session switch landing in that
	// window stamped the new model onto an answer the previous one produced.
	LatchTurnModel(chatID vibekit.ChatID, model string)
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
