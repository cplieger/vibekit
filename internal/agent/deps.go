package agent

import (
	"context"
	"net/http"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// The interfaces below are the runtime's DEPENDENCY contracts: what it calls on the
// collaborators the composition root hands it. Each is declared HERE, at the
// consumer, rather than in a shared contract package, and each names only the
// methods this package invokes — which is what keeps a test fake small enough to
// be obviously correct and stops an unrelated method from widening a contract
// every package in the build then has to import.
//
// The width arithmetic is stated per interface. Where the runtime genuinely uses all
// of what an implementation offers, that is said plainly: the placement is the
// win in that case, because a contract with one consumer has no business in a
// package everything imports.

// mcpNameSets is the MCP server-name census: which of the user's servers are
// switched on, which its config holds at all, and which are reachable through
// the config file including the powers block vibekit never writes. *mcp.Store
// satisfies it.
//
// 3 of the 3 methods the store exports for this, and the runtime needs all three
// because it reasons from their NESTING rather than from any one of them:
// enabled means record and tag OriginUser; configured-but-not-enabled is the
// only case that drops a frame; in AllNames but not configured can only have
// come from a Power; in none of them is a source vibekit cannot see. One set
// alone cannot separate "the user turned this off" from "vibekit never
// configured this", and those two need opposite treatment.
//
// AllNames is the only member that touches disk, which is why the classifier
// consults it last.
type mcpNameSets interface {
	// EnabledNames returns the set of enabled server names.
	EnabledNames(ctx context.Context) map[string]struct{}
	// ConfiguredNames returns every server name vibekit's OWN config holds,
	// enabled or disabled.
	ConfiguredNames(ctx context.Context) map[string]struct{}
	// AllNames returns every server name reachable through the config file
	// vibekit renders, including the powers block KAS reads out of it.
	// Best-effort: a hand-edit can make that file unparseable, so a name this
	// set misses is reported OriginUnknown rather than dropped.
	AllNames(ctx context.Context) map[string]struct{}
}

// RouteRegistrar is a component that mounts its own routes under a sub-tree of
// /api/*. It is the runtime's OUTPUT type, not a dependency: MCPRegistry hands the
// runtime MCP registry out as one so the composition root can pass it to the
// server without the registry's concrete type leaving this package.
//
// 1 method, which is the whole of it. internal/server declares an identical
// unexported routeHandler for the eight components it mounts; the two are
// separate declarations because neither package should name the other's.
// Exported here only because it is an exported method's return type.
type RouteRegistrar interface {
	RegisterRoutes(mux *http.ServeMux)
}

// bridgeChatRecords is the chat store as the BRIDGE LIFECYCLE uses it: read a
// chat, read its transcript to prime a fresh session, write the session metadata
// a spawn produced, and append the turn's messages. 4 of the 11 methods
// *chat.Store offers.
//
// Delete is deliberately absent, and that absence is an app invariant rather
// than a preference: only cmdDeleteChat may remove a chat file, so the path that
// tears bridges down on exit, model switch and restart must not be able to. A
// bridge exiting has never meant a chat should go, and now it cannot.
type bridgeChatRecords interface {
	// Get returns the full chat at id, or false if it does not exist.
	Get(ctx context.Context, id vibekit.ChatID) (*vibekit.Chat, bool)
	// BuildHistory returns a plain-text transcript used for compress priming.
	// Returns "" if the chat is missing or empty.
	BuildHistory(ctx context.Context, id vibekit.ChatID) string
	// Mutate is the single write primitive: load, apply, save, broadcast.
	Mutate(ctx context.Context, id vibekit.ChatID, mutate func(c *vibekit.Chat, exists bool) bool) error
	// AppendMessage appends msg to the chat's messages.
	AppendMessage(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error
}

// chatRecords is the runtime's field type, and it is a UNION rather than a usage
// claim. 9 of the 11 methods *chat.Store offers.
//
// The runtime itself calls 3 (Get, List, Mutate). It is 9 because the runtime is what
// the composition root hands the store to, and it passes narrower views of the
// same value on: bridgeChatRecords (4) to the coordinator, command.ChatStore (6)
// to the dispatcher, translate.ChatRecords (4) to the translator. A field has to
// satisfy every one of them.
//
// The 2 it does NOT carry: RegisterRoutes, which internal/server takes through
// its own routeHandler rather than this field, and UpdateMessage, which is called
// by nothing at all in production — see the note on (*chat.Store).UpdateMessage.
type chatRecords interface {
	bridgeChatRecords

	// List returns every chat's header (no messages) sorted by UpdatedAt
	// descending. Checks ctx.Err() between per-file reads.
	List(ctx context.Context) []vibekit.ChatHeader
	// SetDraft and SetAttachments persist the chat's parked composer state, one
	// half each. Passed on to the command dispatcher; the runtime never calls
	// either.
	SetDraft(ctx context.Context, id vibekit.ChatID, text string) (*vibekit.ComposerState, error)
	SetAttachments(ctx context.Context, id vibekit.ChatID, paths []string) (*vibekit.ComposerState, error)
	// Delete removes the chat file and broadcasts chat_deleted. Passed on to
	// the command dispatcher, whose cmdDeleteChat is the build's only caller;
	// the runtime never calls it, and the coordinator's own view cannot see it.
	Delete(ctx context.Context, id vibekit.ChatID) error
	// UpsertTurnPlan writes the turn's single plan row. Passed on to the
	// translator, whose HandlePlan is the build's only caller; the runtime
	// never calls it.
	UpsertTurnPlan(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error
}

// pushNotifier is the notification SEND half: ask whether anyone is listening,
// then send. *push.Service satisfies it.
//
// 2 of the 8 methods the push service offers, and what the bridge coordinator
// holds — the only thing it does with push is announce a finished turn. It
// cannot subscribe a device, rewrite the user's preferences or close the
// service, which is exactly right for a per-chat lifecycle path.
type pushNotifier interface {
	// HasSubscribers reports whether any device is registered.
	HasSubscribers() bool
	// Send delivers one notification to every subscriber.
	Send(ctx context.Context, title, body string, notifyType vibekit.PushKind, subject vibekit.PushSubject)
}

// pushService is the runtime's whole view of push: the send half plus the two
// lifecycle calls the runtime owns.
//
// 4 of 8. Subscribe and Unsubscribe are absent because nothing ever reached them
// through an interface — they are called on the concrete *push.Service by its own
// HTTP handlers — and SetPreferences belongs to the settings endpoint in
// internal/server, not here.
type pushService interface {
	pushNotifier

	// ReloadPreferences re-reads notification toggles from disk, deduplicating
	// concurrent calls via singleflight. Called on SSE reconnect so an
	// externally-edited config.json takes effect without a container restart.
	ReloadPreferences(ctx context.Context)
	// Close cancels any in-flight pushes via context so the runtime's shutdown
	// path doesn't block on the 10s HTTP client timeout per pending subscriber.
	Close()
}

// The ACP-bridge interfaces below are one contract seen at seven different
// widths, because the runtime asks for wildly different things at different sites.
// *bridge.Bridge satisfies the widest; every narrower one is a statement about
// what a particular function is allowed to do with a bridge it was handed.
//
// The arithmetic, since it is the point: the utility session needs 6 of the 15
// methods, a new session's metadata persist needs 7, a lease needs 2, and the
// parameter-map builders, the host-request answerer, the workflow creator and
// the idle culler need 1 each. Only the per-chat sharedBridge — which starts a
// subprocess, prompts on it, switches its model and its reasoning effort and
// stops it — needs all 15.

// acpSession names the ACP session an RPC is addressed to. 1 of 15, and the only
// one a parameter-map builder has any use for.
type acpSession interface {
	// SessionID returns the ACP session id after Start completes.
	SessionID() vibekit.SessionID
}

// acpCaller sends a JSON-RPC request and waits for its matching response. 1 of
// 15. The provided context enables caller-driven cancellation; if it is
// cancelled before the response arrives, Call returns ctx.Err().
type acpCaller interface {
	Call(ctx context.Context, method string, params any) (*vibekit.RPCResponse, error)
}

// acpResponder answers an INBOUND request from kiro-cli (fs/read_text_file, the
// v3 auth token, the shell type). 1 of 15.
//
// The utility session's forward goroutine takes this and nothing else, and the
// narrowness is load-bearing: an unanswered inbound request wedges the turn
// until the agent drain's ceiling fires, so this goroutine's whole job is to
// reply. Handing it a Call would let a future edit make the answering path spend
// a turn of its own.
type acpResponder interface {
	Respond(ctx context.Context, id int64, result any, err error) error
}

// acpStopper kills the subprocess. 1 of 15. Its NotifCh closes; must be called
// at most once per bridge instance.
type acpStopper interface {
	Stop()
}

// acpSessionCaller is one call, addressed to the bridge's own session. 2 of 15,
// and what a utility-session lease hands its caller to use outside the session
// mutex.
type acpSessionCaller interface {
	acpCaller
	acpSession
}

// acpSessionFacts is everything a freshly started or loaded session knows about
// itself, which is what gets written onto the chat record. 7 of 15, and no
// mutator among them — the persist path reads.
type acpSessionFacts interface {
	acpSession

	// ModelID returns the current model id after Start completes.
	ModelID() vibekit.ModelID
	// CurrentMode returns the currently-active session mode id (empty if the
	// agent doesn't expose modes).
	CurrentMode() string
	// SessionTitle returns KAS's own title for the session, from the
	// session/new or session/load result's flat _meta.title. Creation always
	// yields the literal "New Session"; load yields the real stored title.
	// Advisory — the caller adopts it only for a chat that is still
	// default-named.
	SessionTitle() string
	// Modes returns the set of session modes the running agent supports. Empty
	// for agents that don't expose modes.
	Modes() []vibekit.SessionMode
	// Models returns the set of models the agent can swap to, with
	// deprecated/internal entries filtered out. Zero fallback: if kiro-cli
	// returns nothing, the slice is empty.
	Models() []vibekit.SessionModel
	// ServedModels returns every advertised model id, UNFILTERED — the input to
	// the entitlement check, where Models' display filtering would refuse a
	// deprecated model the account can still use. Empty means unknowable.
	ServedModels() []string
}

// utilityBridge is the long-lived utility session's ACP surface: 6 of the 15.
//
// The 9 it excludes are the point. The utility bridge generates text for error
// explanations, commit messages and the policy/account/knowledge reads; it never
// switches model in session, never sends a bare notification, and never reads
// the mode or model catalogue — the runtime passes it a model at spawn and asks the
// CHAT bridges for the catalogue. A session that can call neither SetModel nor
// SetEffort cannot spend a user's credits at a level nobody asked for.
type utilityBridge interface {
	acpSessionCaller
	acpResponder
	acpStopper

	// Start launches a fresh kiro-cli ACP subprocess. ctx bounds the startup
	// handshake ONLY; the subprocess's lifetime comes from StartOpts.Lifetime,
	// which is REQUIRED.
	Start(ctx context.Context, opts *vibekit.StartOpts) error
	// NotifCh yields incoming ACP notifications, each carrying the read loop's
	// sequence for it. Closes when the subprocess exits. The forward goroutine must
	// be draining it BEFORE Start: on v3 session/new blocks until the host answers
	// requests that arrive here.
	NotifCh() <-chan vibekit.Notification
}

// ACPBridge manages a single kiro-cli ACP subprocess for one chat: all 15
// methods, because a per-chat bridge is started, prompted on, answered on,
// model-switched, effort-switched and stopped. *bridge.Bridge satisfies it. Methods are safe for
// concurrent use; Call and Notify serialize writes to the subprocess stdin
// internally. The prompt-slot state is sharedBridge's; the bridge itself has no
// "busy" concept.
//
// Exported, unlike its narrower relatives, because the composition root has to
// name it: a factory literal's return type is not inferred from the parameter it
// is passed to, so composition writes func() agent.ACPBridge explicitly.
type ACPBridge interface {
	acpSessionFacts
	utilityBridge

	// Notify sends a JSON-RPC notification (no response expected). ctx enables
	// cancellation before the write is attempted.
	Notify(ctx context.Context, method string, params any) error
	// SetModel performs an in-session model swap via
	// session/set_config_option (configId "model") — v3 has no
	// session/set_model. ctx enables caller-driven cancellation.
	SetModel(ctx context.Context, modelID string) error
	// EnsureEffort makes the live session run at the given reasoning-effort level
	// via session/set_config_option (configId "effortLevel"). Its own method rather
	// than a raw Call because a model swap can CLEAR the level KAS is running at, so
	// the coordinator has to re-assert it in the same breath as SetModel and the two
	// belong at the same depth.
	//
	// "Ensure" rather than "set": it compares against the level the session last
	// reported and returns without a round trip on a match, which is what lets the
	// prompt path repair a level KAS changed on its own without paying for a call
	// per prompt. SetModel clears that cache, so a post-swap call always asserts.
	EnsureEffort(ctx context.Context, level string) error
	// CallAt is Call plus the read loop position at which the response arrived, for
	// a caller that has to order a LOCAL decision against the notifications still
	// queued behind that response. The prompt paths are the only callers: a turn
	// settled from its response alone would decide the wire never closed it while
	// the wire's turn_end sat unread in the channel.
	CallAt(ctx context.Context, method string, params any) (*vibekit.RPCResponse, uint64, error)
}

// ACPBridgeFactory creates new ACPBridge instances. The runtime calls it once per
// chat to spawn a fresh kiro-cli subprocess, and once for the utility session;
// each invocation is a new bridge. Exported for the same reason ACPBridge is.
type ACPBridgeFactory func() ACPBridge
