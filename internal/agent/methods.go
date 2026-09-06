package agent

// ACP method name constants, centralised so a protocol rename is one edit.

// v3 (KAS) extension notification method names (_kiro/* namespace). Several map onto
// shared domain handlers; the rest are recognised-but-ignored (noopMethods).
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

// v3 (KAS) native Cedar policy method names. list/explain are C→A requests vibekit
// issues on the utility bridge for the read-only policy VIEW; changed/error are A→C
// notifications. policy/check is deliberately NOT used: it raises a real
// session/request_permission, so it is unsafe as a UI pre-flight query, and explain
// is a pure simulation verified to raise no prompt.
const (
	methodV3PermissionsList    = "_kiro/permissions/list"    // C→A request: {sessionId, scope?} → {rules[]}
	methodV3PermissionsExplain = "_kiro/permissions/explain" // C→A request: {sessionId, capability|toolId, resource?} → {effect, matchedRule?, scope, source, isExplicitAsk}
	methodV3PolicyChanged      = "_kiro/policy/changed"      // A→C notification: {sessionId, status, errors?}
	methodV3PolicyError        = "_kiro/policy/error"        // A→C notification: {sessionId, errors[]}
)

// v3 (KAS) request method names outside the notification set above. openExternalUrl
// is an A→C request (bridge_v3_auth.go); the other two are C→A on the utility bridge.
const (
	methodKiroOpenExternalURL = "_kiro/openExternalUrl"  // A→C request: {url} — open a URL for the user (MCP OAuth); needs the openExternalUrl client capability
	methodKiroGetUsage        = "_kiro/account/getUsage" // C→A request: account/subscription usage; needs profileArn in the getAccessToken reply
	methodKiroCodeIntel       = "_kiro/codeIntelligence" // C→A request: code-intelligence status/init (subcommand param); needs the session opted in via initialize _meta.kiro.settings
)

// methodKiroSessionNotify is the A→C notification KAS's `send_message` builtin
// raises: `{sessionId, callerSessionId, message, severity, workflowId?, nodeId?}`.
//
// The `message` is the ONLY carrier of a workflow step's question: KAS's pause writes
// one fixed literal with an empty `pauseDetail`, so `inspect` says a step wants input
// and never what it asked. `callerSessionId` is the PAUSED STEP's own session, and a
// `session/prompt` addressed to it is what KAS reroutes back into the run.
const methodKiroSessionNotify = "_kiro/session/notify"

// KAS's own filesystem verbs, A→C, each gated on
// `clientCapabilities.fs._meta.kiro.<name>`. NOT declaring one does not remove the
// capability: the else-branch is KAS's in-process NodeFileSystem, so an undeclared
// verb is the same operation with no vibekit path check on it. The read/write rung is
// deliberately NOT declared — it would bypass supervised staging.
const (
	methodKiroFSStat          = "_kiro/fs/stat"
	methodKiroFSReadDirectory = "_kiro/fs/read_directory"
	methodKiroFSDelete        = "_kiro/fs/delete"
)

// v3 (KAS) credential-storage requests, A→C ONLY: every param shape probed
// client→agent returns -32603. KAS builds its store only when initialize declares
// `_meta.kiro.secretStorage: true`; without the flag every spawn re-runs DCR.
//
// Failure semantics are load-bearing: KAS CATCHES a get failure and treats the
// credential as absent, but RETHROWS a store or delete failure into the MCP connect
// path. So a get may degrade quietly and a store must not.
const (
	methodKiroSecretGet    = "_kiro/secret/get"
	methodKiroSecretStore  = "_kiro/secret/store"  //nolint:gosec // G101: ACP method name, not a credential
	methodKiroSecretDelete = "_kiro/secret/delete" //nolint:gosec // G101: ACP method name, not a credential
)

// The whole `_kiro/spec/*` family is deliberately unwired: every invoke verb drives a
// fire-and-forget agent turn with no ACP turn-end signal, so a client cannot tell a
// finished task from a hung one.

// v3 (KAS) knowledge-base method names. _kiro/knowledge is a C→A request dispatched
// by a `subcommand` field, issued on the utility bridge WITHOUT a sessionId so it
// targets the workspace-global default store. indexingStarted/indexingCompleted are
// not handled: they fire only for a PER-AGENT base disjoint from the `default` store
// `GET /api/knowledge` reads, so a refetch could never show what they announced.
const (
	methodKiroKnowledge      = "_kiro/knowledge"       // C→A request: {subcommand, ...} → {success, entries?/message?}
	methodKiroConfigTemplate = "_kiro/config/template" // C→A request (2.14+): {} → {modes:{availableModes,currentModeId}, configOptions[]} — session-less catalog
	// workspacePaths is an ARRAY and is REQUIRED: every other param shape fails
	// -32603 "workspacePaths is not iterable". Needs no workflows capability.
	methodKiroWorkflowList    = "_kiro/workflow/list"    // C→A request: {workspacePaths[]} → {runs[]}
	methodKiroWorkflowInspect = "_kiro/workflow/inspect" // C→A request: {workflowId} → {workflowId, state, nodePlan}
)

