package agent

import (
	"context"
	"net/http"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// The interfaces below are the runtime's DEPENDENCY contracts: what it calls
// on the collaborators the composition root hands it. Each is declared HERE,
// at the consumer, rather than in a shared contract package, and each names
// only the methods this package invokes.

// mcpNameSets is the MCP server-name census: which servers are enabled,
// which the config holds at all, and which are reachable through the
// config file including the powers block. *mcp.Store satisfies it.
//
// All 3 methods are used because the runtime reasons from their NESTING:
// enabled means record and tag OriginUser; configured-but-not-enabled drops
// a frame; in AllNames but not configured can only have come from a Power;
// in none of them is a source vibekit cannot see. AllNames is the only
// member that touches disk, so the classifier consults it last.
type mcpNameSets interface {
	EnabledNames(ctx context.Context) map[string]struct{}
	ConfiguredNames(ctx context.Context) map[string]struct{}
	// AllNames is best-effort: a hand-edit can make the config file
	// unparseable, so a name this set misses reports OriginUnknown rather
	// than being dropped.
	AllNames(ctx context.Context) map[string]struct{}
}

// RouteRegistrar is a component that mounts its own routes under a sub-tree
// of /api/*. It is the runtime's OUTPUT type: MCPRegistry hands the runtime
// MCP registry out as one so the composition root can pass it to the
// server without the registry's concrete type leaving this package.
type RouteRegistrar interface {
	RegisterRoutes(mux *http.ServeMux)
}

// bridgeChatRecords is the chat store as the BRIDGE LIFECYCLE uses it: read
// a chat, read its transcript to prime a fresh session, write the session
// metadata a spawn produced, and append the turn's messages.
//
// Delete is deliberately absent: only cmdDeleteChat may remove a chat file,
// so the path that tears bridges down on exit, model switch and restart
// must not be able to.
type bridgeChatRecords interface {
	Get(ctx context.Context, id vibekit.ChatID) (*vibekit.Chat, bool)
	// BuildHistory returns a plain-text transcript used for compress
	// priming, or "" if the chat is missing or empty.
	BuildHistory(ctx context.Context, id vibekit.ChatID) string
	// Mutate is the single write primitive: load, apply, save, broadcast.
	Mutate(ctx context.Context, id vibekit.ChatID, mutate func(c *vibekit.Chat, exists bool) bool) error
	AppendMessage(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error
}

// chatRecords is the runtime's field type: a UNION of the narrower views the
// composition root passes on to bridgeChatRecords (4), command.ChatStore (6)
// and translate.ChatRecords (4). A field has to satisfy every one of them,
// though the runtime itself calls only 3 (Get, List, Mutate).
type chatRecords interface {
	bridgeChatRecords

	List(ctx context.Context) []vibekit.ChatHeader
	// SetDraft and SetAttachments persist the chat's parked composer state.
	// Passed on to the command dispatcher; the runtime never calls either.
	SetDraft(ctx context.Context, id vibekit.ChatID, text string) (*vibekit.ComposerState, error)
	SetAttachments(ctx context.Context, id vibekit.ChatID, paths []string) (*vibekit.ComposerState, error)
	// Delete removes the chat file and broadcasts chat_deleted; only
	// cmdDeleteChat calls it.
	Delete(ctx context.Context, id vibekit.ChatID) error
	// UpsertTurnPlan writes the turn's single plan row; only HandlePlan
	// calls it.
	UpsertTurnPlan(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error
}

// pushNotifier is the notification SEND half: ask whether anyone is
// listening, then send. *push.Service satisfies it.
type pushNotifier interface {
	HasSubscribers() bool
	Send(ctx context.Context, title, body string, notifyType vibekit.PushKind, subject vibekit.PushSubject)
}

// pushService is the runtime's whole view of push: the send half plus the
// two lifecycle calls the runtime owns. Subscribe/Unsubscribe are absent —
// nothing reaches them through an interface — and SetPreferences belongs to
// the settings endpoint in internal/server, not here.
type pushService interface {
	pushNotifier

	// ReloadPreferences re-reads notification toggles from disk (dedup via
	// singleflight), so an externally-edited config.json takes effect
	// without a container restart. Called on SSE reconnect.
	ReloadPreferences(ctx context.Context)
	// Close cancels in-flight pushes via context so shutdown doesn't block
	// on the 10s HTTP client timeout per pending subscriber.
	Close()
}

// The ACP-bridge interfaces below are one contract seen at different widths,
// because the runtime asks for wildly different things at different sites.
// *bridge.Bridge satisfies the widest; every narrower one states what a
// particular function may do with a bridge it was handed. Only the per-chat
// sharedBridge — which starts a subprocess, prompts on it, switches its
// model and reasoning effort, and stops it — needs all 15.

// acpSession names the ACP session an RPC is addressed to.
type acpSession interface {
	SessionID() vibekit.SessionID
}

// acpCaller sends a JSON-RPC request and waits for its matching response.
// ctx enables caller-driven cancellation; if it is cancelled before the
// response arrives, Call returns ctx.Err().
type acpCaller interface {
	Call(ctx context.Context, method string, params any) (*vibekit.RPCResponse, error)
}

// acpResponder answers an INBOUND request from kiro-cli (fs/read_text_file,
// the v3 auth token, the shell type). The utility session's forward
// goroutine takes only this: an unanswered inbound request wedges the turn
// until the drain ceiling fires, so its whole job is to reply.
type acpResponder interface {
	Respond(ctx context.Context, id int64, result any, err error) error
}

// acpStopper kills the subprocess. NotifCh closes; must be called at most
// once per bridge instance.
type acpStopper interface {
	Stop()
}

// acpSessionCaller is one call addressed to the bridge's own session —
// what a utility-session lease hands its caller to use outside the session
// mutex.
type acpSessionCaller interface {
	acpCaller
	acpSession
}

// acpSessionFacts is everything a freshly started or loaded session knows
// about itself, written onto the chat record on persist. No mutator among
// them.
type acpSessionFacts interface {
	acpSession

	ModelID() vibekit.ModelID
	// CurrentMode returns the currently-active session mode id (empty if
	// the agent doesn't expose modes).
	CurrentMode() string
	// SessionTitle returns KAS's own title from session/new or
	// session/load's flat _meta.title. Creation always yields the literal
	// "New Session"; load yields the real stored title. Advisory — the
	// caller adopts it only for a chat still default-named.
	SessionTitle() string
	// Modes returns the session modes the running agent supports; empty
	// for agents that don't expose modes.
	Modes() []vibekit.SessionMode
	// Models returns the models the agent can swap to, deprecated/internal
	// entries filtered out. Empty if kiro-cli returns nothing.
	Models() []vibekit.SessionModel
	// ServedModels returns every advertised model id, UNFILTERED — the
	// entitlement check's input, where Models' display filtering would
	// refuse a deprecated model the account can still use.
	ServedModels() []string
}

// utilityBridge is the long-lived utility session's ACP surface. It never
// switches model in session, never sends a bare notification, and never
// reads the mode or model catalogue — the runtime passes it a model at
// spawn and asks the CHAT bridges for the catalogue.
type utilityBridge interface {
	acpSessionCaller
	acpResponder
	acpStopper

	// Start launches a fresh kiro-cli ACP subprocess. ctx bounds the
	// startup handshake ONLY; the subprocess's lifetime comes from
	// StartOpts.Lifetime, which is REQUIRED.
	Start(ctx context.Context, opts *vibekit.StartOpts) error
	// NotifCh yields incoming ACP notifications, each carrying the read
	// loop's sequence for it. Closes when the subprocess exits. The
	// forward goroutine must be draining it BEFORE Start: on v3
	// session/new blocks until the host answers requests that arrive
	// here.
	NotifCh() <-chan vibekit.Notification
}

// ACPBridge manages a single kiro-cli ACP subprocess for one chat: all 15
// methods, because a per-chat bridge is started, prompted on, answered on,
// model-switched, effort-switched and stopped. *bridge.Bridge satisfies it.
// Methods are safe for concurrent use; Call and Notify serialize writes to
// the subprocess stdin internally.
type ACPBridge interface {
	acpSessionFacts
	utilityBridge

	// Notify sends a JSON-RPC notification (no response expected).
	Notify(ctx context.Context, method string, params any) error
	// SetModel performs an in-session model swap via
	// session/set_config_option (configId "model") — v3 has no
	// session/set_model.
	SetModel(ctx context.Context, modelID string) error
	// EnsureEffort makes the live session run at the given reasoning-effort
	// level via session/set_config_option (configId "effortLevel"). Its
	// own method rather than a raw Call because a model swap can CLEAR the
	// level KAS is running at, so the coordinator must re-assert it in the
	// same breath as SetModel.
	//
	// "Ensure" rather than "set": it compares against the level the
	// session last reported and returns without a round trip on a match,
	// letting the prompt path repair a level KAS changed on its own
	// without paying for a call per prompt. SetModel clears that cache, so
	// a post-swap call always asserts.
	EnsureEffort(ctx context.Context, level string) error
	// ObserveEffort hands the bridge a reasoning-effort level the SESSION
	// reported on the `config_option_update` notification, which the bridge
	// forwards unread. It is what keeps EnsureEffort's differs-only comparison
	// honest against a level KAS moved on its own: the runtime has already
	// decoded that frame, so the observation costs one method call on a cold
	// path rather than a second decode of every streaming delta.
	ObserveEffort(level string)
	// CallAt is Call plus the read loop position at which the response
	// arrived, for a caller that must order a LOCAL decision against
	// notifications still queued behind that response. The prompt paths
	// are the only callers: a turn settled from its response alone would
	// decide the wire never closed it while the wire's turn_end sat
	// unread in the channel.
	CallAt(ctx context.Context, method string, params any) (*vibekit.RPCResponse, uint64, error)
}

// ACPBridgeFactory creates new ACPBridge instances. The runtime calls it
// once per chat and once for the utility session; each invocation is a new
// bridge.
type ACPBridgeFactory func() ACPBridge
