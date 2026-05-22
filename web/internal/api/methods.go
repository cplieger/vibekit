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
)

// File-system protocol method names (ACP fs/* namespace). Exported so
// test files can reference the canonical string without bare literals.
const (
	MethodFSRead  = "fs/read_text_file"
	MethodFSWrite = "fs/write_text_file"
)

// Slash-command extension method names (_kiro.dev/commands/* namespace).
const (
	MethodCommandsExecute = "_kiro.dev/commands/execute"
	MethodCommandsOptions = "_kiro.dev/commands/options"
)
