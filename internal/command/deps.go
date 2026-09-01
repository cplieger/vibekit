package command

import (
	"context"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/vibekit/internal/workspace"
)

// This file is the package's dependency contracts: the role interfaces each
// handler names in its own signature, and the per-chat bridge interfaces the
// handlers call. Each is shaped by what this package invokes, which is what
// keeps a test stub small enough to be obviously correct.
//
// No composite over these roles exists here: a handler's reach is its
// signature, so widening one is a diff at its declaration.

// sessionScoped names the ACP session an RPC is addressed to.
type sessionScoped interface {
	// SessionID returns the current ACP session ID.
	SessionID() vibekit.SessionID
}

// bridgeCaller sends one request to kiro-cli and waits for its answer.
type bridgeCaller interface {
	// Call sends an RPC call to kiro-cli.
	Call(ctx context.Context, method string, params any) (*vibekit.RPCResponse, error)
	// CallAt is Call plus the read loop position at which the response
	// arrived: notifications queue on a buffered channel while a response
	// goes straight to the waiting Call, so the wire's own turn_end is
	// routinely still unread when the response lands.
	CallAt(ctx context.Context, method string, params any) (*vibekit.RPCResponse, uint64, error)
}

// sessionCaller is the commonest shape on this path: one call, addressed to
// the bridge's own session.
type sessionCaller interface {
	bridgeCaller
	sessionScoped
}

// bridgeRPC is the full JSON-RPC surface — request, notification, and the
// answer to an inbound request from kiro-cli. Carries no prompt-slot method:
// sending a frame and owning the turn are different rights, and the cancel
// path sends a notification without holding the slot.
type bridgeRPC interface {
	sessionCaller

	// Notify sends a one-way notification to kiro-cli.
	Notify(ctx context.Context, method string, params any) error
	// Respond sends a permission response to kiro-cli.
	Respond(ctx context.Context, requestID int64, result any, err error) error
}

// promptSlot is the per-chat turn lock and its unresponsive-cancel budget:
// acquire the slot, register the in-flight call's cancel func against a
// turn generation, arm the grace budget for that generation, then release.
// The generation is what stops an expired budget cancelling a turn that
// started after the one it was armed for — time.Timer.Stop does not halt an
// already-running func.
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

// Bridge is the per-chat ACP bridge as a whole. Only a handler that owns a
// chat for the length of a turn needs it — helpers take a narrower
// parameter. Priming is deliberately not here: that is a bridge-lifecycle
// question PrimeIfNeeded already answers.
//
// Exported because BridgeAccess returns it and runtime's dispatcher wiring names it.
type Bridge interface {
	bridgeRPC
	promptSlot
}

// BridgeAccess provides bridge lifecycle operations needed by prompt,
// cancel, subagent, slash, and permission handlers.
type BridgeAccess interface {
	Bridge(chatID vibekit.ChatID) Bridge
	OpenBridge(ctx context.Context, chatID vibekit.ChatID, model string) (Bridge, error)
	CloseBridge(chatID vibekit.ChatID)
	// PrimeIfNeeded gives the chat's current session its transcript, if that
	// session has not had it yet.
	PrimeIfNeeded(ctx context.Context, chatID vibekit.ChatID)
	// PrimeFromChat notes that chatID's first session should be primed with
	// another chat's transcript — the tangent's fallback when a refused
	// session/fork leaves the new chat with no inherited session.
	//
	// A note rather than a chat-record field: it describes one session's
	// launch, is consumed by the next spawn, and does not survive a restart.
	PrimeFromChat(chatID, sourceChatID vibekit.ChatID)
}

