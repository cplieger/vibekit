// Package api defines the internal contracts between vibekit
// components. All cross-component calls go through these interfaces,
// enabling testability (mock any component) and swappability.
//
// This package contains two concern groups:
//
//  1. Domain types and interfaces — the contract surface (interfaces.go,
//     domain_chat.go, events.go, commands.go, domain_rpc.go, mcp.go,
//     push_types.go, methods.go, strings.go).
//  2. HTTP response/request helpers (httputil.go, decode.go) —
//     WriteJSON, BadRequest, MethodNotAllowed, etc.
//
// Atomic file I/O (SaveBytes, bounded reads) lives in the external
// cplieger/atomicfile package, not here.
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
	// SetPrompting sets the bridge state to prompting (for recovery).
	SetPrompting()
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

// StartOpts collects the parameters for ACPBridge.Start. All fields are
// optional; a zero-value StartOpts creates a new session with no model
// override.
//
// There is no MCPServers field. The user's MCP servers reach KAS through its own
// config file, which vibekit renders — passing them on session/new would OUTRANK
// that file and freeze the set for the session's lifetime.
type StartOpts struct {
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
}

// ACPBridge manages a single kiro-cli ACP subprocess for one chat. Methods
// are safe for concurrent use; Call and Notify serialize writes to the
// subprocess stdin internally. IsPrompting/SetPrompting are wrappers' state;
// the bridge itself has no "busy" concept.
type ACPBridge interface {
	// Start launches a fresh kiro-cli ACP subprocess. If
	// StartOpts.SessionID is empty, a new ACP session is created
	// (session/new). If populated, the existing session is resumed
	// (session/load). Exactly one call per bridge instance. ctx enables
	// caller-driven cancellation of the startup handshake.
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

// --- Services ---

// SteeringGenerator generates steering files for kiro-cli.
type SteeringGenerator interface {
	Generate(ctx context.Context)
	CustomPath() string
}

// --- HTTP ---

// GitHandler registers git-related HTTP routes under /api/git/*.
type GitHandler interface {
	RouteHandler
}

// FileHandler registers file browsing and editing routes under /api/file/*
// and /api/files/*.
type FileHandler interface {
	RouteHandler
}

// AuthHandler registers /api/whoami, /api/login, /api/logout.
type AuthHandler interface {
	RouteHandler
}

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
	Send(ctx context.Context, title, body string, notifyType PushKind)
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

// AccountUsageProvider fetches account/subscription-level usage (plan,
// credits, quota) via the KAS _kiro/account/getUsage request on a live
// bridge. Narrow interface so the server can serve GET /api/account/usage
// without depending on the full Hub surface; the concrete *hub.Hub
// satisfies it via the utility bridge.
type AccountUsageProvider interface {
	AccountUsage(ctx context.Context) (*AccountUsage, error)
}

// --- Pending Changes ---

// There is no PendingStore interface. It existed for vibekit's own staging queue
// — SSE replay of staged ops, rejection on bridge teardown, full-content
// retrieval — and all three are gone with internal/pending. KAS's turn approval
// arrives as an ordinary permission request, so the permission tracker covers it.
