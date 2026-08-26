package vibekit

import "context"

// --- Persistence ---

// There is no ChatStore interface here. *chat.Store offers 9 methods and no
// consumer wants more than 5 of them:
//
//	internal/server        1   RegisterRoutes — the chat router owns its own HTTP surface
//	internal/translate     3   Get, Mutate, AppendMessage
//	internal/agent's coord   4   + BuildHistory
//	internal/command       5   Get, Mutate, AppendMessage, SetDraft, Delete
//	internal/agent's field   7   the union it passes on, not what it calls
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
	// Pass the runtime's shutdown context, not a request or turn context.
	Lifetime    context.Context
	SessionID   string
	Model       string
	Effort      string
	AgentEngine string
	Mode        string
	// Presets are the KAS policy-preset ids this session opens with, resolved
	// from the active security profile (policyfile.Profile). They ride
	// _meta.kiro.policyPreset on BOTH session/new and session/load, because KAS
	// re-reads them on each and persists neither.
	//
	// EMPTY is a real value, not an omission: it is the Custom profile, where the
	// permissions files are the whole policy, and it withholds the wire key
	// entirely rather than sending an empty array.
	//
	// It is a per-SESSION field rather than a live one because KAS offers no way
	// to change a running session's policy — there is no set_config_option id for
	// it and no client-callable setter — so a profile change reaches a chat when
	// its session next starts or loads, which is the intended path rather than a
	// gap. Set on the utility bridge too, since that
	// is the session answering GET /api/permissions, and the profile's own rules
	// are only readable there.
	Presets []string
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
	// internal/agent/hooks.go and internal/agent/bridge_coord.go.
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
	// whether the runtime actually opened a store (internal/secretstore), which is
	// best-effort — no configDir, or a mode that cannot be verified, leaves it
	// nil. Undeclared, KAS keeps its own in-process copy and re-runs discovery
	// and `POST /register` per spawn, which is the documented degradation and
	// what vibekit did before the store existed.
	//
	// Set on BOTH chat bridges and the utility bridge: the capability rides the
	// shared initialize, so whoever declares it must be able to answer.
	SecretStorage bool
	// ToolSearch and Knowledge are the two user settings that have to reach the
	// AGENT, and they land on `_meta.kiro.settings.{toolSearch,knowledge}` through
	// kascap's gates rather than on kiro-cli's own settings file.
	//
	// Both were kiro-cli settings until 2026-08, written through
	// /api/kiro-settings as `toolSearch.enabled` and `chat.enableKnowledge`. That
	// endpoint reaches the TUI and the index builder; measured against the stock
	// 2.19.2 bundle, it reaches no running chat, because KAS's ACP path reads no
	// kiro-cli setting at all. So each control kept its meaning and changed door.
	//
	// Resolved per SPAWN from the settings file (internal/agent's kasSettings), so
	// a flip reaches the next bridge. Neither is a live switch: KAS resolves both
	// at session creation and freezes them for the session's life, which is what
	// the UI hint has to say.
	//
	// Knowledge gates BOTH knowledge rows — the `knowledge` capability that puts
	// the index listing in msg0 and the `knowledge` setting that makes KAS build
	// the Knowledge tool. Gating only one reproduces the defect that made the
	// third key necessary in the first place: a UI over a store the agent cannot
	// query, or a listing of bases it has no tool to read. It does NOT reach
	// `_kiro/knowledge`, whose handler consults neither key, so the REST surface
	// and its panel keep working with the switch off.
	ToolSearch bool
	Knowledge  bool
	// Memory opts INTO kiro-cli's memory subsystem, and it is the only field here
	// that moves TWO levers, because neither alone decides the question.
	//
	// The `userMemoryOptIn` kascap row carries the veto and the child environment's
	// KIRO_FEATURE_MEMORY_EXTERNAL_ENABLED carries eligibility. Off SENDS
	// `{"enabled": false}` rather than withholding the key: KAS reads it as a
	// tri-state through hasOwnProperty, and only an explicit false vetoes, so an
	// absent key means "let the experiment decide" — which is the state an
	// AWS-side ramp turns on silently.
	//
	// The environment half is not redundant with the row and not a kill switch on
	// its own. resolveMemoryEnabled consults AB_MEMORY_INTERNAL first and falls
	// through to the external arm only when the internal one reads "disabled", and
	// AB_MEMORY_INTERNAL is absent from ENV_FEATURE_VARIABLES — so the variable is
	// the only lever that can turn memory ON and the row is the only one that can
	// keep it OFF against both arms.
	//
	// Resolved per SPAWN like its two siblings, and not live for a second reason
	// beyond theirs: the environment is fixed when the subprocess starts, so even a
	// KAS that re-read the gate could not see a flip. Reaches NEW chats only.
	Memory bool
}

// There is no ACPBridge interface here, and no ACPBridgeFactory. The subprocess
// contract is declared at its consumer (internal/agent) at seven widths, because
// the runtime asks for wildly different things at different sites: 15 methods for a
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
// routeHandler. internal/agent declares an exported RouteRegistrar for the one
// value it hands OUT.

// There is no PushService interface here. Its consumers declare what they use:
// internal/agent 4 of the 8 methods (send, ask, reload, close), internal/server 2
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
