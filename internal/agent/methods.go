package agent

// ACP method name constants. Centralised so a protocol rename is a
// single-line change with compile-time verification of all consumers.

// v3 (KAS) extension notification method names (_kiro/* namespace).
// Several map onto shared domain handlers (rate_limit, customAgent
// errors, mcp/status); the rest are recognised-but-ignored (noopMethods)
// with a rationale.
const (
	methodV3MCPStatus            = "_kiro/mcp/status"                        // consolidated MCP server state (connected/failed/oauth)
	methodV3MCPResetServer       = "_kiro/mcp/resetServer"                   // C→A request: reconnect a named server; {serverName, startOAuth?} → {success}
	methodV3MCPGetPrompt         = "_kiro/mcp/getPrompt"                     // C→A request: resolve a prompt; {serverName, promptName, arguments} → {messages[]}
	methodV3MCPGetResource       = "_kiro/mcp/getResource"                   // C→A request: read a resource; {serverName, uri} → {contents[]}
	methodV3SessionsChanged      = "_kiro/sessions/changed"                  // session inventory diff (no client consumer on v3)
	methodV3RateLimit            = "_kiro/error/rate_limit"                  // {sessionId, message}
	methodV3CustomAgentNotFound  = "_kiro/customAgent/not_found"             // {sessionId, requestedAgent, fallbackAgent}
	methodV3CustomAgentConfigErr = "_kiro/customAgent/config_error"          // {sessionId, path, error}
	methodV3SystemNotify         = "_kiro/system/notify"                     // {level, message} — broadcast banner (see init_errors.go)
	methodV3Governance           = "_kiro/governance/state"                  // org/account feature-flag policy → governance_state SSE + GET /api/governance (HandleGovernanceState)
	methodV3ToolsDidChange       = "_kiro/tools/didChange"                   // recognised-ignored (tool catalog; vibekit fetches via REST)
	methodV3SteeringDocs         = "_kiro/steering/documents_changed"        // recognised-ignored (vibekit fetches via /api/workspace/kiro-config)
	methodV3ProgressiveContext   = "_kiro/progressive_context/items_changed" // recognised-ignored (skills/steering list; REST-sourced)
	methodV3Powers               = "_kiro/powers/items_changed"              // recognised-ignored (Kiro powers; not a vibekit surface)
	methodV3CodeReferences       = "_kiro/code_references"                   // licensed-code attributions → per-turn chip (HandleCodeReferences)
)

// v3 (KAS) native Cedar policy method names. list/explain are C→A requests
// vibekit issues on the utility bridge for the read-only policy VIEW;
// changed/error are A→C notifications KAS emits when a permissions.{yaml,json}
// file changes (chokidar hot-reload) or fails to parse. policy/check is
// deliberately NOT used: it calls session.acpToolApproval and raises a real
// session/request_permission, so it is unsafe as a UI pre-flight query —
// explain (a pure simulation, verified to raise no prompt) covers the "why".
const (
	methodV3PermissionsList    = "_kiro/permissions/list"    // C→A request: {sessionId, scope?} → {rules[]}
	methodV3PermissionsExplain = "_kiro/permissions/explain" // C→A request: {sessionId, capability|toolId, resource?} → {effect, matchedRule?, scope, source, isExplicitAsk}
	methodV3PolicyChanged      = "_kiro/policy/changed"      // A→C notification: {sessionId, status, errors?}
	methodV3PolicyError        = "_kiro/policy/error"        // A→C notification: {sessionId, errors[]}
)

// v3 (KAS) request method names outside the notification set above.
// openExternalUrl is an A→C request (answered in bridge_v3_auth.go);
// getUsage is a C→A request vibekit issues on the utility bridge for the
// account-usage footer (account_usage.go).
const (
	methodKiroOpenExternalURL = "_kiro/openExternalUrl"  // A→C request: {url} — open a URL for the user (MCP OAuth); needs the openExternalUrl client capability
	methodKiroGetUsage        = "_kiro/account/getUsage" // C→A request: account/subscription usage; needs profileArn in the getAccessToken reply
	methodKiroCodeIntel       = "_kiro/codeIntelligence" // C→A request: code-intelligence status/init (subcommand param); needs the session opted in via initialize _meta.kiro.settings
)

// KAS's own filesystem verbs, A→C, each gated on
// `clientCapabilities.fs._meta.kiro.<name> === true`. NOT declaring one does
// not remove the capability: the else-branch is KAS's in-process
// NodeFileSystem, so an undeclared verb is the same operation with no vibekit
// path check on it. Read and write have their own `_kiro/fs/{read_file,
// write_file}` rung which vibekit deliberately does NOT declare — that would
// bypass the supervised staging path on fs/{read,write}_text_file.
const (
	methodKiroFSStat          = "_kiro/fs/stat"
	methodKiroFSReadDirectory = "_kiro/fs/read_directory"
	methodKiroFSDelete        = "_kiro/fs/delete"
)