// ChatStore is the chat store as the command handlers use it: read a chat,
// mutate it, append a message, record a draft and its attachments, delete
// it. It excludes List, BuildHistory, UpsertTurnPlan, UpdateMessage and
// RegisterRoutes — each a capability a command handler must not have.
//
// Exported because ChatAccess is exported and *agent.Runtime names this;
// internal/translate declares its own narrower ChatRecords for the same
// store.
type ChatStore interface {
	// Get returns the full chat at id, or false if it does not exist.
	Get(ctx context.Context, id vibekit.ChatID) (*vibekit.Chat, bool)
	// Mutate is the single write primitive: load, apply, save, broadcast
	// chat_created / chat_updated.
	Mutate(ctx context.Context, id vibekit.ChatID, mutate func(c *vibekit.Chat, exists bool) bool) error
	// AppendMessage appends msg to the chat's messages.
	AppendMessage(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error
	// SetDraft persists the chat's unsent composer text. Its own method
	// rather than a Mutate call because a draft save is not activity: Mutate
	// stamps UpdatedAt, and the retention purge ages a chat from that field.
	// A no-op for a chat that does not exist — typing must not create one.
	//
	// The returned state is what landed, nil when nothing did (no record, or
	// the same text already stored) — the draft_changed broadcast keys on it.
	SetDraft(ctx context.Context, id vibekit.ChatID, text string) (*vibekit.ComposerState, error)
	// SetAttachments persists the paths staged beside the draft, replacing
	// the list. The draft's twin, with the same no-UpdatedAt and no-op-on-
	// missing-record contracts.
	SetAttachments(ctx context.Context, id vibekit.ChatID, paths []string) (*vibekit.ComposerState, error)
	// Delete removes the chat file and broadcasts chat_deleted. cmdDeleteChat
	// is the only caller in the build: bridge exits, model switches and
	// restarts never delete.
	Delete(ctx context.Context, id vibekit.ChatID) error
}

// Broadcaster publishes a domain event to every connected client.
type Broadcaster interface {
	Broadcast(ctx context.Context, evt vibekit.ServerEvent)
}

// ChatTeardown ends a chat's life, in one of two ways. Cancelling the chat's
// runs is inside both, not a separate step a caller pairs with them: a run
// is durable state a dead bridge only pauses (KAS reconciles it to paused
// and a later read revives it), so it must be told to cancel before the
// bridge goes.
type ChatTeardown interface {
	// DeleteChatState is the delete path: cancel the chat's runs, drop every
	// in-memory trace, and reap the durable KAS session too. Resolves runs
	// and sessions off the chat record, so it belongs to the record-first
	// delete, where the teardown runs while the record still exists.
	DeleteChatState(ctx context.Context, chatID vibekit.ChatID)
	// DeleteChatStateByChain is the delete grade for a chat whose record is
	// already gone: the close escalation removes the record inside its
	// commit, so the run cancel and the KAS reap are driven from the session
	// chain captured before it.
	DeleteChatStateByChain(ctx context.Context, chatID vibekit.ChatID, sessionChain []string)
	// CloseChatState is the close path: the same cancel and in-memory
	// cleanup, but it leaves the durable KAS session on disk so the chat can
	// be reopened and so History can still list it.
	CloseChatState(ctx context.Context, chatID vibekit.ChatID)
}

// PendingPermAccess provides the pending-permission bookkeeping handlers
// need: an unanswered request is replayed to a reconnecting client, so
// answering or abandoning one has to retire the entry.
type PendingPermAccess interface {
	ClearPendingPermsForChat(chatID vibekit.ChatID)
	// TakePendingPerm claims the request before its answer is sent, and
	// reports false when another surface already answered it: two tabs
	// could each answer one request, and the agent server discards the
	// loser silently.
	//
	// settledBy travels with the claim because the winning take is
	// broadcast to the surfaces that lost, and their card says who
	// answered — from a command handler always vibekit.SettledByUser.
	//
	// The chat is part of the claim because a request id is unique only
	// within one bridge: every bridge mints ids from zero, so two chats
	// routinely hold the same id.
	TakePendingPerm(chatID vibekit.ChatID, requestID int64, settledBy vibekit.SettledBy) bool
}

// TerminalAccess is the interrupt's process half: a turn cancel must reach
// the turn's agent terminals or it strands them.
type TerminalAccess interface {
	// KillTurnTerminals kills the terminals the chat's current turn
	// created, and nothing else — a background command an earlier turn
	// left running on purpose is not the cancel's to kill.
	KillForTurn(chatID vibekit.ChatID)
}

// Workspace carries the two paths handlers resolve against: the hook writer
// and the shell spawn need the working directory, the prompt reads a
// setting out of the config directory, and an attachment path is confined
// to the workspace before it is read.
type Workspace struct {
	// Dir is the workspace root every relative path resolves against.
	Dir string
	// ConfigDir is where settings and hook files live.
	ConfigDir string
}

// ResolveInside confines rel to the workspace root, refusing anything that
// escapes it.
func (w Workspace) ResolveInside(rel string) (string, error) {
	return workspace.ResolveInsideAbs(w.Dir, rel)
}

// LifecycleAccess is the process-lifetime seam: the context a turn runs
// under, and the in-flight accounting that makes agent shutdown wait for
// it. A handler that starts long-lived work takes a turn context,
// registers itself as in-flight, and deregisters on return; shutdown
// cancels the first and waits on the second.
type LifecycleAccess interface {
	// TurnContext returns the context an in-flight turn runs under, plus
	// the teardown its handler defers.
	TurnContext(reqCtx context.Context) (context.Context, context.CancelFunc)
	InflightAdd(delta int)
	InflightDone()
}

// MCPAccess is the MCP readiness gate a prompt waits on, so a first turn
// does not reach the model before the workspace's MCP servers have
// connected, plus the account of what the wait was waiting for when it
// expires.
type MCPAccess interface {
	WaitForReady(ctx context.Context, timeout time.Duration) bool
	// PendingSummary names the servers a readiness wait is still short of.
	// Read only when the wait expires — a diagnostic, not part of the
	// decision.
	PendingSummary(ctx context.Context) MCPPendingSummary
}

// MCPPendingSummary is the three buckets a readiness timeout can be about,
// each wanting a different operator action.
type MCPPendingSummary struct {
	// Silent is the enabled servers that reported no terminal state
	// (including one still connecting, deliberately not recorded).
	Silent []string
	// Failed is the servers that reported a failure, each with the
	// upstream error text.
	Failed []string
	// AwaitingAuth is the servers waiting for an authorization nobody has
	// completed.
	AwaitingAuth []string
}

// TokenSource is the vended KAS credential as this package uses it: one
// method, called when the backend rejects the token vibekit successfully
// vended.
type TokenSource interface {
	// Invalidate withdraws the cached credential from the reuse window, so
	// the next vend re-asks the CLI.
	Invalidate()
}

// AdmissionOutcome is ReserveTurnForPrompt's answer.
type AdmissionOutcome int

const (
	// AdmissionAcquired means the caller holds the chat's admission slot.
	AdmissionAcquired AdmissionOutcome = iota
	// AdmissionBusy is the holder a steer can reach: a prompt-class turn
	// with a live bridge. Answered as the plain 409, on which the client's
	// 409→steer conversion works.
	AdmissionBusy
	// AdmissionStarting is every other holder — a cold spawn, a shell on a
	// bridged or bridgeless chat, a prime. Answered as 409 with the
	// additive `reason: "starting"`.
	AdmissionStarting
)

// TurnOutcomeAccess is what a prompt handler needs to run a turn's
// lifecycle: admit it, open it, wait for its outcome, and close it —
// whether the engine answered or the call failed.
type TurnOutcomeAccess interface {
	// ReserveTurnForPrompt takes the chat's admission slot for a prompt,
	// minting no turn — a bare per-chat reservation, decided synchronously
	// before any bridge exists. A held slot parks the caller up to wait;
	// the refusal is keyed on the holder's source — see AdmissionOutcome.
	ReserveTurnForPrompt(ctx context.Context, chatID vibekit.ChatID, wait time.Duration) AdmissionOutcome
	// TryReserveTurn takes the admission slot iff it is free — the shell
	// door's form (a `!cmd` during any held slot refuses immediately) and
	// the empty-turn recovery's (a competing prompt that won the slot
	// abandons the retry).
	TryReserveTurn(chatID vibekit.ChatID, source vibekit.TurnOpenSource) bool
	// ReleaseTurnReservation frees the admission slot, waking every
	// waiter.
	ReleaseTurnReservation(chatID vibekit.ChatID)
	// AdmissionHolderSource reports who holds the chat's admission: the
	// open turn's source when one is open, else the reservation's.
	//
	// A prime holder (TurnSourcePrime) matters because a prime is
	// vibekit's own transcript replay sent as a real session/prompt, and
	// its frames are neither broadcast nor served nor persisted — a steer
	// aimed into that window would be silently discarded, so CmdSteer
	// refuses instead.
	AdmissionHolderSource(chatID vibekit.ChatID) (vibekit.TurnOpenSource, bool)
	// StartTurn opens the chat's turn at bridge-ready, immediately before
	// the call that drives it, so everything true of the turn is recorded
	// once for its whole life, with the bridge live. The caller holds a
	// completion handle until ReleaseTurn; zero means none opened, and a
	// caller answering zero must broadcast the failure and release its
	// slots rather than proceed.
	//
	// It waits while the chat is finalizing a previous turn, so a prompt
	// sent during that turn's persistence does not observe an epoch as
	// open too early.
	StartTurn(ctx context.Context, chatID vibekit.ChatID, source vibekit.TurnOpenSource) vibekit.TurnEpoch
	// AwaitTurn blocks until the named turn has finalized and reports what
	// it did. A caller holding that turn's handle never receives
	// vibekit.ErrNoSuchTurn.
	AwaitTurn(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch) (vibekit.TurnResult, error)
	// ReleaseTurn gives up the handle StartTurn issued, after which the
	// finalized record may be dropped.
	ReleaseTurn(chatID vibekit.ChatID, epoch vibekit.TurnEpoch)
	// SettleTurnOnResponse closes the named turn on the response that
	// settled it — the local fallback, which runs only if the wire's own
	// turn_end did not get there first.
	//
	// seq is the read loop position the response arrived at, and the
	// settle parks until the folder reaches it.
	SettleTurnOnResponse(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch, seq uint64, resp *vibekit.RPCResponse)
	// TurnOpenedAfter reports whether any turn on the chat opened after
	// epoch — the structural half of the empty-turn gate.
	TurnOpenedAfter(chatID vibekit.ChatID, epoch vibekit.TurnEpoch) bool
	// FinalizeLocalShellTurn closes a `!cmd` turn vibekit ran itself.
	FinalizeLocalShellTurn(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch)
	// AbandonInFlightTurn finalizes a turn the prompt call could not
	// finish. reason is the user-facing account of the failure and becomes
	// the transcript's interrupted divider.
	//
	// It waits for no read loop position: the two failures that reach it —
	// an oversize frame and a cancel-grace expiry — settle locally with
	// the bridge and the KAS execution still alive.
	AbandonInFlightTurn(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch, reason string)
}

// Roles is the wiring-time role set: the host names which of its
// interfaces answers each role once, at registration, and
// RegisterDefaults hands every handler only the roles that handler's
// signature declares.
//
// A plain struct rather than an interface, deliberately: no handler,
// helper or constructor takes this type, so nothing in the package widens
// as the host grows.
//
// Taken by pointer wherever it travels.
type Roles struct {
	Bridges BridgeAccess
	// Chats is the chat store directly, not through a getter on a wider
	// composite — the composite would need to also carry Broadcast and the
	// run surface, which is how a host becomes the one thing that
	// qualifies.
	Chats     ChatStore
	Bus       Broadcaster
	Teardown  ChatTeardown
	Perms     PendingPermAccess
	Terminals TerminalAccess
	// Tabs is the open-tab set, and it may be nil: a build with no config
	// dir has no store to persist an arrangement to. The coordinator
	// answers the tab half of every operation with a 503 in that state.
	Tabs TabSet
	// Lifecycle is the process lifetime: the turn context and the
	// in-flight counter a shutdown waits on.
	Lifecycle   LifecycleAccess
	MCP         MCPAccess
	TurnOutcome TurnOutcomeAccess
	// Tokens is the KAS credential cache, and it may also be nil: a
	// runtime built without WithKiroCLIPath vends no token at all.
	Tokens TokenSource
	// Workspace is last for fieldalignment (a trailing length word stops
	// the leading-pointer count early).
	Workspace Workspace
}

// promptRoles is the prompt path's six roles, threaded through the shell
// interception as well. Passed by pointer, built once at registration.
type promptRoles struct {
	bridges BridgeAccess
	chats   ChatStore
	// bus is separate from chats because they are separate owners.
	bus         Broadcaster
	lifecycle   LifecycleAccess
	mcp         MCPAccess
	turnOutcome TurnOutcomeAccess
	// tokens may be nil, as in Roles.
	tokens    TokenSource
	workspace Workspace // last for fieldalignment, as in Roles
}
