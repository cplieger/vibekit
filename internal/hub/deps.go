package hub

import (
	"context"

	"github.com/cplieger/vibekit/internal/api"
)

// The interfaces below are the hub's DEPENDENCY contracts: what it calls on the
// collaborators the composition root hands it. Each is declared HERE, at the
// consumer, rather than in a shared contract package, and each names only the
// methods this package invokes — which is what keeps a test fake small enough to
// be obviously correct and stops an unrelated method from widening a contract
// every package in the build then has to import.
//
// The width arithmetic is stated per interface. Where the hub genuinely uses all
// of what an implementation offers, that is said plainly: the placement is the
// win in that case, because a contract with one consumer has no business in a
// package everything imports.

// mcpNameSets is the MCP server-name census: which of the user's servers are
// switched on, which its config holds at all, and which are reachable through
// the config file including the powers block vibekit never writes. *mcp.Store
// satisfies it.
//
// 3 of the 3 methods the store exports for this, and the hub needs all three
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
	Send(ctx context.Context, title, body string, notifyType api.PushKind, subject api.PushSubject)
}

// pushService is the hub's whole view of push: the send half plus the two
// lifecycle calls the hub owns.
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
	// Close cancels any in-flight pushes via context so the hub's shutdown
	// path doesn't block on the 10s HTTP client timeout per pending subscriber.
	Close()
}

// The ACP-bridge interfaces below are one contract seen at seven different
// widths, because the hub asks for wildly different things at different sites.
// *bridge.Bridge satisfies the widest; every narrower one is a statement about
// what a particular function is allowed to do with a bridge it was handed.
//
// The arithmetic, since it is the point: the utility session needs 6 of the 14
// methods, a new session's metadata persist needs 7, a lease needs 2, and the
// parameter-map builders, the host-request answerer, the workflow creator and
// the idle culler need 1 each. Only the per-chat sharedBridge — which starts a
// subprocess, prompts on it, switches its model and stops it — needs all 14.

// acpSession names the ACP session an RPC is addressed to. 1 of 14, and the only
// one a parameter-map builder has any use for.
type acpSession interface {
	// SessionID returns the ACP session id after Start completes.
	SessionID() api.SessionID
}

// acpCaller sends a JSON-RPC request and waits for its matching response. 1 of
// 14. The provided context enables caller-driven cancellation; if it is
// cancelled before the response arrives, Call returns ctx.Err().
type acpCaller interface {
	Call(ctx context.Context, method string, params any) (*api.RPCResponse, error)
}

// acpResponder answers an INBOUND request from kiro-cli (fs/read_text_file, the
// v3 auth token, the shell type). 1 of 14.
//
// The utility session's forward goroutine takes this and nothing else, and the
// narrowness is load-bearing: an unanswered inbound request wedges the turn
// until the agent drain's ceiling fires, so this goroutine's whole job is to
// reply. Handing it a Call would let a future edit make the answering path spend
// a turn of its own.
type acpResponder interface {
	Respond(ctx context.Context, id int64, result any, err error) error
}

// acpStopper kills the subprocess. 1 of 14. Its NotifCh closes; must be called
// at most once per bridge instance.
type acpStopper interface {
	Stop()
}

// acpSessionCaller is one call, addressed to the bridge's own session. 2 of 14,
// and what a utility-session lease hands its caller to use outside the session
// mutex.
type acpSessionCaller interface {
	acpCaller
	acpSession
}

// acpSessionFacts is everything a freshly started or loaded session knows about
// itself, which is what gets written onto the chat record. 7 of 14, and no
// mutator among them — the persist path reads.
type acpSessionFacts interface {
	acpSession

	// ModelID returns the current model id after Start completes.
	ModelID() api.ModelID
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
	Modes() []api.SessionMode
	// Models returns the set of models the agent can swap to, with
	// deprecated/internal entries filtered out. Zero fallback: if kiro-cli
	// returns nothing, the slice is empty.
	Models() []api.SessionModel
	// ServedModels returns every advertised model id, UNFILTERED — the input to
	// the entitlement check, where Models' display filtering would refuse a
	// deprecated model the account can still use. Empty means unknowable.
	ServedModels() []string
}

// utilityBridge is the long-lived utility session's ACP surface: 6 of the 14.
//
// The 8 it excludes are the point. The utility bridge generates text for error
// explanations, commit messages and the policy/account/knowledge reads; it never
// switches model in session, never sends a bare notification, and never reads
// the mode or model catalogue — the hub passes it a model at spawn and asks the
// CHAT bridges for the catalogue. A session that cannot call SetModel cannot
// spend a user's credits at a level nobody asked for.
type utilityBridge interface {
	acpSessionCaller
	acpResponder
	acpStopper

	// Start launches a fresh kiro-cli ACP subprocess. ctx bounds the startup
	// handshake ONLY; the subprocess's lifetime comes from StartOpts.Lifetime,
	// which is REQUIRED.
	Start(ctx context.Context, opts *api.StartOpts) error
	// NotifCh yields incoming ACP notifications. Closes when the subprocess
	// exits. The forward goroutine must be draining it BEFORE Start: on v3
	// session/new blocks until the host answers requests that arrive here.
	NotifCh() <-chan *api.RPCResponse
}

// ACPBridge manages a single kiro-cli ACP subprocess for one chat: all 14
// methods, because a per-chat bridge is started, prompted on, answered on,
// model-switched and stopped. *bridge.Bridge satisfies it. Methods are safe for
// concurrent use; Call and Notify serialize writes to the subprocess stdin
// internally. The prompt-slot state is sharedBridge's; the bridge itself has no
// "busy" concept.
//
// Exported, unlike its narrower relatives, because the composition root has to
// name it: a factory literal's return type is not inferred from the parameter it
// is passed to, so composition writes func() hub.ACPBridge explicitly.
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
}

// ACPBridgeFactory creates new ACPBridge instances. The hub calls it once per
// chat to spawn a fresh kiro-cli subprocess, and once for the utility session;
// each invocation is a new bridge. Exported for the same reason ACPBridge is.
type ACPBridgeFactory func() ACPBridge