// v3 (KAS) credential-storage requests. All three are A→C ONLY — every param
// shape probed client→agent returns -32603, so there is nothing here for
// vibekit to CALL. KAS builds its AcpSecretStorage only when initialize
// declares `_meta.kiro.secretStorage: true`; without the flag the store is
// never constructed and every bridge spawn re-runs Dynamic Client Registration.
//
// Failure semantics differ per method and are load-bearing: KAS CATCHES a get
// failure (warns, treats the credential as absent) but RETHROWS a store or
// delete failure into the MCP connect path. So a get may degrade quietly and a
// store must not.
const (
	methodKiroSecretGet    = "_kiro/secret/get"
	methodKiroSecretStore  = "_kiro/secret/store"  //nolint:gosec // G101: ACP method name, not a credential
	methodKiroSecretDelete = "_kiro/secret/delete" //nolint:gosec // G101: ACP method name, not a credential
)

// The whole `_kiro/spec/*` family is deliberately unwired: `.kiro/specs/**/*.md`
// is already served by the file browser, and every invoke verb drives a
// fire-and-forget agent turn with no ACP turn-end signal, so a client cannot
// tell a finished task from a hung one.

// v3 (KAS) knowledge-base method names. _kiro/knowledge is a C→A request
// dispatched by a `subcommand` field (show/add/remove/update/clear/cancel);
// vibekit issues it on the utility bridge WITHOUT a sessionId so it targets
// the workspace-global default store, not any chat's session store.
// indexingStarted/indexingCompleted are deliberately not handled: they fire only
// for a non-builtin mode's PER-AGENT knowledge base, which is disjoint from the
// `default` store `GET /api/knowledge` reads, so refetching on that signal could
// never show the base it announced. Progress for a user `add` is polled via
// `show` instead.
const (
	methodKiroKnowledge      = "_kiro/knowledge"       // C→A request: {subcommand, ...} → {success, entries?/message?}
	methodKiroConfigTemplate = "_kiro/config/template" // C→A request (2.14+): {} → {modes:{availableModes,currentModeId}, configOptions[]} — session-less catalog
	// methodKiroWorkflowList enumerates workflow RUNS for a workspace.
	// workspacePaths is an ARRAY and is REQUIRED: every other param shape
	// (cwd, sessionId, a nested _meta) fails -32603 "workspacePaths is not
	// iterable". Needs no workflows capability (probed 2026-08-02).
	methodKiroWorkflowList    = "_kiro/workflow/list"    // C→A request: {workspacePaths[]} → {runs[]}
	methodKiroWorkflowInspect = "_kiro/workflow/inspect" // C→A request: {workflowId} → {workflowId, state, nodePlan}
)

// The nine A→C workflow lifecycle NOTIFICATIONS, exactly KAS's own
// `KIND_TO_METHOD` table. Every payload gets `parentSessionId` merged in when
// the run has a parent, and none carries a top-level `sessionId` except
// node_start, which carries the STEP's.
//
// They arrive on the launching chat's bridge, because KAS parents a run on the
// calling chat's session — so no session→chat resolution is needed. Translated
// to three SSE events; see api/domain_workflow.go for why three and not six.
const (
	methodWFRunStart      = "_kiro/workflow/run_start"      // {workflowId, workflowName, inputs, nodeTree[], parentSessionId?}
	methodWFRunComplete   = "_kiro/workflow/run_complete"   // {workflowId, status, finalState}
	methodWFNodeStart     = "_kiro/workflow/node_start"     // {workflowId, nodeId, nodePath[], type, agentName?, sessionId?, iteration?, branchId?}
	methodWFNodeComplete  = "_kiro/workflow/node_complete"  // {workflowId, nodeId, nodePath[], status, artifacts?, capturedOutput?}
	methodWFNodePaused    = "_kiro/workflow/node_paused"    // {workflowId, nodeId, nodePath[], reason} — note `reason`, not `pauseReason`
	methodWFPaused        = "_kiro/workflow/paused"         // {workflowId, pauseReason}
	methodWFLoopIteration = "_kiro/workflow/loop_iteration" // {workflowId, loopId, iteration, stopConditionMet}
	methodWFWatchPoll     = "_kiro/workflow/watch_poll"     // {workflowId, nodeId, nodePath[], outcome, at}
	methodWFStepsQueued   = "_kiro/workflow/steps_queued"   // {workflowId, pendingSteps[], resolution?}
)

