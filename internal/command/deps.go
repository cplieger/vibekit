package command

import (
	"context"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/vibekit/internal/workspace"
)

// This file is the package's dependency contracts: the role interfaces each
// handler names in its own signature, and the per-chat bridge interfaces the
// handlers call. Each is shaped by what THIS package invokes, which is what
// keeps a test stub small enough to be obviously correct.
//
// The package declares no composite over these roles, and must not: a handler's
// reach is its signature, so widening one is a diff at its declaration rather
// than a consequence of the host growing a method.

// The bridge interfaces below are declared HERE, at the consumer, rather than in
// a shared contract package. *agent.sharedBridge is what satisfies them, and the
// check is forced where agent calls SessionParams(sb, …) rather than by an
// assertion anybody has to remember to write.
//
// The width arithmetic runs from the inside out: SessionParams needs 1 of the
// bridge's 10 methods, the prompt retry needs 1, a rewind needs 2, and only a
// handler that holds the bridge for a whole turn needs all 10. A single Bridge
// parameter on every one of those made each helper claim the whole surface, so
// the seams are named and each signature takes the narrowest that fits.

// sessionScoped names the ACP session an RPC is addressed to. 1 of the 10
// methods the bridge offers, and the only one SessionParams — which every RPC
// helper on this path funnels through — has any use for.
type sessionScoped interface {
	// SessionID returns the current ACP session ID.
	SessionID() vibekit.SessionID
}

// bridgeCaller sends one request to kiro-cli and waits for its answer. 1 of 10:
// the prompt retry loop re-invokes exactly this and nothing else, so a wider
// parameter would let a future edit reach the prompt slot from inside a retry.
type bridgeCaller interface {
	// Call sends an RPC call to kiro-cli.
	Call(ctx context.Context, method string, params any) (*vibekit.RPCResponse, error)
	// CallAt is Call plus the read loop position at which the response arrived. A
	// prompt cannot be settled from its response alone: notifications queue on a
	// buffered channel while a response goes straight to the waiting Call, so the
	// wire's own turn_end is routinely still unread when the response lands.
	CallAt(ctx context.Context, method string, params any) (*vibekit.RPCResponse, uint64, error)
}

// sessionCaller is the commonest shape on this path: one call, addressed to the
// bridge's own session. 2 of 10.
type sessionCaller interface {
	bridgeCaller
	sessionScoped
}

// bridgeRPC is the full JSON-RPC surface — request, notification, and the
// answer to an inbound request from kiro-cli. 4 of 10. It carries no prompt-slot
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
// 10, and they are one protocol rather than six methods: acquire the slot,
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

// Bridge is the per-chat ACP bridge as a whole: all 10 methods, composed from
// the two seams above. Only a handler that owns a chat for the length of a turn
// needs it — the helpers take a narrower parameter.
//
// Priming is deliberately NOT here. It used to carry IsPrimed and SetPrimed so
// the prompt path could run check-then-set-then-prime itself, which meant this
// package decided when a session gets its transcript — a bridge-lifecycle
// question it has no other stake in, and one PrimeIfNeeded's own name already
// claims to answer.
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
	// session has not had it yet. It takes no Bridge: it looks the bridge up
	// itself, which is what lets the whole primed-or-not decision live in one
	// place. Passing one back in forced a type assertion on the far side (the
	// concrete bridge round-tripping through this interface) whose failure branch
	// could only log.
	PrimeIfNeeded(ctx context.Context, chatID vibekit.ChatID)
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
// mutate it, append a message, record a draft and its attachments, delete it.
// 6 of the 11 methods *chat.Store offers.
//
// The 5 it excludes are each a capability a command handler must not have. It
// cannot List (that is the chats index endpoint's read, and a dispatcher
// enumerating every chat is a dispatcher that can leak one into another's
// response), it cannot BuildHistory (priming is the bridge coordinator's), it
// cannot UpsertTurnPlan (a plan row is the translator's, written from an ACP
// frame rather than from a command), it cannot UpdateMessage, and it cannot
// RegisterRoutes.
//
// Exported because ChatAccess is exported and *agent.Runtime has to name this as its
// internal/translate declares its own narrower
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
	//
	// The returned state is what landed, and nil when nothing did (no record, or
	// the same text already stored), which is what the draft_changed broadcast
	// keys on.
	SetDraft(ctx context.Context, id vibekit.ChatID, text string) (*vibekit.ComposerState, error)
	// SetAttachments persists the paths staged beside the draft, replacing the
	// list. The draft's twin, with the same two contracts for the same two
	// reasons: no UpdatedAt stamp, and no record means no-op.
	SetAttachments(ctx context.Context, id vibekit.ChatID, paths []string) (*vibekit.ComposerState, error)
	// Delete removes the chat file and broadcasts chat_deleted. cmdDeleteChat
	// is the ONLY caller in the build, and that is an invariant: bridge exits,
	// model switches and restarts never delete.
	Delete(ctx context.Context, id vibekit.ChatID) error
}

