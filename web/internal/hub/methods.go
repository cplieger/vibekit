package hub

import "vibekit/internal/api"

// ACP method name constants. Centralised so a protocol rename is a
// single-line change with compile-time verification of all consumers.
const (
	methodPrompt            = api.MethodPrompt
	methodCancel            = api.MethodCancel
	methodUpdate            = "session/update"
	methodRequestPermission = "session/request_permission"
	methodSetConfigOption   = "session/setConfigOption"
	methodSpawn             = api.MethodSpawn
	methodTerminate         = "session/terminate"
	methodAttach            = "session/attach"
	methodList              = "session/list"
	methodMessageSend       = "message/send"
	methodCommandsExecute   = api.MethodCommandsExecute
	methodCommandsOptions   = api.MethodCommandsOptions
)

// kiro-cli extension notification method names. These are the
// _kiro.dev/* namespace methods dispatched in initDispatch. Declared
// here so the protocol surface is greppable from one file and typos
// are caught at compile time.
const (
	methodMetadata             = "_kiro.dev/metadata"
	methodMetadataLegacy       = "kiro/metadata"
	methodCommandsAvailable    = "_kiro.dev/commands/available"
	methodCompactionStatus     = "_kiro.dev/compaction/status"
	methodSubagentListUpdate   = "_kiro.dev/subagent/list_update"
	methodAgentNotFound        = "_kiro.dev/agent/not_found"
	methodAgentConfigError     = "_kiro.dev/agent/config_error"
	methodModelNotFound        = "_kiro.dev/model/not_found"
	methodErrorRateLimit       = "_kiro.dev/error/rate_limit"
	methodAgentSwitched        = "_kiro.dev/agent/switched"
	methodSessionActivity      = "_kiro.dev/session/activity"
	methodSessionListUpdate    = "_kiro.dev/session/list_update"
	methodInboxNotification    = "_kiro.dev/session/inbox_notification"
	methodExtSessionUpdate     = "_kiro.dev/session/update"
	methodSessionRetry         = "_kiro.dev/session/retry"
	methodMCPServerInitialized = "_kiro.dev/mcp/server_initialized"
	methodMCPServerInitFailure = "_kiro.dev/mcp/server_init_failure"
	methodMCPOAuthRequest      = "_kiro.dev/mcp/oauth_request"
	methodClearStatus          = "_kiro.dev/clear/status"
)

// File-system protocol method names (ACP fs/* namespace).
const (
	methodFSRead  = api.MethodFSRead
	methodFSWrite = api.MethodFSWrite
)

// Terminal protocol method names (ACP terminal/* namespace).
const (
	methodTermPrefix      = "terminal/"
	methodTermCreate      = "terminal/create"
	methodTermOutput      = "terminal/output"
	methodTermRelease     = "terminal/release"
	methodTermWaitForExit = "terminal/waitForExit"
	methodTermKill        = "terminal/kill"
)
