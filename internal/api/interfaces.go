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

// There is no Broadcaster interface here. Sending an event to every connected
// SSE client is ONE method, and its two consumers declare it themselves:
// internal/chat (chat and message lifecycle) and internal/forges (a forge
// connection change). internal/command declares the same method as a member of
// its ChatAccess role. *hub.Hub satisfies all three.

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

// There is no ACPBridge interface here, and no ACPBridgeFactory. The subprocess
// contract is declared at its consumer (internal/hub) at seven widths, because
// the hub asks for wildly different things at different sites: 14 methods for a
// per-chat bridge that starts, prompts, model-switches and stops, 6 for the
// utility session, 7 for the metadata persist, 2 for a lease, 1 each for the
// parameter builders and the idle culler. *bridge.Bridge satisfies the widest.
//
// StartOpts stays here: it is a type, and internal/bridge decodes it on the
// implementing side without importing the hub.

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

// There is no UtilityPrompter interface here. AI-backed prompt generation is a
// single method, and its two consumers declare it themselves: internal/server
// (explain-error, explain-diff) and internal/git (commit message, PR
// description, branch name). *hub.Hub satisfies both.

// --- Pending Changes ---

// There is no PendingStore interface. It existed for vibekit's own staging queue
// — SSE replay of staged ops, rejection on bridge teardown, full-content
// retrieval — and all three are gone with internal/pending. KAS's turn approval
// arrives as an ordinary permission request, so the permission tracker covers it.