// Broadcaster publishes a domain event to every connected client.
type Broadcaster interface {
	Broadcast(ctx context.Context, evt vibekit.ServerEvent)
}

// ChatTeardown ends a chat's life, in one of two ways. It stays a composite
// because both members genuinely span the host: each reaches the pending-decision
// tracker, the bridge coordinator, the terminal registry, the assistant buffers,
// the line tracker AND the run surface, so there is no single owner to name.
//
// Cancelling the chat's runs is INSIDE both, not a third role a caller pairs with
// them. Both call sites did that pairing by hand, in the same order, for the same
// reason: a run is durable state a dead bridge only PAUSES — KAS reconciles it to
// paused and a later read revives it — so it must be told to cancel, per run,
// before the bridge goes. A contract two callers have to remember is one a third
// caller will forget.
type ChatTeardown interface {
	// DeleteChatState is the delete path: cancel the chat's runs, drop every
	// in-memory trace, and reap the durable KAS session too.
	DeleteChatState(ctx context.Context, chatID vibekit.ChatID)
	// CloseChatState is the close path: the same cancel and in-memory cleanup,
	// but it LEAVES the durable KAS session on disk so the chat can be reopened
	// and so History can still list it.
	CloseChatState(ctx context.Context, chatID vibekit.ChatID)
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
	//
	// The CHAT is part of the claim because a request id is unique only within one
	// bridge: every bridge mints ids from zero, so two chats routinely hold the
	// same id, and an id-only claim let one chat's answer retire the other's card.
	// Every caller here has cmd.ChatID.
	TakePendingPerm(chatID vibekit.ChatID, requestID int64, settledBy vibekit.SettledBy) bool
}

// TerminalAccess is the interrupt's process half: a turn cancel must reach
// the turn's agent terminals or it strands them (§5.6 R3 — cancelling
// mid-`npm test` left the command running with no owner).
type TerminalAccess interface {
	// KillTurnTerminals kills the terminals the chat's CURRENT turn created,
	// and nothing else — a background command an earlier turn left running
	// on purpose is not the cancel's to kill.
	KillForTurn(chatID vibekit.ChatID)
}

// Workspace carries the two paths handlers resolve against: the hook writer and
// the shell spawn need the working directory, the prompt reads a setting out of
// the config directory, and an attachment path is confined to the workspace
// before it is read.
//
// It is a VALUE, not an interface, and that is the point. Two of its three former
// methods returned process constants and the third was one call into
// internal/workspace, so there was nothing to substitute and never a second
// implementation — an interface there abstracts a string. Its three methods on
// Runtime went with it, which is three fewer on a receiver that has too many.
type Workspace struct {
	// Dir is the workspace root every relative path resolves against.
	Dir string
	// ConfigDir is where settings and hook files live.
	ConfigDir string
}

// ResolveInside confines rel to the workspace root, refusing anything that
// escapes it. Passed as a function value to BuildPromptBlocks, which is why it is
// a method rather than something a caller composes: it is a path rule, and there
// is exactly one root to apply it against.
func (w Workspace) ResolveInside(rel string) (string, error) {
	return workspace.ResolveInsideAbs(w.Dir, rel)
}

// LifecycleAccess is the process-lifetime seam: the context a turn runs under,
// and the in-flight accounting that makes agent shutdown wait for it.
//
// The three belong together because they are one protocol. A handler that starts
// long-lived work takes a turn context, registers itself as in-flight, and
// deregisters on return; shutdown cancels the first and waits on the second.
type LifecycleAccess interface {
	// TurnContext returns the context an in-flight turn runs under, plus the
	// teardown its handler defers. It replaced a ShutdownCtx() accessor that
	// handed out the runtime's raw lifetime context and left every consumer to
	// derive a turn context correctly; the one consumer wanted the derivation,
	// not the lifetime.
	TurnContext(reqCtx context.Context) (context.Context, context.CancelFunc)
	InflightAdd(delta int)
	InflightDone()
}

