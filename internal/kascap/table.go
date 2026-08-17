package kascap

// enabled is the shape every _meta.kiro.settings entry takes. A helper rather
// than four inline literals because KAS reads each one through the same
// absent-key-means-false resolver, so the shape is a contract shared by all of
// them rather than a coincidence repeated four times.
func enabled() map[string]any { return map[string]any{"enabled": true} }

// hooksValue is the v2 hook-engine opt-in object. Not a bare true: KAS requires
// an object carrying a v2 member and then checks that member (resolverObject).
func hooksValue() map[string]any { return map[string]any{"enabled": true, "v2": true} }

// envWorkflows is the operator off switch for the workflows row. Named after the
// capability rather than the fix, because a variable an operator reads in a
// compose file has to say what it controls; the VIBEKIT_ prefix is this app's
// (WT_ is reserved for the two names web-terminal-kiro reads too).
const envWorkflows = "VIBEKIT_AGENT_WORKFLOWS"

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
		door:     doorSession,
		resolver: resolverSetting,
		env:      envWorkflows,
		value:    enabled(),
		send:     true,
		because: `_meta.kiro.settings.workflows gates the agent's workflow TOOLS the
same way: resolveWorkflows resolves an absent key to false, which
removes the whole workflowChatTools array (run_workflow,
inspect_workflow, update_workflow, validate_workflow, send_message)
plus the workflow steering doc. vibekit drives the workflow surface
from the CLIENT side (POST /api/runs, GET /api/recipes, the
/docs/workflows tab, a per-run bridge), so the run half worked while
the agent had no way to reach a workflow itself.

It rides the SESSION door, and that is the whole defect this row
records. KAS resolves it per session, not per connection: the only
readers are createNewSessionState, which calls resolveWorkflows(parsed2)
over parseSettings(kiroMeta?.settings) off the session call's own _meta
with NO persisted default, and hydrateSessionForLoad, which passes
persisted.metadata.workflowsEnabled as that default. Nothing on the
connection door reads settings.workflows at all — on 2.18.0 the literal
isSettingEnabled key set is codeIntelligence, goal, inlineAgents,
knowledge, subagentOrchestration, toolSearch and _providerPowers, and
workflows is absent from every isSettingEnabled AND every
isFeatureEnabled call in the bundle (the latter matters because a
connection-door closure bridges initialize's settings onto the feature-flag
provider, so a key read that way WOULD be connection-scoped) — so
declaring it at
initialize resolved absent-to-false on every session and cost the agent
the entire workflow tool array with no error, no log and no -32601.

Sent on session/load as well as session/new, because the persisted
default only carries a value a PREVIOUS create put there: every session
created before this row existed persisted workflowsEnabled false, so a
resumed chat would keep losing the capability a fresh one has. The
client value wins over the persisted one, which is what lets a load
repair such a session.

What makes the session door work at all: KiroSessionMetaSchema declares
no settings field, but it ends in .passthrough(), so the key survives
parseKiroMeta rather than being stripped. That is a property of somebody
else's schema, which is why TestSessionNewCarriesWorkflowsAtSessionDoor
pins the wire and the census pins the version it was read from.

Carries an env override because it is the one row here that changes what
the AGENT can do rather than what it can see, and it creates
agent-origin workflow runs in a tier with no run supervisor. See the env
column: VIBEKIT_AGENT_WORKFLOWS=false stops sending it.`,
	},
	{
		key:      "goal",
		door:     doorConnection,
		resolver: resolverSetting,
		value:    enabled(),
		send:     true,
		because: `_meta.kiro.settings.goal makes KAS's own /goal parser reachable.
Two sites read it, both off the stored initialize block
(isSettingEnabled(this.clientMeta?.settings ?? {}, "goal"), so this is a
CONNECTION-door key even though its sibling workflows is not): the
slash-command source that publishes /goal in available_commands_update,
and the prompt path, where parseGoalCommand turns "/goal <text> [--max
N]" into launchGoal — a bundled repeat workflow that iterates toward the
goal until it self-declares success or the iteration budget runs out.

Worth sending for one reason: without it, typed /goal reaches the MODEL
as prose and the model answers as though it had run something, which is
exactly the lie typed /compact used to tell. With it, the verb either
launches a real run or is not offered.

This row is LOAD-BEARING for a real affordance, not a muscle-memory
fallback: the composer's chat-actions menu has a Set-a-goal row, and it
composes exactly "/goal <text> [--max N]" and sends it through the
ordinary prompt path, because the parser is the only route that can set
the iteration bound. The bundled recipe's repeat node is written
maxIterations: 200 and launchGoal applies the user's bound by mutating
that node on a clone, so launching the recipe by source instead bounds
every goal at 200. Stop sending this key and that row goes back to
reaching the model as prose.

vibekit still does not decode available_commands_update and ships no
palette, so the TYPED verb is discoverable only to a user who already
knows it; the menu row is the discoverable door. Note the loop it starts
is an ordinary workflow run parented on the calling session, so it lands
in the same unsupervised population as an agent-launched run — and its
frames arrive on the calling chat's topic, which is why the row opens no
run tab.`,
	},
	{
		key:      "workspaceTrusted",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
		because: `workspaceTrusted is the trust verdict every workspace-scoped read in
KAS is gated on, and vibekit sends true because that is what it already
gets: the mount is the user's own repository tree, the container hands the
agent a root shell over it, and nothing about that is safer for the agent
reading the repo's own steering files.

What it gates, measured in 2.18.0: scanNestedAgentsMd returns an empty
map outright when it is false (the only hard gate on the nested AGENTS.md
walk); NodeSteeringDocumentSource and ProgressiveContextManager filter
workspace-scoped docs and items out; executionAllowed(v2) is exactly
this flag, so v2 hooks load but never fire
("hooks.v2.executionDisabledUntrustedWorkspace");
buildUntrustedAutoloadAskRules injects ask rules over the config
directories; MCPConfigManager skips the workspace server files; and the
agent-profile watcher is not started.

Sending it changes NOTHING today, and that is deliberate rather than a
happy accident: this version takes workspaceTrusted as a KiroAgent
CONSTRUCTOR option and BOTH entry points hardcode it to true, so no
client key by this name is read anywhere in the bundle — resolveCapabilities
does not map it and neither clientMeta nor kiroMeta is ever its receiver.
The row's value is therefore the record, not the mechanism: it states
which side of the gate vibekit means to be on, in the one place a reader
looks for that, so an upstream release that starts reading a client key
finds vibekit's answer already written down instead of inheriting a
default nobody chose. It is also the same gate that widens the
untrusted-repository surface in a filed security report, which is the
reason to want the answer visible as a line a human can flip rather than
implied by silence.

Harmless to send meanwhile: initialize reads _meta.kiro as a plain object
(no schema, no strict()), so an unread key is ignored. If this row ever
needs to be false, the WITHHOLDING is what expresses it — an absent key
reads as false at every one of the sites above.`,
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
		key:      "sessionEviction",
		door:     doorSession,
		resolver: resolverSetting,
		send:     false,
		because: `WITHHELD, pending a probe that prices it. sessionEviction is an
opt-in disk budget: resolveSessionEviction reads {enabled, maxBytes} off
the session call's own settings, and when enabled createNewSessionState
fires checkStorageBudget, which calls runSessionEviction to DELETE the
least recently modified sessions until the tree is under budget.

The reason to withhold it is not cost, it is authority. vibekit already
owns retention end to end: chat_retention_days drives its own reaper, and
kiro-cli's competing purge is pinned off (cleanup.periodDays=0) for
exactly this reason. Turning this on would install a SECOND retention
authority with a different key (bytes, not age), a different unit of
deletion (a KAS session, not a vibekit chat) and no knowledge of the
chain: a chat's acp_session_id plus its prior_acp_session_ids are one
session chain, retention keys on the whole chain, and an LRU that walks
sessions by mtime would happily evict an earlier segment of a LIVE chat's
chain. Nothing in this tier would notice, and the visible symptom would be
a chat whose older turns stopped replaying.

What a probe has to answer before this can flip: whether eviction
respects a session vibekit still references, what the default budget is
against a real /config volume, and whether the reaper and the budget can
be expressed as one policy rather than two. Until then the disk is
bounded by the reaper, which is the authority that knows about chains.`,
	},
	{
		key:      "specPlan",
		door:     doorSession,
		resolver: resolverSetting,
		send:     false,
		because: `WITHHELD, pending a probe that prices it. specPlan is not a display
toggle: resolveSpecPlan reads {enabled, workflow, skipClarification} per
session, and enabling it flips the bundled prompt arms
({{#specPlanEnabled}} blocks) from "explore and plan" to "MANDATORY: you
MUST use spec-driven planning", adds subagent/create-spec to the agent's
declared delegation set, and persists specPlanEnabled + specWorkflow onto
the session record so the choice survives a reload.

So it changes what the agent DOES on an ordinary prompt, and it points
that behaviour at a spec surface vibekit does not have: /specs was
deleted (the board's write side could not work, since every
_kiro/spec/invoke verb drives a fire-and-forget turn with no ACP
turn-end signal), and specs are documents on the /docs tab now. Sending
this would make the agent produce and delegate against artifacts the UI
can only browse, and it would do it two tiers before anything in vibekit
can drive a spec.

What a probe has to answer before this can flip: which spec artifacts a
run actually writes and where, whether create-spec's turns are observable
on this wire at all, and whether 'quick' or 'full' is the right workflow
for a chat-first client. Note skipClarification defaults to TRUE, so a
naive enable also silences the clarification round.`,
	},
	{
		key:      "specPhaseCheckpoints",
		door:     doorConnection,
		resolver: resolverCapability,
		send:     false,
		because: `WITHHELD, pending a probe that prices it. specPhaseCheckpoints is a
capability KAS lifts onto its agentContext at initialize
(specPhaseCheckpoints: capabilities?.specPhaseCheckpoints) and reads as
=== true when building the spec-mode prompt, where it selects a different
workflow-selection process and injects a "# Phase Checkpoints" section.

It is a promise about the CLIENT, not a feature request: declaring it
tells the agent to stop at phase boundaries and expect the client to
carry the user across them. vibekit has no spec surface to stop at, so
the checkpoints would land as prose in a chat transcript and the agent
would wait for an affordance that does not exist.

Recorded rather than left absent because the census reads it off the
bundle every version, and an unclaimed line invites somebody to claim it.
The order is: a spec surface first, then this.`,
	},
	{
		key:      "requirementsAnalysis",
		door:     doorConnection,
		resolver: resolverCapability,
		send:     false,
		because: `WITHHELD, pending a probe that prices it. requirementsAnalysis is the
sibling of specPhaseCheckpoints on the same lift
(requirementsAnalysis: capabilities?.requirementsAnalysis, also read off
clientMeta) and the same === true gate in the spec-mode prompt builder,
where it turns on a requirements-analysis step ahead of the plan.

Same reasoning and the same blocker: it reshapes what a spec-mode turn
produces, for a spec surface vibekit does not ship. Withholding leaves
the prompt on the arm vibekit can actually render.

Both spec capabilities are cheap to flip once there is somewhere for
their output to go, and neither is a security decision, which is why they
are recorded together as a pair rather than argued separately.`,
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