// The nine A→C workflow lifecycle NOTIFICATIONS, exactly KAS's own KIND_TO_METHOD
// table. Every payload gets `parentSessionId` merged in when the run has a parent,
// and none carries a top-level `sessionId` except node_start, which carries the
// STEP's. They arrive on the launching chat's bridge, because KAS parents a run on
// the calling chat's session, so no session→chat resolution is needed.
const (
	methodWFRunStart     = "_kiro/workflow/run_start"     // {workflowId, workflowName, inputs, nodeTree[], parentSessionId?}
	methodWFRunComplete  = "_kiro/workflow/run_complete"  // {workflowId, status, finalState}
	methodWFNodeStart    = "_kiro/workflow/node_start"    // {workflowId, nodeId, nodePath[], type, agentName?, sessionId?, iteration?, branchId?}
	methodWFNodeComplete = "_kiro/workflow/node_complete" // {workflowId, nodeId, nodePath[], status, artifacts?, capturedOutput?}
	methodWFNodePaused   = "_kiro/workflow/node_paused"   // {workflowId, nodeId, nodePath[], reason} — note `reason`, not `pauseReason`
	// `pauseDetail` is what the heal READS: the reason is prose a second KAS path
	// re-renders (a pause inside a parallel branch arrives as a wrapper sentence
	// composed FROM the detail), while `{class, code, occurredAt}` is correct in
	// both cases. Absent for an interruption, a permanent failure and a
	// need-input park.
	methodWFPaused        = "_kiro/workflow/paused"         // {workflowId, pauseReason, pauseDetail?}
	methodWFLoopIteration = "_kiro/workflow/loop_iteration" // {workflowId, loopId, iteration, stopConditionMet}
	methodWFWatchPoll     = "_kiro/workflow/watch_poll"     // {workflowId, nodeId, nodePath[], outcome, at}
	methodWFStepsQueued   = "_kiro/workflow/steps_queued"   // {workflowId, pendingSteps[], resolution?}
)

// C→A workflow verbs vibekit issues (beyond list/inspect above).
//
//   - listRecipes: `source` is the launch key, `bundled://<name>` or an absolute
//     *.workflow.json path.
//   - new: writes nothing on failure; NO parentSessionId makes a run parentless.
//   - invoke is fire-and-forget; cancel and resume are node-boundary verbs.
const (
	methodKiroWorkflowListRecipes = "_kiro/workflow/listRecipes"
	methodKiroWorkflowNew         = "_kiro/workflow/new"
	methodKiroWorkflowInvoke      = "_kiro/workflow/invoke"
	methodKiroWorkflowCancel      = "_kiro/workflow/cancel"
	// The only KAS verb that takes a run OUT of `_kiro/workflow/list`, so it is
	// what a History row's delete has to reach; cancel only settles a status.
	methodKiroWorkflowDelete = "_kiro/workflow/delete"
	methodKiroWorkflowResume = "_kiro/workflow/resume"
	// The run stops at the next NODE boundary, like cancel. cancel's optional
	// `targetStatus` is deliberately not sent: letting a client choose which
	// terminal status a stop records would make history mean different things
	// depending on which door was used.
	methodKiroWorkflowPause = "_kiro/workflow/pause"

	// Resets a finished run's FAILED and aborted nodes plus their ancestors. Legal
	// only from `failed`/`aborted`, and it rehydrates the run from disk, which is
	// what lets vibekit re-host a run whose bridge closed at terminal status.
	methodKiroWorkflowRetry = "_kiro/workflow/retry"

	// Mutates a live run. vibekit narrows it to a step-status update (mark a step
	// completed/failed so the run advances); `replace_remaining` is a plan editor
	// and is not wired.
	methodKiroWorkflowUpdate = "_kiro/workflow/update"
)

// v3 (KAS) hook-management method names. list/setEnabled are C→A requests on the
// utility bridge and are gated on v2Hooks; didChange is an A→C notification.
// `_kiro/hooks/triggerHook` and `_kiro/hooks/executeHook` are deliberately ABSENT:
// answering executeHook is what made vibekit run `sh -c` on a command a hook file
// specifies, and naming a method here makes it reachable.
const (
	methodKiroHooksList       = "_kiro/hooks/list"       // C→A request: {workspacePaths?,trigger?,toolId?,includeDisabled?} → {hooks[]}
	methodKiroHooksSetEnabled = "_kiro/hooks/setEnabled" // C→A request: {hookId, enabled} → {success, code?, error?}
	methodKiroHooksDidChange  = "_kiro/hooks/didChange"  // A→C notification: {hooks[]}
)

// v3 (KAS) Infrastructure-Safety method names. getProperties is a C→A request
// returning [] by default; propertiesChanged/statusChanged are A→C notifications KAS
// emits ONLY when the client's infrastructureSafety capability is declared AND an AWS
// governance flag is on, off by default on individual accounts. Properties are
// formalized by a remote MCP endpoint, never authored here — there is no set RPC.
const (
	methodV3SafetyGetProperties = "_kiro/safety/getProperties"     // C→A request (reachable; not wired — inert by default)
	methodV3SafetyPropertiesChg = "_kiro/safety/propertiesChanged" // A→C notification → safety_properties SSE
	methodV3SafetyStatusChanged = "_kiro/safety/statusChanged"     // A→C notification → safety_status SSE
)

// Terminal protocol method names. v3/KAS uses snake_case terminal/wait_for_exit
// (v2's camelCase terminal/waitForExit is gone).
const (
	methodTermPrefix      = "terminal/"
	methodTermCreate      = "terminal/create"
	methodTermOutput      = "terminal/output"
	methodTermRelease     = "terminal/release"
	methodTermWaitForExit = "terminal/wait_for_exit"
	methodTermKill        = "terminal/kill"
)