// MCPAccess is the MCP readiness gate a prompt waits on, so a first turn does
// not reach the model before the workspace's MCP servers have connected, plus
// the account of what the wait was waiting FOR when it expires.
type MCPAccess interface {
	WaitForReady(ctx context.Context, timeout time.Duration) bool
	// PendingSummary names the servers a readiness wait is still short of. Read
	// only when the wait EXPIRES: it is a diagnostic, not part of the decision.
	PendingSummary(ctx context.Context) MCPPendingSummary
}

// MCPPendingSummary is the three buckets a readiness timeout can be about, and
// they want three different operator actions: a server that has said nothing at
// all, one that reported a failure, and one waiting for a human to authorize it.
//
// A struct rather than three adjacent []string returns, because the three are
// indistinguishable by type and the only consumer stamps them onto a log line as
// separate attributes — a transposition would mislabel every bucket and read as
// perfectly plausible.
//
// Declared here, at the consumer, like every other role in this file: the
// producer is the MCP registry in another package, and the vocabulary belongs to
// the surface that reads it.
type MCPPendingSummary struct {
	// Silent is the enabled servers that reported no terminal state. A server
	// still CONNECTING is in here too — that state is deliberately not recorded —
	// which is the honest answer for a readiness timeout and is why the attribute
	// says silent rather than absent.
	Silent []string
	// Failed is the servers that reported a failure, each with the upstream
	// error text.
	Failed []string
	// AwaitingAuth is the servers waiting for an authorization nobody has
	// completed. The one bucket whose remedy is a click rather than a repair.
	AwaitingAuth []string
}

// TokenSource is the vended KAS credential as this package uses it: one method,
// called when the backend rejects the token vibekit successfully vended.
//
// Declared here, at the consumer, like every other role in this file. The
// implementation is kiroauth.CLISource, constructed inside internal/agent, so the
// prompt path reaches it through this role rather than importing it.
type TokenSource interface {
	// Invalidate withdraws the cached credential from the reuse window, so the
	// next vend re-asks the CLI. It keeps the cached value as the CLI-failure
	// fallback, so an unnecessary call costs one subprocess and nothing else.
	Invalidate()
}

// TurnOutcomeAccess is what a prompt handler needs to run a turn's lifecycle:
// open it, wait for its outcome, and close it — whether the engine answered or
// the call failed.
type TurnOutcomeAccess interface {
	// OpenTurn opens the chat's turn before the call that drives it, so everything
	// true of the turn — the model answering, the credit reading its spend is
	// measured against, when it began — is recorded once for its whole life. The
	// caller holds a completion handle until ReleaseTurn; zero means none opened.
	//
	// It WAITS while the chat is finalizing a previous turn, so a prompt sent during
	// that turn's persistence does not observe an epoch as open too early.
	OpenTurn(ctx context.Context, chatID vibekit.ChatID, source vibekit.TurnOpenSource) vibekit.TurnEpoch
	// AwaitTurn blocks until the named turn has finalized and reports what it
	// did. A caller holding that turn's handle never receives
	// vibekit.ErrNoSuchTurn.
	AwaitTurn(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch) (vibekit.TurnResult, error)
	// ReleaseTurn gives up the handle OpenTurn issued, after which the finalized
	// record may be dropped.
	ReleaseTurn(chatID vibekit.ChatID, epoch vibekit.TurnEpoch)
	// SettleTurnOnResponse closes the named turn on the response that settled it —
	// the LOCAL fallback, which runs only if the wire's own turn_end did not get
	// there first.
	//
	// seq is the read loop position the response arrived at, and the settle parks
	// until the folder reaches it. Without that wait the fallback decides the wire
	// never closed this turn while the wire's turn_end sits queued behind the
	// response.
	SettleTurnOnResponse(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch, seq uint64, resp *vibekit.RPCResponse)
	// TurnOpenedAfter reports whether any turn on the chat opened after epoch.
	//
	// The structural half of the empty-turn gate, and the only clause that depends on
	// no frame arriving: a mis-bound pre-open closes with the firing condition
	// satisfied, and what it necessarily violates is a LATER epoch on this chat.
	TurnOpenedAfter(chatID vibekit.ChatID, epoch vibekit.TurnEpoch) bool
	// PrimeTurnOpen reports whether the chat's open turn is a PRIME.
	//
	// A prime is vibekit's own transcript replay sent as a real session/prompt, and
	// its frames are neither broadcast nor served nor persisted — so a steer aimed
	// into that window was consumed by a throwaway turn and discarded, which is user
	// data loss with nothing on screen to say so. CmdSteer refuses instead, and the
	// client's rollback for a refused send returns the text to the composer.
	PrimeTurnOpen(chatID vibekit.ChatID) bool
	// FinalizeLocalShellTurn closes a `!cmd` turn vibekit ran itself.
	FinalizeLocalShellTurn(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch)
	// AbandonInFlightTurn finalizes a turn the prompt call could not finish.
	// `reason` is the user-facing account of the failure, and it becomes the
	// transcript's interrupted divider, so the caller renders its prose FIRST and
	// hands the same string here that it broadcasts and returns.
	//
	// It waits for no read loop position. The two failures that reach it — an
	// oversize frame and a cancel-grace expiry — settle locally with the bridge and
	// the KAS execution still alive, so no bracket is coming.
	AbandonInFlightTurn(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch, reason string)
}

