package hub

import "github.com/cplieger/vibekit/internal/api"

// ACP method name constants. Centralised so a protocol rename is a
// single-line change with compile-time verification of all consumers.

// Compile-time assertion that api method constants are accessible.
var _ = api.MethodPrompt

// v3 (KAS) extension notification method names (_kiro/* namespace).
// Several map onto shared domain handlers (rate_limit, customAgent
// errors, mcp/status); the rest are recognised-but-ignored (noopMethods)
// with a rationale. Shapes verified against the KAS 2.12 acp-server
// bundle (see kiro-cli-research.md "v3 _kiro/* wire surface").
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
// Shapes verified against the KAS 2.12 acp-server bundle + a live probe.
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
)

// v3 (KAS) spec-workflow method names. getTaskStatuses is a stateless C→A
// request ({workspacePaths, tasksFilePath, featureName} → {tasks:[tree]})
// vibekit issues on the utility bridge to build the read-only board.
// taskStatusChanged is an A→C notification ({sessionId, tasksFilePath,
// changes:[{taskId, executionStatus, ...}]}) emitted while a spec execution
// runs; translated to the spec_task_changed SSE. invoke / resolveSession are
// verified functional but deliberately NOT wired (each invoke verb drives a
// fire-and-forget agent turn with no acp turn-end signal — see hub/spec.go).
// Shapes verified against the KAS 2.12 acp-server bundle + a live probe.
const (
	methodV3SpecGetTaskStatuses   = "_kiro/spec/getTaskStatuses"   // C→A request
	methodV3SpecTaskStatusChanged = "_kiro/spec/taskStatusChanged" // A→C notification
)

// v3 (KAS) knowledge-base method names. _kiro/knowledge is a C→A request
// dispatched by a `subcommand` field (show/add/remove/update/clear/cancel);
// vibekit issues it on the utility bridge WITHOUT a sessionId so it targets
// the workspace-global default store (disk-backed at
// $KIRO_HOME/.kiro/knowledge_bases/default), not any chat's session store.
// The two indexingStarted/indexingCompleted notifications are A→C — verified
// live to fire only for agent-declared knowledge_bases sync at session start,
// NOT for a user-initiated `add` (whose progress is polled via `show`). They
// are still translated to the knowledge_indexing SSE so the agent-sync case
// surfaces. Shapes verified against the KAS 2.12 acp-server bundle + a live
// probe (see kiro-cli-research.md "v3 _kiro/* wire surface").
const (
	methodKiroKnowledge                  = "_kiro/knowledge"                   // C→A request: {subcommand, ...} → {success, entries?/message?}
	methodKiroKnowledgeIndexingStarted   = "_kiro/knowledge/indexingStarted"   // A→C notification: {sessionId, name, fileCount}
	methodKiroKnowledgeIndexingCompleted = "_kiro/knowledge/indexingCompleted" // A→C notification: {sessionId, name, status, itemCount?}
)

// v3 (KAS) hook-management method names. list/setEnabled/triggerHook are C→A
// requests vibekit issues on the utility bridge (which opts into the v2 hook
// engine via _meta.kiro.hooks={enabled,v2} — see internal/bridge/bridge.go).
// All three are gated: KAS answers them only when v2Hooks is enabled (verified
// live — otherwise list throws "not available when v2Hooks is disabled").
// executeHook is an A→C REQUEST KAS makes back to the client to RUN a
// runCommand hook's shell command (security-sensitive; answered on the utility
// bridge only, and only while a user-initiated trigger is in flight — see
// utility_bridge.go). didChange is an A→C notification KAS emits after a hook
// file changes. Shapes verified against the KAS 2.12 acp-server bundle + a live
// probe (see kiro-cli-research.md "v3 _kiro/* wire surface").
const (
	methodKiroHooksList        = "_kiro/hooks/list"        // C→A request: {workspacePaths?,trigger?,toolId?,includeDisabled?} → {hooks[]}
	methodKiroHooksSetEnabled  = "_kiro/hooks/setEnabled"  // C→A request: {hookId, enabled} → {success, code?, error?}
	methodKiroHooksTriggerHook = "_kiro/hooks/triggerHook" // C→A request: {sessionId,hookId,hookName,hookActionType,command|prompt,approved?} → {success, code?, error?}
	methodKiroHooksExecuteHook = "_kiro/hooks/executeHook" // A→C request: {hookId,hookName,command,sessionId,userPrompt,operationId,timeout?} → {output,exitCode,cancelled?}
	methodKiroHooksDidChange   = "_kiro/hooks/didChange"   // A→C notification: {hooks[]}
)

// v3 (KAS) Infrastructure-Safety method names. getProperties is a C→A request
// (reachable over acp: {sessionId} → {properties}, returns [] by default);
// propertiesChanged/statusChanged are A→C notifications KAS emits ONLY when the
// gate is installed — which requires the client's infrastructureSafety
// capability (declared in initialize, see internal/bridge/bridge.go) AND an AWS
// governance flag (infraSafetyMonitor|infraSafetyEnforce) that is off by default
// on individual/Builder-ID accounts. So on a normal account these never fire;
// the notification handlers (translate/safety.go) are defensive/forward-looking,
// mirroring _kiro/code_references. Properties are formalized by a remote MCP
// endpoint, never authored by vibekit — there is no set/toggle RPC. Shapes
// verified against the KAS 2.12 acp-server bundle + a live probe.
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
