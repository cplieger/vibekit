package agent

import (
	"context"
	"net/http"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// The interfaces below are the runtime's dependency contracts, declared at the
// consumer rather than in a shared package, each naming only what it invokes.

// mcpNameSets is the MCP server-name census. *mcp.Store satisfies it.
//
// All 3 methods are used because the runtime reasons from their NESTING:
// enabled, configured-but-not-enabled, in AllNames only (a Power), in none.
type mcpNameSets interface {
	EnabledNames(ctx context.Context) map[string]struct{}
	ConfiguredNames(ctx context.Context) map[string]struct{}
	// AllNames is best-effort: an unparseable config file reports
	// OriginUnknown for a name it misses rather than dropping it.
	AllNames(ctx context.Context) map[string]struct{}
}

// RouteRegistrar is a component that mounts its own routes under a sub-tree of
// /api/*. The runtime's OUTPUT type, so the registry's own type stays here.
type RouteRegistrar interface {
	RegisterRoutes(mux *http.ServeMux)
}

// bridgeChatRecords is the chat store as the BRIDGE LIFECYCLE uses it.
//
// Delete is deliberately absent: only cmdDeleteChat may remove a chat file, so
// the path that tears bridges down on exit and restart must not be able to.
type bridgeChatRecords interface {
	Get(ctx context.Context, id vibekit.ChatID) (*vibekit.Chat, bool)
	// BuildHistory returns a plain-text transcript for priming, "" if empty.
	BuildHistory(ctx context.Context, id vibekit.ChatID) string
	// Mutate is the single write primitive: load, apply, save, broadcast.
	Mutate(ctx context.Context, id vibekit.ChatID, mutate func(c *vibekit.Chat, exists bool) bool) error
	AppendMessage(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error
	// UpdateMessage amends ONE persisted message by id and NO-OPS when that id
	// is absent, which is what a truncation leaves (amendLostReason).
	UpdateMessage(ctx context.Context, chatID vibekit.ChatID, msgID string, mutate func(*vibekit.Message)) error
}

// chatRecords is the runtime's field type: a UNION of the narrower views the
// composition root passes on to bridgeChatRecords, command.ChatStore and
// translate.ChatRecords. The runtime itself calls only Get, List and Mutate.
type chatRecords interface {
	bridgeChatRecords

	List(ctx context.Context) []vibekit.ChatHeader
	// SetDraft and SetAttachments are passed on to the command dispatcher.
	SetDraft(ctx context.Context, id vibekit.ChatID, text string) (*vibekit.ComposerState, error)
	SetAttachments(ctx context.Context, id vibekit.ChatID, paths []string) (*vibekit.ComposerState, error)
	// Delete removes the chat file; only cmdDeleteChat calls it.
	Delete(ctx context.Context, id vibekit.ChatID) error
	// UpsertTurnPlan writes the turn's single plan row; only HandlePlan calls it.
	UpsertTurnPlan(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error
}

// pushNotifier is the notification SEND half. *push.Service satisfies it.
type pushNotifier interface {
	HasSubscribers() bool
	Send(ctx context.Context, title, body string, notifyType vibekit.PushKind, subject vibekit.PushSubject)
}

// pushService is the runtime's whole view of push: the send half plus the two
// lifecycle calls the runtime owns. Subscribe/Unsubscribe are absent, and
// SetPreferences belongs to the settings endpoint in internal/server.
type pushService interface {
	pushNotifier

	// ReloadPreferences re-reads notification toggles from disk, so an
	// externally-edited config.json takes effect without a restart.
	ReloadPreferences(ctx context.Context)
	// Close cancels in-flight pushes so shutdown does not block on their timeout.
	Close()
}

// The ACP-bridge interfaces below are one contract seen at different widths:
// *bridge.Bridge satisfies the widest, and every narrower one states what a
// particular function may do with a bridge it was handed.

// acpSession names the ACP session an RPC is addressed to.
type acpSession interface {
	SessionID() vibekit.SessionID
}

// acpCaller sends a JSON-RPC request and waits for its response. Returns
// ctx.Err() if ctx is cancelled before the response arrives.
type acpCaller interface {
	Call(ctx context.Context, method string, params any) (*vibekit.RPCResponse, error)
}

// acpResponder answers an INBOUND request from kiro-cli. The utility session's
// forward goroutine takes only this: an unanswered inbound request wedges the
// turn until the drain ceiling fires, so its whole job is to reply.
type acpResponder interface {
	Respond(ctx context.Context, id int64, result any, err error) error
}

// acpStopper kills the subprocess. NotifCh closes; must be called at most
// once per bridge instance.
type acpStopper interface {
	Stop()
}

// acpSessionCaller is one call addressed to the bridge's own session — what a
// utility-session lease hands its caller to use outside the session mutex.
type acpSessionCaller interface {
	acpCaller
	acpSession

	// CallAt is Call plus the read loop position the response arrived at, for a
	// caller ordering a LOCAL decision against notifications still queued behind
	// it — a turn_end or a session/load replay that PRECEDES the response.
	CallAt(ctx context.Context, method string, params any) (*vibekit.RPCResponse, uint64, error)
}

// acpSessionResponder is what the utility session's forward goroutine needs of
// the bridge it was started with: answer its inbound requests, and name the
// session whose frames it may keep. Reading the id off the bridge PARAMETER is
// what keeps forward off us.mu, which stopLocked holds across <-forwardDone.
type acpSessionResponder interface {
	acpResponder
	acpSession
}

// acpSessionFacts is everything a freshly started or loaded session knows about
// itself, written onto the chat record on persist. No mutator among them.
type acpSessionFacts interface {
	acpSession

	ModelID() vibekit.ModelID
	// CurrentMode returns the active mode id, empty when the agent has none.
	CurrentMode() string
	// SessionTitle returns KAS's own title (flat _meta.title). Advisory:
	// creation always yields "New Session", so adopt it only for a default name.
	SessionTitle() string
	// Modes returns the session modes the agent supports; empty if it has none.
	Modes() []vibekit.SessionMode
	// Models returns the swappable models, deprecated/internal entries filtered.
	Models() []vibekit.SessionModel
	// ServedModels returns every advertised model id, UNFILTERED — the
	// entitlement input, where Models' display filter would refuse a live model.
	ServedModels() []string
}

// utilityBridge is the long-lived utility session's ACP surface. It never
// switches model in session, never sends a bare notification, and never reads
// the mode or model catalogue — the CHAT bridges answer for those.
type utilityBridge interface {
	acpSessionCaller
	acpResponder
	acpStopper

	// Start launches a fresh kiro-cli ACP subprocess. ctx bounds the startup
	// handshake ONLY; the subprocess's lifetime is StartOpts.Lifetime, REQUIRED.
	Start(ctx context.Context, opts *vibekit.StartOpts) error
	// NotifCh yields incoming ACP notifications with the read loop's sequence,
	// closing when the subprocess exits. The forward goroutine must be draining
	// it BEFORE Start: on v3 session/new blocks on requests that arrive here.
	NotifCh() <-chan vibekit.Notification
}

// ACPBridge manages a single kiro-cli ACP subprocess for one chat.
// *bridge.Bridge satisfies it. Methods are safe for concurrent use; Call and
// Notify serialize writes to the subprocess stdin internally.
type ACPBridge interface {
	acpSessionFacts
	utilityBridge

	// Notify sends a JSON-RPC notification (no response expected).
	Notify(ctx context.Context, method string, params any) error
	// SetModel performs an in-session model swap via session/set_config_option
	// (configId "model") — v3 has no session/set_model.
	SetModel(ctx context.Context, modelID string) error
	// EnsureEffort makes the live session run at the given reasoning-effort level
	// (session/set_config_option, configId "effortLevel"). It returns without a
	// round trip when the level the session last reported already matches, and
	// SetModel clears that cache because a swap can CLEAR the level KAS runs at.
	EnsureEffort(ctx context.Context, level string) error
	// ObserveEffort records a level the SESSION reported on
	// `config_option_update`, keeping EnsureEffort's comparison honest.
	ObserveEffort(level string)
	// SessionLoadSeq returns the read loop position the `session/load` response
	// arrived at, the position forward must have folded up to. Zero on a
	// session/new, and zero is also legal, so pair it with the load returning.
	SessionLoadSeq() uint64
}

// ACPBridgeFactory creates new ACPBridge instances, once per chat and once for
// the utility session; each invocation is a new bridge.
type ACPBridgeFactory func() ACPBridge
