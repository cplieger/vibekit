// Package api defines the internal contracts between vibekit
// components. All cross-component calls go through these interfaces,
// enabling testability (mock any component) and swappability.
//
// The package holds the contract surface: the interfaces (interfaces.go) and
// the wire and domain types (domain_chat.go, events.go, commands.go,
// domain_rpc.go, mcp.go, push_types.go, methods.go, strings.go). The types
// are what keep the dependency graph acyclic, and cmd/wire-codegen walks them
// through internal/wirespec to generate the TypeScript client's decoders.
//
// HTTP request and response plumbing is NOT here: WriteJSON, BadRequest,
// MethodNotAllowed, DecodeJSON and the rest live in internal/httpreply, which
// imports nothing from this package. Atomic file I/O (SaveBytes, bounded
// reads) lives in the external cplieger/atomicfile package.
//
// Implementation packages import api, never the reverse.
package api

import (
	"context"
	"net/http"
	"time"
)

// --- Persistence ---

// ChatStore persists chat sessions as one JSON file per chat under
// <dir>/<chat_id>.json. The directory listing is the index; there is no
// separate index file. Mutations go through Mutate (atomic load → apply →
// save → broadcast); there is no event-sourcing replay.
type ChatStore interface {
	RouteHandler

	// Reads

	// Get returns the full chat at id, or false if it does not exist.
	Get(ctx context.Context, id ChatID) (*Chat, bool)
	// List returns every chat's header (no messages) sorted by UpdatedAt
	// descending. Checks ctx.Err() between per-file reads.
	List(ctx context.Context) []ChatHeader
	// BuildHistory returns a plain-text transcript used for compress
	// priming. Returns "" if the chat is missing or empty.
	BuildHistory(ctx context.Context, id ChatID) string

	// Mutations

	// Mutate is the single write primitive: load → apply → save →
	// broadcast chat_created / chat_updated.
	Mutate(ctx context.Context, id ChatID, mutate func(c *Chat, exists bool) bool) error
	// SetDraft persists the chat's unsent composer text (Chat.Draft).
	//
	// Its own method rather than a Mutate call because a draft save is not
	// ACTIVITY and must not be recorded as any. Mutate stamps UpdatedAt on
	// every write and the retention purge ages a chat from exactly that field,
	// so a debounced autosave firing while the user types would push the purge
	// cutoff out by a whole retention window each time — a chat holding an
	// abandoned draft would never be purged, and a draft can hold a
	// credential. It also broadcasts nothing: Draft is not on ChatHeader, so
	// the frame would carry no draft and only cost every client a re-render.
	//
	// A no-op (nil error) for a chat that does not exist: a chat is a server
	// record from its first prompt onward, and typing must not create one.
	SetDraft(ctx context.Context, id ChatID, text string) error
	// Delete removes the chat file and broadcasts chat_deleted.
	Delete(ctx context.Context, id ChatID) error
	// AppendMessage appends msg to the chat's messages.
	AppendMessage(ctx context.Context, chatID ChatID, msg *Message) error
	// UpdateMessage mutates one message in place (by ID).
	UpdateMessage(ctx context.Context, chatID ChatID, msgID string, mutate func(*Message)) error
}

// --- Communication ---

// CommandBridge abstracts the per-chat ACP bridge for command handlers.
// Declared here (consumer-side) so both command and hub can reference
// the same contract without circular imports.
type CommandBridge interface {
	// Call sends an RPC call to kiro-cli.
	Call(ctx context.Context, method string, params any) (*RPCResponse, error)
	// Notify sends a one-way notification to kiro-cli.
	Notify(ctx context.Context, method string, params any) error
	// Respond sends a permission response to kiro-cli.
	Respond(ctx context.Context, requestID int64, result any, err error) error
	// SessionID returns the current ACP session ID.
	SessionID() SessionID
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
	// IsPrimed reports whether the bridge has been primed.
	IsPrimed() bool
	// SetPrimed marks the bridge as primed.
	SetPrimed()
}

// Broadcaster sends events to all connected SSE clients.
type Broadcaster interface {
	Broadcast(ctx context.Context, evt ServerEvent)
}

// Hub manages SSE connections and dispatches POST /api/command.
type Hub interface {
	Broadcaster
	RouteHandler

	// RouteHandler provides RegisterRoutes to wire /api/events (SSE) and
	// /api/command (POST).

	// Lifecycle

	// Shutdown drains in-flight prompts and closes all bridges.
	Shutdown()
}