// Roles is the wiring-time role set: the host names which of its interfaces
// answers each role once, at registration, and RegisterDefaults hands every
// handler only the roles that handler's signature declares.
//
// A plain struct rather than an interface, deliberately. No handler, helper or
// constructor takes this type, so nothing in the package widens as the host
// grows, and the compiler stops a handler reaching a role it was not given. It
// is the composition root's argument list with names on it; the alternative,
// eight positional parameters all satisfied by the same value, is the same
// wiring spelled worse.
//
// Taken by POINTER wherever it travels: eight interface fields is 128 bytes, and
// copying a wiring record per call buys nothing.
type Roles struct {
	Bridges BridgeAccess
	// Chats is the chat store directly. It used to arrive through a ChatStore()
	// GETTER on a ChatAccess composite, which was a second indirection for
	// nothing, and the composite also carried Broadcast and CancelForChat — so
	// only a type holding the store AND the bus AND the run surface could satisfy
	// it. That is how a host becomes the one thing that qualifies.
	Chats     ChatStore
	Bus       Broadcaster
	Teardown  ChatTeardown
	Perms     PendingPermAccess
	Terminals TerminalAccess
	// Tabs is the open-tab set, and it is the one role that may be NIL: a build
	// with no config dir has no store to persist an arrangement to. The
	// coordinator answers the tab half of every operation with a 503 in that
	// state rather than pretending it happened — the same shape internal/server
	// uses for an unwired ui-state store.
	Tabs TabSet
	// Lifecycle is the process lifetime: the turn context and the in-flight
	// counter a shutdown waits on.
	Lifecycle   LifecycleAccess
	MCP         MCPAccess
	TurnOutcome TurnOutcomeAccess
	// Tokens is the KAS credential cache, and it is the second role that may be
	// NIL: a runtime built without WithKiroCLIPath vends no token at all, so
	// there is nothing to invalidate. The prompt path skips the call in that
	// state rather than the host handing over a source that answers nothing.
	Tokens TokenSource
	// Workspace is last because it ends in a string: govet's fieldalignment
	// counts leading POINTER bytes, and a trailing length word stops that count
	// early. Field order here carries no other meaning.
	Workspace Workspace
}

// promptRoles is the prompt path's six roles. The one handler that takes a
// struct rather than plain parameters, because the same set is threaded through
// the shell interception as well, and six parameters ahead of ctx at each site
// is what would actually obscure which one needs what. Passed by pointer, built
// once at registration: 96 bytes is not a per-request copy worth making.
type promptRoles struct {
	bridges BridgeAccess
	chats   ChatStore
	// bus is separate from chats because they are separate owners. It arrived as
	// a Broadcast member on the old ChatAccess composite, which is what made the
	// chat store and the event bus inseparable to every consumer of it.
	bus         Broadcaster
	lifecycle   LifecycleAccess
	mcp         MCPAccess
	turnOutcome TurnOutcomeAccess
	// tokens may be nil, as in Roles.
	tokens    TokenSource
	workspace Workspace // last for fieldalignment, as in Roles
}
