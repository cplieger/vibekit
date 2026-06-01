// Package api defines the internal contracts between vibekit
// components. All cross-component calls go through these interfaces,
// enabling testability (mock any component) and swappability.
//
// This package contains three concern groups:
//
//  1. Domain types and interfaces — the contract surface (interfaces.go,
//     domain_chat.go, events.go, commands.go, domain_rpc.go, mcp.go,
//     push_types.go, methods.go, strings.go).
//  2. HTTP response/request helpers (httputil.go, decode.go,
//     fileio_serve.go) — WriteJSON, BadRequest, RequireMethod, etc.
//  3. File I/O utilities (fileio.go) — SaveBytes, SaveJSON,
//     CleanupStaleTemps.
//
// Implementation packages import api, never the reverse. Domain types
// Tag, FileChange, and ConflictPayload live in checkpoint/types (a
// leaf package with zero internal dependencies).
package api

import (
	"context"
	"net/http"

	checkpoint "vibekit/internal/checkpoint/types"
)

// --- Persistence ---

// ChatStore persists chat sessions as one JSON file per chat under
// <dir>/<chat_id>.json. The directory listing is the index; there is no
// separate index file. Mutations go through Mutate (atomic load → apply →
// save → broadcast); there is no event-sourcing replay.
type ChatStore interface {
	RouteHandler

	// Wiring

	// SetBroadcaster wires the SSE broadcaster the store uses to emit
	// chat_created / chat_updated / chat_deleted / message_* events.
	// Called once after construction to break the init cycle with the hub.
	SetBroadcaster(b Broadcaster)

	// Reads

	// Get returns the full chat at id, or false if it does not exist.
	Get(ctx context.Context, id ChatID) (*Chat, bool)
	// List returns every chat's header (no messages) sorted by UpdatedAt
	// descending. Checks ctx.Err() between per-file reads.
	List(ctx context.Context) []ChatHeader
	// ChildrenOf returns the IDs of chats whose ParentChatID equals
	// parentID. Used by delete to cascade to rewind children without
	// loading the full chat list.
	ChildrenOf(ctx context.Context, parentID ChatID) []ChatID
	// BuildHistory returns a plain-text transcript used for compress
	// priming. Returns "" if the chat is missing or empty.
	BuildHistory(ctx context.Context, id ChatID) string

	// Mutations

	// Mutate is the single write primitive: load → apply → save →
	// broadcast chat_created / chat_updated.
	Mutate(ctx context.Context, id ChatID, mutate func(c *Chat, exists bool) bool) error
	// Delete removes the chat file and broadcasts chat_deleted.
	Delete(ctx context.Context, id ChatID) error
	// Archive moves a chat to the archive directory instead of deleting.
	Archive(ctx context.Context, id ChatID) error
	// ListArchived returns headers for all archived chats.
	ListArchived(ctx context.Context) []ChatHeader
	// RestoreArchived moves a chat from the archive back to active.
	RestoreArchived(ctx context.Context, id ChatID) error
	// UpdateArchivedSummary rewrites an archived chat's Summary field.
	UpdateArchivedSummary(ctx context.Context, id ChatID, summary string) error
	// LoadArchived returns the parsed archived chat.
	LoadArchived(ctx context.Context, id ChatID) (*Chat, error)
	// DeleteArchived permanently removes a single archived chat.
	DeleteArchived(ctx context.Context, id ChatID) error
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
	// SetLastActive updates the last-active timestamp.
	SetLastActive()
	// SetPrompting sets the bridge state to prompting (for recovery).
	SetPrompting()
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

	// RegisterSlashRoutes wires /api/slash/execute and /api/slash/options.
	RegisterSlashRoutes(mux *http.ServeMux)

	// Lifecycle

	// Shutdown drains in-flight prompts and closes all bridges.
	Shutdown()
}

// StartOpts collects the parameters for ACPBridge.Start. All fields are
// optional; a zero-value StartOpts creates a new session with no agent,
// no model override, no extra args, and no MCP servers.
type StartOpts struct {
	// SessionID is the ACP session id to resume. Empty means create new.
	SessionID string
	// Agent is the agent identifier to launch.
	Agent string
	// Model is the model id to use for the session.
	Model string
	// ExtraArgs are permission-mode flags derived from user settings.
	ExtraArgs []string
	// MCPServers is the ACP mcpServers array (enabled user-configured
	// MCP servers). Nil means empty set.
	MCPServers []map[string]any
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
	// Modes returns the set of session modes the running agent
	// supports. Empty for agents that don't expose modes.
	Modes() []SessionMode
	// Models returns the set of models the agent can swap to, with
	// deprecated/internal entries filtered out. Zero fallback: if
	// kiro-cli returns nothing, the slice is empty.
	Models() []SessionModel
	// SetModel performs an in-session model swap via session/set_model.
	// ctx enables caller-driven cancellation of the RPC call.
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
// typed contract.
type UtilityPrompter interface {
	UtilityPrompt(ctx context.Context, prompt string) (string, error)
}

// --- Pending Changes ---

// PendingStore is the consumer-side interface for the pending-change
// subsystem. The hub uses these methods for SSE replay, rejection on
// bridge teardown, and full-content retrieval. The concrete
// *pending.Store satisfies this interface implicitly.
type PendingStore interface {
	// ListForChat returns all pending changes for the given chat.
	ListForChat(chatID ChatID) []PendingChange
	// Get returns a single pending change by tool-call ID.
	Get(toolCallID string) (PendingChange, bool)
	// ChatIDs returns the IDs of all chats with pending changes.
	ChatIDs() []ChatID
	// RejectAllForChat rejects all pending changes for the given chat,
	// returning the rejected snapshots.
	RejectAllForChat(chatID ChatID) []PendingChange
	// Resolve resolves a single pending change with the given action.
	Resolve(ctx context.Context, toolCallID string, action PendingAction) (PendingChange, error)
}

// --- Checkpoints ---

// CheckpointService is the consumer-side interface for the checkpoint
// subsystem. Enables stub/mock injection in hub tests without
// constructing a real checkpoint.Store with filesystem state.
type CheckpointService interface {
	Snapshot(ctx context.Context, chatID ChatID, relPath string, newContent []byte, messageCount int) (checkpoint.Tag, error)
	Restore(ctx context.Context, chatID ChatID, tag checkpoint.Tag) (int, error)
	RestorePreview(ctx context.Context, chatID ChatID, tag checkpoint.Tag) ([]string, error)
	CheckoutFile(ctx context.Context, chatID ChatID, tag checkpoint.Tag, relPath string) error
	Diff(ctx context.Context, chatID ChatID, from, to checkpoint.Tag) ([]checkpoint.FileChange, error)
	Conflicts(ctx context.Context, chatID ChatID) ([]checkpoint.ConflictPayload, error)
	ReadBlob(ctx context.Context, chatID ChatID, sha string) ([]byte, error)
	OldestTag(ctx context.Context, chatID ChatID) checkpoint.Tag
	AdvanceTurn(ctx context.Context, chatID ChatID, messageCount int)
	Cleanup(ctx context.Context, chatID ChatID)
	StartBackgroundTasks(ctx context.Context)
	Stop()
}