// StartOpts collects the parameters for ACPBridge.Start. Lifetime is
// REQUIRED; every other field is optional, so a StartOpts carrying nothing but
// a Lifetime creates a new session with no model override.
//
// There is no MCPServers field. The user's MCP servers reach KAS through its own
// config file, which vibekit renders — passing them on session/new would OUTRANK
// that file and freeze the set for the session's lifetime.
type StartOpts struct {
	// Lifetime bounds the kiro-cli SUBPROCESS. Cancelling it closes the
	// process's stdin and signals its tree.
	//
	// It is REQUIRED, and Start refuses a nil one. This is the canonical
	// statement of the fleet's rule for a lifetime context: a long-lived
	// component takes it as a parameter to the method that runs, and where
	// that context must outlive the call it is REQUIRED there rather than
	// defaulted, because every default for a lifetime is a lifetime nothing
	// can cancel. Start used to substitute context.WithoutCancel(ctx) —
	// literally an uncancellable context — for a caller who forgot this
	// field, and startProcess had a second context.Background() fallback
	// behind that one. Both are gone; a caller that genuinely wants a
	// subprocess owned solely by Stop() now says so by passing
	// context.Background() at the call site, which is a decision in the
	// diff rather than an omission nobody reviews.
	//
	// It is deliberately NOT Start's ctx, which bounds only the startup
	// handshake. Two contexts, one parameter each, is also why the lifetime
	// rides this struct instead of becoming a second positional argument:
	// adjacent same-typed parameters are the misuse-proof-signature hazard
	// this fleet fixes elsewhere, and a swap here is silent. Conflating the
	// two is a defect with a measured signature:
	// CmdPrompt runs a turn under a per-turn context and cancels it on
	// handler return, so a bridge that took its lifetime from there had its
	// stdin closed and its head signalled the moment the FIRST prompt
	// finished. vibekit could not see that either — kiro-cli passes its stdio
	// down to kiro-cli-chat and node, so all three hold the write end of the
	// stdout pipe and the head's death never reaches the readLoop as EOF.
	// The bridge stayed registered and healthy-looking (measured 105 s) while
	// every write to it returned "file already closed", which is what made
	// every model switch fall back to a restart, and each abandoned child
	// tree leaked ~250 MB.
	//
	// Pass the hub's shutdown context, not a request or turn context.
	Lifetime    context.Context
	SessionID   string
	Model       string
	Effort      string
	AgentEngine string
	Mode        string
	// ExtraArgs are operator-supplied kiro-cli launch flags
	// (VIBEKIT_KIRO_ACP_ARGS), already filtered, appended after the args
	// vibekit derives itself. Set on CHAT bridges only — never on the utility
	// bridge, where an `--effort max` would spend real credits generating a
	// two-word title. See bridge.FilterACPArgs.
	ExtraArgs []string
	// EnableHooks opts the session into KAS's v2 hook engine by
	// declaring _meta.kiro.hooks={enabled,v2} in the initialize
	// handshake. Set on BOTH the utility bridge (so the hooks-management
	// dashboard's list|setEnabled|triggerHook work) AND chat bridges (so
	// the workspace's .kiro/hooks/*.json hooks autofire on their triggers
	// during a turn). In v2 mode KAS loads and runs the hooks itself; it
	// does not call back the client to execute autofired hooks. See
	// internal/hub/hooks.go and internal/hub/bridge_coord.go.
	EnableHooks bool
	// Supervised requests KAS's turn-approval gate for this session, by setting
	// the `autopilot` config option to FALSE at session/new.
	//
	// A value passed once at creation, not a flag vibekit enforces: it persists
	// into KAS's own session metadata, so it survives session/load and never needs
	// re-asserting. Everything vibekit used to do to hold writes back — staging
	// them, mirroring them, resolving them one at a time — is KAS's now.
	Supervised bool
	// SecretStorage declares `_meta.kiro.secretStorage` in the initialize
	// handshake, opting into KAS's AcpSecretStorage so it asks this client to
	// hold the MCP OAuth credentials it derives.
	//
	// It is a per-spawn value rather than a constant because declaring the
	// capability is a COMMITMENT: KAS rethrows a client-side store failure into
	// the MCP connect path, so a bridge that declares it with no credential
	// store behind it turns every MCP OAuth connect into a failure. Set from
	// whether the hub actually opened a store (internal/secretstore), which is
	// best-effort — no configDir, or a mode that cannot be verified, leaves it
	// nil. Undeclared, KAS keeps its own in-process copy and re-runs discovery
	// and `POST /register` per spawn, which is the documented degradation and
	// what vibekit did before the store existed.
	//
	// Set on BOTH chat bridges and the utility bridge: the capability rides the
	// shared initialize, so whoever declares it must be able to answer.
	SecretStorage bool
}

