package kascap

// enabled is the shape every _meta.kiro.settings entry takes. A helper rather
// than four inline literals because KAS reads each one through the same
// absent-key-means-false resolver, so the shape is a contract shared by all of
// them rather than a coincidence repeated four times.
func enabled() map[string]any { return map[string]any{"enabled": true} }

// hooksValue is the v2 hook-engine opt-in object. Not a bare true: KAS requires
// an object carrying a v2 member and then checks that member (resolverObject).
func hooksValue() map[string]any { return map[string]any{"enabled": true, "v2": true} }

// table is every capability key vibekit knows about, sent or withheld.
//
// Row order is presentation only. Both builders emit maps, and encoding/json
// sorts map keys, so no wire byte depends on this order.
var table = []decl{
	{
		key:      "openExternalUrl",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
		because: `_meta.kiro.openExternalUrl opts into KAS's _kiro/openExternalUrl
request (open a URL for the user, e.g. an MCP OAuth page).
openExternalUrl advertises that we can open a URL for the user; KAS (v3) gates
its _kiro/openExternalUrl request on it (proactively opening an MCP server's
OAuth page — the client surfaces a clickable banner, no auto-open; see
hub/bridge_v3_auth.go).`,
	},
	{
		key:      "infrastructureSafety",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
		because: `_meta.kiro.infrastructureSafety opts into KAS's Infrastructure-Safety
gate for infrastructure-as-code tool calls. Safe-by-default: KAS installs
the gate only when this capability AND an AWS governance flag
(infraSafetyMonitor|infraSafetyEnforce) are both set, and that flag is off
by default on individual/Builder-ID accounts — so declaring it has zero
effect there (verified on a live probe: no gate, getProperties returns []).
It is required for the gate's statusChanged/propertiesChanged notifications
to ever surface (translate/safety.go); on an enterprise account with the
flag on, enforce mode can block infra-as-code writes remotely. Distinct
from supervised mode, which is KAS's autopilot gate (vibekit-acp.md).`,
	},
	{
		key:      "userInput",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
		because: `_meta.kiro.userInput opts into KAS's _kiro/userInput request (2.14+):
the agent's structured questions (plan-mode clarifications, spec
gates) arrive as answerable A→C requests with full option metadata
(descriptions, recommended, sub-options) instead of being flattened
into permission prompts — and free-form questions are SURFACED
instead of silently skipped (without the capability KAS advances
past them). Handled by translate/user_input.go; answered via the
user_input_response command.`,
	},
	{
		key:      "backgroundProcesses",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
		because: `_meta.kiro.backgroundProcesses opts into KAS's background-process tools
(control_bash_process, list_processes, get_process_output). KAS serves
them from its own ACPBackgroundProcessManager over standard
terminal/create + terminal/output, which vibekit already implements
(hub/agent_terminal.go) — so the capability is the whole integration:
without it the agent has no way to run a dev server or a watcher
without blocking its turn on a foreground command.`,
	},
	{
		key:      "knowledge",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
		because: `_meta.kiro.knowledge gates getKnowledgeListing into the system prompt,
i.e. it tells the agent WHICH knowledge bases are indexed (four lines
per base, undefined when none). vibekit ships the knowledge UI and
seeds chat.enableKnowledge, so the index exists and /knowledge works —
the agent just could not see what was in it.`,
	},
	{
		key:      "secretStorage",
		door:     doorConnection,
		resolver: resolverCapability,
		send:     true,
		// Always present, value from the spawn: a false is a real declaration
		// here, not an omission. See because.
		gate: func(s Spawn) (any, bool) { return s.SecretStorage, true },
		because: `_meta.kiro.secretStorage opts into KAS's AcpSecretStorage: it then asks
the client to HOLD the MCP OAuth credentials it derives (the DCR result,
the token set, the PKCE verifier) via _kiro/secret/{get,store,delete}.
KAS keeps only an in-process memory copy and has no file of its own, so
without this flag the store is never constructed and every bridge spawn
re-runs discovery and a fresh POST /register — measured, and measured to
stop at zero DCRs once a stored blob is replayed. Answered by
hub/bridge_v3_secret.go against internal/secretstore. Declaring it is a
COMMITMENT: KAS rethrows a client-side store/delete failure into the MCP
connect path, so the handlers must answer on every bridge that declares
it, the utility bridge included.

Which is why it is CONDITIONAL rather than a literal true. The store is
best-effort (no configDir, or a mode internal/secretstore cannot verify as
0600, leaves it nil), and declaring the capability over a nil store made
every MCP OAuth connect fail on the -32603 from secretStoreResult — worse
than not offering it, because undeclared merely costs one DCR per spawn.
The hub reads its store per spawn (StartOpts.SecretStorage); a false here
means KAS never asks, so the unanswerable request is never made.`,
	},
	{
		key:      "hooks",
		door:     doorConnection,
		resolver: resolverObject,
		send:     true,
		// Presence-gated, not value-gated: when hooks are off the key is absent
		// entirely, which is what the pre-kascap literal did with an if.
		gate: func(s Spawn) (any, bool) { return hooksValue(), s.Hooks },
		because: `hooks opts into KAS's v2 hook engine. Set on the utility bridge (so
_kiro/hooks/list|setEnabled|triggerHook are available for the
hooks-management dashboard) AND on chat bridges (so the workspace's
user-authored .kiro/hooks/*.json hooks autofire on their triggers
during an agent turn). In v2 mode KAS loads the hook files and runs
runCommand hooks internally — it does not call back the client to run
autofired hooks. See hub/hooks.go and hub/bridge_coord.go.`,
	},
	{
		key:      "codeIntelligence",
		door:     doorConnection,
		resolver: resolverSetting,
		value:    enabled(),
		send:     true,
		because: `_meta.kiro.settings.codeIntelligence opts every session into KAS's
native code tool (tree-sitter symbol navigation always; LSP-backed
rename/references/diagnostics once the workspace is initialized —
hub/code_intel.go). This is the client-owned settings channel KAS
reads into clientMeta (the sqlite chat.enableCodeIntelligence
setting does NOT apply to acp mode); lab-verified against 2.13.0.
Costs nothing when unused: with no lsp.json and no servers on
PATH the LSP operations degrade gracefully and tree-sitter still
works.`,
	},
	{
		key:      "knowledge",
		door:     doorConnection,
		resolver: resolverSetting,
		value:    enabled(),
		send:     true,
		because: `_meta.kiro.settings.knowledge is the THIRD part of the knowledge
gate, and without it the other two are decoration.
isSettingEnabled(settings, "knowledge") treats an absent key as
false, and that is the sole gate on KAS constructing its Knowledge
TOOL. So before this key: chat.enableKnowledge made the index
exist, _meta.kiro.knowledge told the agent WHAT was indexed, and
no tool existed to read it. vibekit shipped the whole knowledge UI,
the REST surface, the progress polling and a system-prompt listing
over a store the agent could not query, silently in both
directions (no error, no -32601). vibekit.md says "both are
needed"; there are three.`,
	},
	{
		key:      "workflows",
		door:     doorConnection,
		resolver: resolverSetting,
		value:    enabled(),
		send:     true,
		because: `_meta.kiro.settings.workflows gates the agent's workflow TOOLS the
same way: resolveWorkflows resolves an absent key to false, which
removes the whole workflowChatTools array (run_workflow,
inspect_workflow, update_workflow, validate_workflow, send_message)
plus the workflow steering doc. vibekit drives the workflow surface
from the CLIENT side (POST /api/runs, GET /api/recipes, the
/docs/workflows tab, a per-run bridge), so the run half worked while
the agent had no way to reach a workflow itself.`,
	},
	{
		key:      "subagentOrchestration",
		door:     doorConnection,
		resolver: resolverSetting,
		value:    enabled(),
		send:     true,
		// The two tool names below carried markdown backticks as comment
		// decoration in bridge.go. A raw string cannot hold a backtick, so the
		// decoration is dropped; every word is verbatim.
		because: `subagentOrchestration swaps the agent's delegation tool: absent
gives one-shot invoke_sub_agent, present gives
orchestrate_subagent, which wraps the same invoke config and adds
pipeline stages with depends_on and bounded loops. Same
absent-means-false resolver as the two keys above, and kiro-cli's
own TUI sends it, so withholding it diverged vibekit's agent from
the reference client's for no stated reason.

The cost this does NOT remove, recorded because it is the reason to
think twice before reaching for a pipeline: a subagent has no
session of its own, so its whole run lands in the PARENT
transcript and every later turn re-bills it. Measured elsewhere in
this fleet at 48,931 bytes for a trivial delegation against 3,018
for the same work as a workflow step. So this key makes pipelines
expressible, not cheap; real fan-out still belongs in a workflow
run, and A4.2's tool-output cap is what bounds the damage when the
agent chooses otherwise.`,
	},
	{
		key:        "semanticReview",
		door:       doorConnection,
		resolver:   resolverSetting,
		absentTrue: true,
		send:       false,
		because: `WITHHELD, and withholding it is what turns it ON. semanticReview is the
one settings key KAS does not read through isSettingEnabled: its resolver
is parsed2.data.semanticReview?.enabled ?? persistedDefault ?? true, so an
absent key resolves TRUE where every other settings key resolves false.
vibekit therefore gets the semantic-reviewer subagent in autonomous mode
today by saying nothing at all.

This row exists because that was previously unrecordable. A map literal
has no line for a key it omits, so the state vibekit relies on read as an
oversight, and the obvious "fix" of adding semanticReview: {enabled: true}
alongside the others would have looked like tightening a gap while
changing nothing. The trap is the opposite direction: sending
{"enabled": false} here is the only way to disable it, and a future row
that sets send:true must carry a value deliberately rather than inherit
the enabled() shape the other settings rows use.`,
	},
}
