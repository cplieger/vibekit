package api

// ACP protocol method name constants. The complete vocabulary lives here
// so a protocol rename is a single-line change with compile-time
// verification of all consumers across packages.

// Bridge lifecycle methods — used by the bridge package to drive the
// kiro-cli subprocess through initialize → session/new|load → set_model.
const (
	MethodInitialize  = "initialize"
	MethodSessionNew  = "session/new"
	MethodSessionLoad = "session/load"
	MethodSetModel    = "session/set_model"
	MethodCancel      = "session/cancel"
)

// File-system protocol method names (ACP fs/* namespace). Exported so
// test files can reference the canonical string without bare literals.
const (
	MethodFSRead  = "fs/read_text_file"
	MethodFSWrite = "fs/write_text_file"
)

// MCP elicitation method names. When an MCP server requests structured
// input mid-tool-execution, kiro-cli (acting as the MCP client) forwards
// the request to us over ACP as elicitation/create — sent the same way
// as the fs/* requests above (see the agent's client-protocol method
// registry, where elicitation_create sits beside fs_read_text_file).
// We surface a form to the user and reply with {action, content}.
// elicitation/complete is an agent→client notification telling us a
// pending elicitation was cancelled upstream so we dismiss the dialog.
// Gated by the clientCapabilities.elicitation capability advertised in
// bridge.initialize(); without it kiro-cli does not forward elicitation.
const (
	MethodElicitationCreate   = "elicitation/create"
	MethodElicitationComplete = "elicitation/complete"
)

// Slash-command extension method names (_kiro.dev/commands/* namespace).
const (
	MethodCommandsExecute = "_kiro.dev/commands/execute"
	MethodCommandsOptions = "_kiro.dev/commands/options"
)

// Session-level ACP method names — prompt and subagent spawn.
const (
	MethodPrompt = "session/prompt"
	MethodSpawn  = "session/spawn"
)

// Session-level ACP method names — streaming updates, permissions,
// config, and subagent lifecycle.
const (
	MethodSessionUpdate     = "session/update"
	MethodRequestPermission = "session/request_permission"
	MethodSetConfigOption   = "session/setConfigOption"
	MethodTerminate         = "session/terminate"
	MethodAttach            = "session/attach"
	MethodList              = "session/list"
	MethodMessageSend       = "message/send"
)

// ContentTypeText is the ACP content-block type discriminator for plain text content.
// Used across hub, command, and translate packages; declared here as a single source
// of truth so a protocol rename is one edit.
const ContentTypeText = "text"

// ModelAuto is the sentinel model value meaning "keep current / use
// task-based selection". Used by bridge, hub, and model-switch logic.
const ModelAuto = "auto"

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
