package agent

// v3 (KAS) extension notifications (_kiro/* namespace).
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

// v3 (KAS) native Cedar policy methods. policy/check is deliberately NOT used:
// it calls session.acpToolApproval and raises a real session/request_permission,
// so it is unsafe as a UI pre-flight query; explain simulates without prompting.
const (
	methodV3PermissionsList    = "_kiro/permissions/list"    // C→A request: {sessionId, scope?} → {rules[]}
	methodV3PermissionsExplain = "_kiro/permissions/explain" // C→A request: {sessionId, capability|toolId, resource?} → {effect, matchedRule?, scope, source, isExplicitAsk}
	methodV3PolicyChanged      = "_kiro/policy/changed"      // A→C notification: {sessionId, status, errors?}
	methodV3PolicyError        = "_kiro/policy/error"        // A→C notification: {sessionId, errors[]}
)

// v3 (KAS) requests outside the notification set above.
const (
	methodKiroOpenExternalURL = "_kiro/openExternalUrl"  // A→C request: {url} — open a URL for the user (MCP OAuth); needs the openExternalUrl client capability
	methodKiroGetUsage        = "_kiro/account/getUsage" // C→A request: account/subscription usage; needs profileArn in the getAccessToken reply
	methodKiroCodeIntel       = "_kiro/codeIntelligence" // C→A request: code-intelligence status/init (subcommand param); needs the session opted in via initialize _meta.kiro.settings
)

// methodKiroSessionNotify is the A→C notification KAS's `send_message` builtin
// raises: {sessionId, callerSessionId, message, severity, workflowId?, nodeId?,
// agentName?}, severity info|success|warning|error. Only `warning` waits for an
// answer; the other three advance or end the step. Two facts bind: `message` is
// the ONLY carrier of a step's question (KAS's own pause writes a fixed literal
// into state.pauseReason, so `inspect` never reports what was asked), and
// `callerSessionId` is the paused step's own session, which is the address a
// session/prompt must target for KAS to reroute the answer into the run.
const methodKiroSessionNotify = "_kiro/session/notify"

// KAS's own filesystem verbs, A→C, each gated on
// clientCapabilities.fs._meta.kiro.<name> === true. NOT declaring one does not
// remove the capability — the else-branch is KAS's in-process NodeFileSystem, so
// an undeclared verb runs the same operation with no vibekit path check. The
// `_kiro/fs/{read_file,write_file}` rung is deliberately absent: it would bypass
// the supervised staging path on fs/{read,write}_text_file.
const (
	methodKiroFSStat          = "_kiro/fs/stat"
	methodKiroFSReadDirectory = "_kiro/fs/read_directory"
	methodKiroFSDelete        = "_kiro/fs/delete"
)

// v3 (KAS) credential-storage requests, all three A→C ONLY — every client→agent
// param shape probed returns -32603. KAS builds its AcpSecretStorage only when
// initialize declares _meta.kiro.secretStorage: true; without the flag every
// bridge spawn re-runs Dynamic Client Registration. Failure semantics differ and
// are load-bearing: KAS catches a get failure (credential treated as absent) but
// rethrows a store or delete failure into the MCP connect path.
const (
	methodKiroSecretGet    = "_kiro/secret/get"
	methodKiroSecretStore  = "_kiro/secret/store"  //nolint:gosec // G101: ACP method name, not a credential
	methodKiroSecretDelete = "_kiro/secret/delete" //nolint:gosec // G101: ACP method name, not a credential
)

// The `_kiro/spec/*` family is deliberately unwired: .kiro/specs/**/*.md is
// already served by the file browser, and every invoke verb drives a
// fire-and-forget turn with no turn-end signal to tell finished from hung.

// v3 (KAS) knowledge-base methods. vibekit issues _kiro/knowledge on the utility
// bridge WITHOUT a sessionId, so it targets the workspace-global default store
// rather than any chat's. indexingStarted/indexingCompleted are unhandled: they
// fire only for a non-builtin mode's per-agent base, disjoint from the `default`
// store GET /api/knowledge reads, so refetching could never show what they
// announced. Progress for a user `add` is polled via `show`.
const (
	methodKiroKnowledge      = "_kiro/knowledge"       // C→A request: {subcommand, ...} → {success, entries?/message?}
	methodKiroConfigTemplate = "_kiro/config/template" // C→A request (2.14+): {} → {modes:{availableModes,currentModeId}, configOptions[]} — session-less catalog
	// methodKiroWorkflowList enumerates workflow RUNS. workspacePaths is an
	// ARRAY and REQUIRED: every other param shape fails -32603 "workspacePaths
	// is not iterable".
	methodKiroWorkflowList    = "_kiro/workflow/list"    // C→A request: {workspacePaths[]} → {runs[]}
	methodKiroWorkflowInspect = "_kiro/workflow/inspect" // C→A request: {workflowId} → {workflowId, state, nodePlan}
)

