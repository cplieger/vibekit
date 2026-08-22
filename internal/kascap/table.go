package kascap

// enabledMember is the one member name KAS reads inside a settings entry
// (isSettingEnabled returns val.enabled). Named rather than inlined because the
// spelling is invisible on the wire when it is wrong: an object without this
// exact key resolves to undefined, which is neither the true a feature needs nor
// the false a veto needs. Any future row that sends a runtime bool here must
// build its object around this constant for that reason.
const enabledMember = "enabled"

// enabled is the shape every _meta.kiro.settings entry vibekit SENDS takes. A
// helper rather than inline literals because KAS reads each one through the same
// absent-key-means-false resolver, so the shape is a contract shared by all of
// them rather than a coincidence repeated at each row.
func enabled() map[string]any { return map[string]any{enabledMember: true} }

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
agent/bridge_v3_auth.go).`,
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
(agent/agent_terminal.go) — so the capability is the whole integration:
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
agent/bridge_v3_secret.go against internal/secretstore. Declaring it is a
COMMITMENT: KAS rethrows a client-side store/delete failure into the MCP
connect path, so the handlers must answer on every bridge that declares
it, the utility bridge included.

Which is why it is CONDITIONAL rather than a literal true. The store is
best-effort (no configDir, or a mode internal/secretstore cannot verify as
0600, leaves it nil), and declaring the capability over a nil store made
every MCP OAuth connect fail on the -32603 from secretStoreResult — worse
than not offering it, because undeclared merely costs one DCR per spawn.
The runtime reads its store per spawn (StartOpts.SecretStorage); a false here
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
autofired hooks. See agent/hooks.go and agent/bridge_coord.go.`,
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
agent/code_intel.go). This is the client-owned settings channel KAS
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
		key:      "session_title_llm",
		door:     doorConnection,
		resolver: resolverSetting,
		value:    enabled(),
		send:     true,
		because: `session_title_llm turns on KAS's LLM session title: one
fire-and-forget call to its fast model (qdev::simple-task) on a session's
FIRST prompt, asking for a 3-to-6-word Title Case name for the
conversation, with a 15s abort budget. It ships dark upstream
(FEATURES.session_title_llm default false, in-source comment "Ships dark
… until the experiment ramps"), so without this row nothing generates one.

What it buys is the History page. GET /api/sessions reads each row's title
straight off _kiro/session/list, which is KAS's OWN stored title and never
vibekit's chat name, so a closed chat whose agent never called
update_session_information shows deriveSessionTitle's 80-char truncation of
the first prompt. This replaces that with a real name.

It also renames the TAB, and that is not separable. The title arrives on the
shared focus_update channel, and translate/focus.go's applyFocusTitle adopts
any title titleIsPromptDerived does not recognise — an LLM title is not
prompt-shaped, so it lands on Chat.Name about a second after the first
prompt. That is a rung-2 upgrade rather than a precedence change: it
replaces the local 80-char label and an agent focus title arriving later
still wins. There is no discriminator that would let History have it alone;
KAS emits a title-only focus_update here while the
update_session_information tool usually carries a description or status
too, but the tool's fields are all optional, so filtering on title-only
would drop a genuine agent rename to protect a truncation.

MUST be the connection door, and MUST NOT carry the schema's leading
underscore. KAS installs the bridge that makes this key readable inside
initialize, from clientCapabilities._meta.kiro.settings, so a session-door
row would never be consulted. And the authoritative schema
(@kiro/acp-type-covenant BaseAgentSettingsSchema) declares _sessionRecap
and friends with an underscore while the agent reads
isFeatureEnabled("session_title_llm") without one: rawKeys is the client's
object keys verbatim, so only the string in the call is read. The
underscore spelling validates and is silently ignored.

Residual cost, accepted rather than gated: the utility bridge shares this
handshake, so its own first prompt (a commit message, a PR description, an
error explanation) also generates one title per utility-session lifetime —
about one extra cheap call per 20 utility operations, since the session
recycles at maxUtilityPrompts. Suppressing it would need a third Spawn
boolean, which doubles the exhaustive golden matrix from four rows to
eight, and the title it produces reaches nothing: the utility session is
filtered out of History by toResumable, and a focus_update for a chat
record that does not exist is a no-op inside applyFocusTitle's Mutate.

Turn it off by withholding the row, not by sending false — absent reads as
false at the isFeatureEnabled site. KIRO_DISABLE_SESSION_TITLE_LLM=true in
the bridge environment is upstream's own kill switch and overrides this.`,
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
		key:      "policyPreset",
		door:     doorSession,
		resolver: resolverCapability,
		value:    []string{"read-workspace"},
		send:     true,
		because: `policyPreset restores the fs_read floor that KAS grants a bundled mode
and denies a custom one, which kiro-cli 2.19.1 turned from a latent asymmetry
into a silent capability loss.

The 2.19.1 mechanism: filterSearchResults now runs every grep_search and
file_search result through evaluateSingleResource({capability:"fs_read"}) and
admits only effect === "allow". mostRestrictive([]) returns an implicit ASK, so
"no matching rule" is a DROP, not a pass. The rule that normally makes that a
non-event is DEFAULT_AGENT_POLICY's fs_read allow ./** — and
resolveAgentPermissions hands that agent-scope policy ONLY to KAS-shipped
profiles: a user- or workspace-authored agent "stays fail-closed and contributes
no agent-scope rules".

vibekit is exactly the client that loses. It seeds ZERO Cedar rules by decision
(vibekit.md, Settings/Permissions), and its mode pill offers every workspace
custom agent as a one-click mode threaded to StartOpts.Mode. Measured in this
workspace: 44 .kiro/agents/*.md, 22 declaring a permissions block, and ZERO
declaring an fs_read rule of any effect — a declared block REPLACES the default
rather than extending it, so all 44 contribute none either way. So without this
row, a chat switched to any custom agent gets zero search results after the pin
bump, and file_search compounds it: hasMoreResults is computed from the
PRE-filter provider count while the FILTERED list is sliced, so the agent is
told an empty result set is "incomplete" and to keep refining a query that can
never return anything. Builtin and bundled modes are unaffected, and a subagent
under a builtin parent is safe (combineResults returns the parent's allow).

Why a preset and not a rule file: read-workspace resolves to
[FS_READ_WORKSPACE_RULE], the same object a builtin mode already gets, so this
grants NOTHING beyond the status quo for the default mode. It is session-scope,
so precedence by restrictiveness means it can never override a user or workspace
deny. And it needs no permissions.yaml, which keeps vibekit's seeds-zero-rules
posture intact — the alternative would have been writing a real allow rule to
disk, which is a standing policy decision this row deliberately does not touch.

It rides the SESSION door because resolvePresetIds reads _meta.kiro.policyPreset
off the session call's own _meta, on BOTH session/new and session/load, and the
value is NOT persisted in session metadata — so a key sent only on new dies at
the first resume. withSessionMeta covers both verbs already.

Two traps. validatePresetIds THROWS InvalidParamsError on an unknown id, so an
upstream rename of read-workspace would fail session/new OUTRIGHT rather than
degrade — every chat refusing to start, which is why VALID_PRESET_IDS is worth
watching at a bump. And this key is in neither the isSettingEnabled nor the
isFeatureEnabled population, so the census cannot see it and will not list it in
unclaimed.txt: this row is the only record it exists.`,
	},
	{
		key:      "userMemoryOptIn",
		door:     doorConnection,
		resolver: resolverSetting,
		send:     false,
		because: `WITHHELD, and withholding is also kiro-cli's OWN default, so this row
changes nothing on the wire. It exists to record the decision and what to watch.

kiro-cli 2.19.1 added a memory subsystem: a model-facing memory tool over a
JSONL store under the user's home, plus a scope-filtered index injected into msg0
at session creation. This key is the client's answer to it.

THE DEFAULT, measured three ways. kiro-cli's TUI maps its own setting
memory.enabled onto this key inside a loop that assigns only when the stored
value is a boolean, so it sends the key ONLY when the user set one explicitly and
omits it entirely otherwise. And that setting is not reachable: asking kiro-cli
for memory.enabled answers "is not a valid setting", and it is absent from the
settings-all listing. So upstream's default is an ABSENT key, which is exactly
what this row produces.

WHY NOT A VIBEKIT SETTING, today. A three-state control (follow / on / off) was
built and reverted, because every state is inert: the subsystem is
client-UNREACHABLE rather than merely dark. The experiment value comes from
featureConfig.get, whose registry is [env, experiment] with the "client"
precedence seat declared and never constructed, and neither memory key appears in
ENV_FEATURE_VARIABLES. No client key and no environment variable can turn it on,
so an On option would do nothing, and a shipped control that does nothing teaches
a reader to distrust the rest of the panel.

The one state with a future is OFF. KAS reads this key into a TRI-STATE, testing
hasOwnProperty before isSettingEnabled, and its gate vetoes only on an explicit
false. So absent means "no opinion, let the experiment decide" while
present-and-false is a refusal that survives a backend ramp. That veto is what to
reach for if a watch condition below fires; it buys nothing while the feature
cannot start.

WATCH, and the first two would bite silently. A "client" provider appearing in
buildFeatureConfigRegistry, or either memory key appearing in
ENV_FEATURE_VARIABLES: both make the gate reachable, and neither is in a census
regex, so the census will not report them. A backend ramp of
memory_external_enabled needs no upstream release at all, and with no row sending
false the first symptom would be a memory block appearing in prompts.

WHY IT WOULD BE DECLINED ANYWAY on this deployment, so the watch has its answer
ready. vibekit has ONE home directory and no authentication, so one store is
global and a user-preference entry written in one person's chat lands in
everyone's msg0. That store is affirmatively unreachable through vibekit's own
file surface, since internal/filebrowse deny-lists the home tree as credentials,
so a model would write entries no user can read or delete. Scoping collapses
here: the resident-scope resolver over the primary workspace path yields the
single bucket "workspace", and the repo resolver starts its walk at the parent
directory, so every repo under /workspace shares one scope and the feature's
headline scope-resident injection does not function. And the index is frozen at
session creation for a bridge that lives as long as its tab.

If this is ever sent as a veto the value must be exactly the enabled member set
false: isSettingEnabled reads that member unchecked, so an empty object yields
undefined, which is not false and does NOT veto. That is the inverse of the trap
every other settings row here guards against.`,
	},
	{
		key:      "memoryEnable",
		door:     doorConnection,
		resolver: resolverSetting,
		send:     false,
		because: `WITHHELD, and unlike its sibling userMemoryOptIn it must not be sent
even as false, because this key is read at TWO sites and only one of them is the
memory gate.

Site one is the memory channel fallback: resolveMemoryEnabled treats
isSettingEnabled(settings,"memoryEnable") as an isInsiderChannel substitute "for
older CLI nightly builds". That path is inert here, since eligibility needs the
experiment value to be the string "insider" and its default is false.

Site two is the reason for this row. The key is ALSO read through
isFeatureEnabled("memoryEnable"), feeding resolveRemoteToolAllowlist, where for
a kiro-cli client it appends the remote searchMemories tool. So sending it
allowlists a remote memory-search tool while the local store stays dark — a
half-on state with no upside: an allowlisted tool over a subsystem this
deployment vetoes.

Withholding is sufficient and correct at both sites: absent resolves false
through isSettingEnabled and through the bridged isFeatureEnabled provider
alike. The veto that actually matters is userMemoryOptIn's, and it is a
different key.`,
	},
	{
		key:      "streamingShellContent",
		door:     doorConnection,
		resolver: resolverCapability,
		send:     false,
		because: `WITHHELD, and unreachable rather than merely unwanted — the obvious
reason is the wrong one, which is why this row spells it out.

kiro-cli 2.19.1 added _kiro/tools/content_chunk, an A→C notification carrying
live shell output while a command runs, gated on this capability. The guess a
reader makes is that vibekit simply has not adopted it yet. The real gate is a
SECOND one the capability does not open: the producer is ExecuteBash's
StreamCoalescer fed by onOutputChunk, subscribed behind
if (input.onOutputChunk && term.onOutputChunk). vibekit declares terminal:true
with no sandbox, so KAS builds an ACPTerminal, whose entire method set is
runCommand, readOutputLines, ensureCommandRunsInCwd, close and focus — no
onOutputChunk — and whose runCommand awaits completion, so there is no mid-flight
output on this path at all. Declaring the flag would emit ZERO frames.

That also relocates the trap. The emit path returns, so declaring the capability
REPLACES the mid-flight tool_call_update for kind === "execute" rather than
adding to it — a real hazard, but for a client whose terminals KAS hosts
in-process (DefaultTerminalManager), not for this one.

And vibekit already ships the feature, better, because vibekit owns the pid:
pipe-rate 4 KiB reads against 32 ms coalescing, an explicit per-chunk UTF-16
offset against arrival order with no sequence number, and a 64 KiB rolling ring
that keeps the TAIL against a hard 256 KiB cap that keeps the head. That last
pair is the one that matters for a long build: the tail is where the error is.
Adopting would deliver a coarser, unordered second copy of bytes already on the
wire.

ANSI is NOT a difference, and an earlier revision of this row said it was.
sanitizeOutput filters only invisible Unicode (tag characters, zero-width and
bidi ranges); ESC is U+001B and is in none of its ranges, and the streaming
redactor only replaces four named env-var VALUES that are unset here. So escapes
reach the client intact either way. The confusion was a name collision with
vibekit's own SanitizeOutput, which IS StripANSI composed with SanitizeUnicode —
agent_terminal.go documents having been burned by exactly that, measuring spans=0
with it and spans=2 without.

The ONE condition that reverses this: a sandbox. The sandbox && capabilities.terminal
arm hands back DefaultTerminalManager, whose DefaultTerminal DOES implement
onOutputChunk — at which point vibekit's own terminal handlers go silent and the
stream moves to this frame. Sandbox is a standing proven negative in this
container (_kiro/sandbox/applyConfig is a silent no-op: bwrap needs an
unprivileged userns Docker's default profile forbids), so it holds, but it is one
compose change from not holding and the failure would be silent — a blank tool
card with the completion snapshot still filling in.

If the premise ever flips, the order is: a content_chunk handler registered and
green, THEN a toolCallId→card join (vibekit keys on terminal_id today), and only
THEN this row's send. Never the flag first.`,
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