// ACPBridge manages a single kiro-cli ACP subprocess for one chat. Methods
// are safe for concurrent use; Call and Notify serialize writes to the
// subprocess stdin internally. The prompt-slot state is the wrapper's;
// the bridge itself has no "busy" concept.
type ACPBridge interface {
	// Start launches a fresh kiro-cli ACP subprocess. If
	// StartOpts.SessionID is empty, a new ACP session is created
	// (session/new). If populated, the existing session is resumed
	// (session/load). Exactly one call per bridge instance.
	//
	// ctx bounds the startup handshake ONLY. The subprocess's own lifetime
	// comes from StartOpts.Lifetime, which is REQUIRED — Start returns an
	// error on a nil one rather than substituting a context nothing can
	// cancel — so a caller may pass a request-scoped ctx here without
	// killing the bridge when that request returns.
	Start(ctx context.Context, opts *StartOpts) error
	// Stop kills the subprocess. NotifCh closes. Must be called at most
	// once per bridge instance.
	Stop()
	// Call sends a JSON-RPC request and waits for its matching response.
	// The provided context enables caller-driven cancellation; if ctx is
	// cancelled before the response arrives, Call returns ctx.Err().
	Call(ctx context.Context, method string, params any) (*RPCResponse, error)
	// Notify sends a JSON-RPC notification (no response expected). ctx
	// enables cancellation before the write is attempted.
	Notify(ctx context.Context, method string, params any) error
	// Respond writes a JSON-RPC response to an incoming request from
	// kiro-cli (e.g. fs/read_text_file). ctx enables cancellation before
	// the write is attempted.
	Respond(ctx context.Context, id int64, result any, err error) error
	// SessionID returns the ACP session id after Start completes.
	SessionID() SessionID
	// ModelID returns the current model id after Start completes.
	ModelID() ModelID
	// CurrentMode returns the currently-active session mode id (empty
	// if the agent doesn't expose modes).
	CurrentMode() string
	// SessionTitle returns KAS's own title for the session, from the
	// session/new or session/load result's flat _meta.title. Creation
	// always yields the literal "New Session"; load yields the real
	// stored title. Advisory — the caller adopts it only for a chat that
	// is still default-named.
	SessionTitle() string
	// Modes returns the set of session modes the running agent
	// supports. Empty for agents that don't expose modes.
	Modes() []SessionMode
	// Models returns the set of models the agent can swap to, with
	// deprecated/internal entries filtered out. Zero fallback: if
	// kiro-cli returns nothing, the slice is empty.
	Models() []SessionModel
	// ServedModels returns every advertised model id, UNFILTERED — the input to
	// the entitlement check, where Models' display filtering would refuse a
	// deprecated model the account can still use. Empty means unknowable.
	ServedModels() []string
	// SetModel performs an in-session model swap via
	// session/set_config_option (configId "model") — v3 has no
	// session/set_model. ctx enables caller-driven cancellation of the
	// RPC call.
	SetModel(ctx context.Context, modelID string) error
	// NotifCh yields incoming ACP notifications. Closes when the
	// subprocess exits.
	NotifCh() <-chan *RPCResponse
}

// ACPBridgeFactory creates new ACPBridge instances. The hub calls it
// once per chat to spawn a fresh kiro-cli subprocess; each factory
// invocation is a new bridge.
type ACPBridgeFactory func() ACPBridge

// --- HTTP ---

// RouteHandler is the minimal contract for any component that wires its
// own routes under a sub-tree of /api/*. Used by the MCP config store,
// the MCP runtime registry, and anything else that owns its own mux
// subset.
type RouteHandler interface {
	RegisterRoutes(mux *http.ServeMux)
}

// PushService manages Web Push subscriptions and sends notifications.
type PushService interface {
	RouteHandler
	Subscribe(sub PushSubscription)
	Unsubscribe(endpoint string)
	Send(ctx context.Context, title, body string, notifyType PushKind, subject PushSubject)
	HasSubscribers() bool
	SetPreferences(prefs map[PushKind]bool)
	// ReloadPreferences re-reads notification toggles from disk,
	// deduplicating concurrent calls via singleflight. Called on SSE
	// reconnect so externally-edited config.json changes take effect
	// without a container restart.
	ReloadPreferences(ctx context.Context)
	// Close cancels any in-flight pushes via context so the hub's
	// shutdown path doesn't block on the 10s HTTP client timeout
	// per pending subscriber.
	Close()
}

// --- AI Utilities ---

// UtilityPrompter is the narrow interface for AI-backed prompt
// generation (error explanations, conflict resolution, commit messages).
// Declared here so both the server and git packages share a single
// typed contract. It is NOT used for chat titles — those come from KAS
// (see translate/focus.go). effort is the per-task reasoning-effort
// level: cheap tasks (summaries, error explanations) pass EffortLow, tasks
// that read a diff or merge code (commit messages, PR descriptions,
// conflict resolution) pass EffortMedium; "" keeps the session's current
// level. Best-effort — a model with no effort config ignores it.
type UtilityPrompter interface {
	UtilityPrompt(ctx context.Context, prompt string, effort EffortLevel) (string, error)
}

// --- Pending Changes ---

// There is no PendingStore interface. It existed for vibekit's own staging queue
// — SSE replay of staged ops, rejection on bridge teardown, full-content
// retrieval — and all three are gone with internal/pending. KAS's turn approval
// arrives as an ordinary permission request, so the permission tracker covers it.