// The nine A→C workflow lifecycle notifications, exactly KAS's KIND_TO_METHOD
// table. Only node_start carries a top-level sessionId, and it is the STEP's.
// They arrive on the launching chat's bridge (KAS parents a run on the calling
// chat's session), so no session→chat resolution is needed.
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

// C→A workflow verbs vibekit issues, beyond list/inspect above.
//
//   - listRecipes: `source` is the launch key — `bundled://<name>` or an
//     absolute *.workflow.json path.
//   - new: workflowPath takes `bundled://` verbatim, a bare name is refused;
//     passing NO parentSessionId is what makes a run parentless.
//   - invoke: fire-and-forget, and the run's lifecycle frames arrive on the
//     invoking connection. cancel stops at the next NODE boundary.
const (
	methodKiroWorkflowListRecipes = "_kiro/workflow/listRecipes"
	methodKiroWorkflowNew         = "_kiro/workflow/new"
	methodKiroWorkflowInvoke      = "_kiro/workflow/invoke"
	methodKiroWorkflowCancel      = "_kiro/workflow/cancel"
	// methodKiroWorkflowDelete cancels a non-terminal run, then removes its run
	// directory. The only verb that takes a run OUT of _kiro/workflow/list —
	// cancel only settles a run's status.
	methodKiroWorkflowDelete = "_kiro/workflow/delete"
	methodKiroWorkflowResume = "_kiro/workflow/resume"
	// methodKiroWorkflowPause sets control.pauseRequested; the run stops at the
	// next NODE boundary, like cancel. cancel's optional `targetStatus` is
	// deliberately never sent, so a stop always records the same status.
	methodKiroWorkflowPause = "_kiro/workflow/pause"

	// methodKiroWorkflowRetry resets a finished run's failed and aborted nodes
	// plus their ancestors; legal only from failed/aborted, and it requires the
	// run in the calling process's live registry, so a re-hosting caller must
	// `load` first. Resetting ZERO nodes is a success reply.
	methodKiroWorkflowRetry = "_kiro/workflow/retry"

	// methodKiroWorkflowLoad registers an existing run from disk into the calling
	// process; the prerequisite for every verb reaching a run it has never seen.
	methodKiroWorkflowLoad = "_kiro/workflow/load"

	// methodKiroWorkflowUpdate mutates a live run. vibekit narrows it to
	// set_step_status; replace_remaining is a plan editor and is not wired.
	methodKiroWorkflowUpdate = "_kiro/workflow/update"
)

// v3 (KAS) hook management. list/setEnabled ride the utility bridge (which opts
// into the v2 hook engine via _meta.kiro.hooks={enabled,v2}) and KAS answers them
// only when v2Hooks is on; didChange is A→C after a hook file changes.
//
// `_kiro/hooks/triggerHook` and `_kiro/hooks/executeHook` are deliberately
// ABSENT: answering executeHook is what made vibekit run `sh -c` on a command a
// hook file specifies, and naming a method here is what makes it reachable.
const (
	methodKiroHooksList       = "_kiro/hooks/list"       // C→A request: {workspacePaths?,trigger?,toolId?,includeDisabled?} → {hooks[]}
	methodKiroHooksSetEnabled = "_kiro/hooks/setEnabled" // C→A request: {hookId, enabled} → {success, code?, error?}
	methodKiroHooksDidChange  = "_kiro/hooks/didChange"  // A→C notification: {hooks[]}
)

// v3 (KAS) Infrastructure-Safety. propertiesChanged/statusChanged fire only when
// the client's infrastructureSafety capability is declared AND an AWS governance
// flag (infraSafetyMonitor|infraSafetyEnforce) is on — off by default on
// individual accounts, so this surface is inert. Properties are formalized by a
// remote MCP endpoint; there is no set/toggle RPC.
const (
	methodV3SafetyGetProperties = "_kiro/safety/getProperties"     // C→A request (reachable; not wired — inert by default)
	methodV3SafetyPropertiesChg = "_kiro/safety/propertiesChanged" // A→C notification → safety_properties SSE
	methodV3SafetyStatusChanged = "_kiro/safety/statusChanged"     // A→C notification → safety_status SSE
)

// Terminal protocol methods (ACP terminal/* namespace). v3/KAS spells the wait
// verb snake_case: terminal/wait_for_exit.
const (
	methodTermPrefix      = "terminal/"
	methodTermCreate      = "terminal/create"
	methodTermOutput      = "terminal/output"
	methodTermRelease     = "terminal/release"
	methodTermWaitForExit = "terminal/wait_for_exit"
	methodTermKill        = "terminal/kill"
)
