package vibekit

import "context"

// --- Persistence ---

// There is no ChatStore interface here. *chat.Store offers 9 methods and no
// consumer wants more than 5 of them:
//
//	internal/server        1   RegisterRoutes — the chat router owns its own HTTP surface
//	internal/translate     3   Get, Mutate, AppendMessage
//	internal/hub's coord   4   + BuildHistory
//	internal/command       5   Get, Mutate, AppendMessage, SetDraft, Delete
//	internal/hub's field   7   the union it passes on, not what it calls
//
// Two members were reached through no interface at all: RegisterRoutes (called
// on the concrete store) and UpdateMessage (called by nothing in production).

// --- Communication ---

// There is no Broadcaster interface here. Sending an event to every connected
// SSE client is ONE method, and its two consumers declare it themselves:
// internal/chat (chat and message lifecycle) and internal/forges (a forge
// connection change). internal/command declares the same method as a member of
// its ChatAccess role. *agent.Runtime satisfies all three.

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
// implementing side without importing the agent.

// --- HTTP ---

// There is no RouteHandler interface here, and it is the one whose measurement
// is worth keeping. Eight packages referenced it, but only ONE consumed it:
// internal/server, the router, which calls RegisterRoutes on the eight
// components it mounts. Five packages merely IMPLEMENTED it (auth, filebrowse,
// forges, git, mcp's Store and RegistryProxy) and an implementor is not a
// consumer — each of their var _ assertions was a claim the composition root
// already forced. So it is declared once, at the router, as an unexported
// routeHandler. internal/hub declares an exported RouteRegistrar for the one
// value it hands OUT.

// There is no PushService interface here. Its consumers declare what they use:
// internal/hub 4 of the 8 methods (send, ask, reload, close), internal/server 2
// (mount the routes, write the toggles), internal/forges 2 (its PRNotifier).
//
// Subscribe and Unsubscribe were members no consumer ever reached through an
// interface — *push.Service's own HTTP handlers call them on itself — so they
// are simply methods on the concrete type now.

// --- AI Utilities ---

// There is no UtilityPrompter interface here. AI-backed prompt generation is a
// single method, and its two consumers declare it themselves: internal/server
// (explain-error, explain-diff) and internal/git (commit message, PR
// description, branch name). *agent.Runtime satisfies both.

// --- Pending Changes ---

// There is no PendingStore interface. It existed for vibekit's own staging queue
// — SSE replay of staged ops, rejection on bridge teardown, full-content
// retrieval — and all three are gone with internal/pending. KAS's turn approval
// arrives as an ordinary permission request, so the permission tracker covers it.