// C→A workflow verbs vibekit issues (beyond list/inspect above).
//
//   - listRecipes: {workspacePaths[]} → {recipes[{name, description, source,
//     inputs, plan, builtIn}]}. `source` is the launch key: `bundled://<name>`
//     for a compiled-in recipe, an absolute *.workflow.json path for a
//     workspace one.
//   - new: {workflowPath, workspacePaths[], inputs} → {workflowId,
//     initialState}. workflowPath accepts a `bundled://` source verbatim; a
//     bare name is refused with "must end in '.workflow.json'". Validates
//     completely and writes nothing on failure. Passing NO parentSessionId is
//     what makes a run parentless.
//   - invoke: {workflowId} → fire-and-forget; the run executes on the process
//     that invoked it, and its lifecycle frames arrive on that connection.
//   - cancel: {workflowId} — a node-boundary verb: the in-flight node still
//     runs out before the recorded outcome changes.
//   - resume: {workflowId} → {status} — the recovery verb for a
//     restart-paused run.
const (
	methodKiroWorkflowListRecipes = "_kiro/workflow/listRecipes"
	methodKiroWorkflowNew         = "_kiro/workflow/new"
	methodKiroWorkflowInvoke      = "_kiro/workflow/invoke"
	methodKiroWorkflowCancel      = "_kiro/workflow/cancel"
	// methodKiroWorkflowDelete removes a run and its on-disk state: KAS cancels a
	// non-terminal run first (target status `aborted`), then removes the run
	// directory, answering {ok, previousStatus}. It is the only KAS verb that
	// takes a run OUT of `_kiro/workflow/list`, so it is what a History row's
	// delete has to reach — cancel only settles a run's status.
	methodKiroWorkflowDelete = "_kiro/workflow/delete"
	methodKiroWorkflowResume = "_kiro/workflow/resume"
	// methodKiroWorkflowPause: {workflowId} -> {paused:true}. Sets
	// control.pauseRequested; the run stops at the next NODE boundary, like
	// cancel.
	//
	// cancel additionally takes an optional `targetStatus` (default "aborted")
	// that vibekit does not send: the UI verb is "stop", and letting a client
	// choose which terminal status a stop records would make the run history
	// mean different things depending on which door was used.
	methodKiroWorkflowPause = "_kiro/workflow/pause"

	// methodKiroWorkflowRetry resets a finished run's FAILED and aborted nodes
	// plus their ancestors, leaving completed work alone. Legal only from
	// `failed`/`aborted`, and it rehydrates the run from disk, which is what
	// lets vibekit re-host a run whose bridge was closed at terminal status.
	methodKiroWorkflowRetry = "_kiro/workflow/retry"

	// methodKiroWorkflowUpdate mutates a live run. vibekit narrows it to
	// `set_step_status` (mark an in-flight step completed/failed so the run
	// advances); `replace_remaining` is a plan editor and is not wired.
	methodKiroWorkflowUpdate = "_kiro/workflow/update"
)

// v3 (KAS) hook-management method names. list/setEnabled are C→A requests
// vibekit issues on the utility bridge (which opts into the v2 hook engine via
// _meta.kiro.hooks={enabled,v2}). Both are gated: KAS answers them only when
// v2Hooks is enabled. didChange is an A→C notification KAS emits after a hook
// file changes.
//
// TWO names KAS still serves are deliberately ABSENT: `_kiro/hooks/triggerHook`
// (C→A) and `_kiro/hooks/executeHook` (A→C). The pair was Run-now, removed
// because answering executeHook is what made vibekit run `sh -c` on a command a
// hook file specifies. Naming a method here is what makes it reachable, so the
// names went with the feature.
const (
	methodKiroHooksList       = "_kiro/hooks/list"       // C→A request: {workspacePaths?,trigger?,toolId?,includeDisabled?} → {hooks[]}
	methodKiroHooksSetEnabled = "_kiro/hooks/setEnabled" // C→A request: {hookId, enabled} → {success, code?, error?}
	methodKiroHooksDidChange  = "_kiro/hooks/didChange"  // A→C notification: {hooks[]}
)

// v3 (KAS) Infrastructure-Safety method names. getProperties is a C→A request
// (reachable over acp: {sessionId} → {properties}, returns [] by default);
// propertiesChanged/statusChanged are A→C notifications KAS emits ONLY when the
// client's infrastructureSafety capability is declared AND an AWS governance
// flag (infraSafetyMonitor|infraSafetyEnforce) is on, which is off by default on
// individual/Builder-ID accounts. Properties are formalized by a remote MCP
// endpoint, never authored by vibekit — there is no set/toggle RPC.
const (
	methodV3SafetyGetProperties = "_kiro/safety/getProperties"     // C→A request (reachable; not wired — inert by default)
	methodV3SafetyPropertiesChg = "_kiro/safety/propertiesChanged" // A→C notification → safety_properties SSE
	methodV3SafetyStatusChanged = "_kiro/safety/statusChanged"     // A→C notification → safety_status SSE
)

// Terminal protocol method names (ACP terminal/* namespace). v3/KAS uses
// snake_case terminal/wait_for_exit (v2's camelCase terminal/waitForExit
// is gone).
const (
	methodTermPrefix      = "terminal/"
	methodTermCreate      = "terminal/create"
	methodTermOutput      = "terminal/output"
	methodTermRelease     = "terminal/release"
	methodTermWaitForExit = "terminal/wait_for_exit"
	methodTermKill        = "terminal/kill"
)
