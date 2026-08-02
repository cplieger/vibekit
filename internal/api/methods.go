package api

// ACP protocol method name constants. The complete vocabulary lives here
// so a protocol rename is a single-line change with compile-time
// verification of all consumers across packages.

// Bridge lifecycle methods — used by the bridge package to drive the
// kiro-cli subprocess through initialize → session/new|load. On v3 (KAS)
// model/mode/effort switches all route through session/set_config_option
// (session/set_model is gone), and session/fork branches a session at a
// message id (see command/rewind.go).
const (
	MethodInitialize  = "initialize"
	MethodSessionNew  = "session/new"
	MethodSessionLoad = "session/load"
	MethodSetMode     = "session/set_mode"
	MethodSessionFork = "session/fork"
	MethodCancel      = "session/cancel"
)

// File-system protocol method names (ACP fs/* namespace). Exported so
// test files can reference the canonical string without bare literals.
const (
	MethodFSRead  = "fs/read_text_file"
	MethodFSWrite = "fs/write_text_file"
)

// MCP elicitation method name. On v3 (KAS) an MCP server's structured-input
// request is forwarded to us as the extension request _kiro/mcp/elicitation
// (a JSON-RPC request with an id, delivered like fs/*). The params nest the
// elicitation body under an "elicitation" object with sessionId/toolCallId
// at the top level; we surface a form and reply with {action, content} on
// the request id. Gated by the clientCapabilities.elicitation capability
// advertised in bridge.initialize(). v3 has no elicitation-complete method.
const (
	MethodElicitationCreate = "_kiro/mcp/elicitation"
)

// Session createdReason values (kiro-cli 2.16+). KAS records why a session
// exists in its own session metadata — `human | rewind | subagent | tangent`
// — and a fork supplies it via _meta.kiro.createdReason on session/fork.
// vibekit stamps Rewind on its branch forks (command/rewind.go) so they are
// self-describing in KAS's roster; the other values are set by KAS itself
// (tangent is the TUI's named-side-conversation feature, which vibekit does
// not surface — its chat tabs already cover that model).
const (
	CreatedReasonRewind = "rewind"
)

// Agent user-input method name. On v3 (KAS, 2.14+) the agent's user_input
// tool (structured questions: plan-mode clarifications, spec gates) is
// forwarded to us as the extension request _kiro/userInput (a JSON-RPC
// request with an id, delivered like _kiro/mcp/elicitation) carrying
// {sessionId, toolCallId, question, options[{title, description,
// recommended, subOptionsLabel, subOptions[]}]}. We surface a question
// dialog and reply {action:"answered", answer:"<text>"} on the request id;
// any other action makes KAS advance to the next phase. Gated by the
// initialize capability _meta.kiro.userInput:true — without it KAS
// flattens the question into a session/request_permission and SKIPS
// free-form (no-options) questions entirely.
const (
	MethodKiroUserInput = "_kiro/userInput"
)

// Session-level ACP method name for prompts.
const (
	MethodPrompt = "session/prompt"
)

// Session-level ACP method names — streaming updates, permissions, config.
const (
	MethodSessionUpdate     = "session/update"
	MethodRequestPermission = "session/request_permission"
	MethodSetConfigOption   = "session/set_config_option"
)

// ContentTypeText is the ACP content-block type discriminator for plain text content.
// Used across hub, command, and translate packages; declared here as a single source
// of truth so a protocol rename is one edit.
const ContentTypeText = "text"

// ModelAuto is the sentinel model value meaning "keep current / use
// task-based selection". Used by bridge, hub, and model-switch logic.
const ModelAuto = "auto"

// AgentEngineV3 is the only agent engine vibekit speaks:
// `kiro-cli acp --agent-engine v3` (KAS). It requires the host to answer
// _kiro/auth/getAccessToken + _kiro/terminal/shell_type and emits the
// reshaped _kiro/* extension set. The legacy v1/v2 identifiers were
// removed with the v2 wire — vibekit is v3-only (resolveAgentEngine).
const AgentEngineV3 = "v3"

// Session config-option ids for session/set_config_option (v3/KAS). Model
// and reasoning-effort switches route through set_config_option with one of
// these configId values plus a matching value string. (Verified against the
// KAS 2.12 acp-server bundle: MODEL_CONFIG_ID / EFFORT_LEVEL_CONFIG_ID.)
// Mode switches use the dedicated session/set_mode method, not this path.
const (
	ConfigOptionModel  = "model"
	ConfigOptionMode   = "mode"
	ConfigOptionEffort = "effortLevel"
)

// ACP content-block JSON field name constants. These are the wire-format
// keys inside a content block object (distinct from ContentTypeText which
// is the field VALUE). Single source of truth for hub, command, and
// translate packages.
const (
	ContentKeyType = "type"
	ContentKeyText = "text"
)

// TextBlock returns a canonical ACP text content block:
//
//	{"type": "text", "text": content}
//
// Eliminates ad-hoc map construction across hub, command, and translate.
func TextBlock(content string) map[string]any {
	return map[string]any{ContentKeyType: ContentTypeText, ContentKeyText: content}
}

// KeySessionID is the ACP wire key for the session identifier in
// parameter maps. Single source of truth; hub and command packages
// reference this constant instead of bare "sessionId" literals.
const KeySessionID = "sessionId"
