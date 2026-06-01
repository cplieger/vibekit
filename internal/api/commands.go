package api

// Client commands: the wire shapes for POST /api/command envelopes +
// per-type payloads. Command routing lives in hub/command.go; this file
// just declares the contracts.

import "encoding/json"

// CommandType identifies the kind of client command posted to
// /api/command. Using a typed string instead of bare literals makes
// typos a compile error and the full command vocabulary discoverable.
type CommandType string

// Command type constants. Every value here corresponds to a key in
// hub/command.go's registerCommandHandlers dispatch map.
const (
	CmdCreateChat               CommandType = "create_chat"
	CmdPrompt                   CommandType = "prompt"
	CmdCancel                   CommandType = "cancel"
	CmdDeleteChat               CommandType = "delete_chat"
	CmdSwitchModel              CommandType = "switch_model"
	CmdPermissionResponse       CommandType = "permission_response"
	CmdSpawnSubagent            CommandType = "spawn_subagent"
	CmdMessageSubagent          CommandType = "message_subagent"
	CmdTerminateSubagent        CommandType = "terminate_subagent"
	CmdAttachSubagent           CommandType = "attach_subagent"
	CmdListSessions             CommandType = "list_sessions"
	CmdSetAutoApproveCrew       CommandType = "set_auto_approve_crew"
	CmdRestoreCheckpoint        CommandType = "restore_checkpoint"
	CmdUndoEdit                 CommandType = "undo_edit"
	CmdRewindChat               CommandType = "rewind_chat"
	CmdPromoteRewindChat        CommandType = "promote_rewind_chat"
	CmdDiscardRewindChat        CommandType = "discard_rewind_chat"
	CmdSetEffort                CommandType = "set_effort"
	CmdCreateHook               CommandType = "create_hook"
	CmdResolvePendingChange     CommandType = "resolve_pending_change"
	CmdResolvePendingPartial    CommandType = "resolve_pending_change_partial"
	CmdResolveAllPendingChanges CommandType = "resolve_all_pending_changes"
	CmdSetSupervisedMode        CommandType = "set_supervised_mode"
	CmdTrustPendingChanges      CommandType = "trust_pending_changes"
	CmdClearPendingTrust        CommandType = "clear_pending_trust"
)

// ClientCommand is the envelope for every command the browser posts.
// Type determines how Payload unmarshals.
type ClientCommand struct {
	Type      CommandType     `json:"type"`
	RequestID string          `json:"request_id"`
	ChatID    ChatID          `json:"chat_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// PromptCommand is the payload for type="prompt".
//
// Text is required, trimmed, and capped at 512 KiB; oversize returns
// HTTP 413. MessageID is required and must match the ULID/id character
// set enforced by the hub's validMessageID check (128-byte cap, no
// control chars). Agent and Model are optional; if non-empty they must
// match validIdent (ASCII alphanumerics plus `_.-`, 1-128 bytes).
// ActiveFile and OpenFiles are forwarded to kiro-cli as _meta.kiro
// metadata; they do not reach a filesystem sink directly in vibekit.
// Attachments are resolved via resolveInsideWorkDir before read.
type PromptCommand struct {
	Text        string       `json:"text"`
	MessageID   string       `json:"message_id"` // client-generated ULID
	Agent       string       `json:"agent,omitempty"`
	Model       string       `json:"model,omitempty"`
	ActiveFile  string       `json:"active_file,omitempty"` // editor active file path
	OpenFiles   []string     `json:"open_files,omitempty"`  // all open editor tabs
	Attachments []Attachment `json:"attachments,omitempty"` // files attached via pill row
}

// Attachment is a file attached to a prompt. The server reads the file
// and decides whether to send it as a document content block (PDF, DOCX,
// etc.) or a text path reference based on the extension.
type Attachment struct {
	Path string `json:"path"` // workspace-relative path
	Name string `json:"name"` // display name (filename.ext)
}

// CreateChatCommand is the payload for type="create_chat".
//
// Name is optional and capped at 512 bytes (maxChatNameBytes); empty
// values default to "New conversation". Agent and Model are optional;
// if non-empty they must match validIdent (ASCII alphanumerics plus
// `_.-`, 1-128 bytes). Validation failures return HTTP 400.
type CreateChatCommand struct {
	Name  string `json:"name,omitempty"`
	Agent string `json:"agent,omitempty"`
	Model string `json:"model,omitempty"`
}

// SwitchModelCommand is the payload for type="switch_model". Ends the
// current ACP session and starts a fresh one with a new model, priming
// the replacement with the chat transcript so kiro-cli has context.
// kiro-cli cannot swap models mid-session, so we rebuild the bridge.
// Agent changes are intentionally NOT part of this command — the agent
// itself is responsible for switching modes, and the user cannot force
// an agent change on a live chat from the client.
//
// Model accepted forms: empty or "auto" keep the current model (a bare
// restart of the bridge, useful when the session is wedged); any other
// value must match validIdent (ASCII alphanumerics plus `_.-`, 1-128
// bytes).
type SwitchModelCommand struct {
	// Target model id. Empty or "auto" = keep current model (a bare
	// restart of the bridge, useful when the session is wedged).
	Model string `json:"model,omitempty"`
}

// PermissionResponseCommand is the payload for type="permission_response".
type PermissionResponseCommand struct {
	OptionID  string `json:"option_id"`
	RequestID int64  `json:"request_id"`
}

// SpawnSubagentCommand is the payload for type="spawn_subagent".
// Spawns a new ephemeral subagent via session/spawn.
type SpawnSubagentCommand struct {
	Task string `json:"task"`
	Name string `json:"name,omitempty"`
}

// MessageSubagentCommand is the payload for type="message_subagent".
// Sends a message to a specific subagent via message/send.
type MessageSubagentCommand struct {
	SubSessionID string `json:"sub_session_id"`
	Text         string `json:"text"`
}

// RewindChatCommand is the payload for type="rewind_chat".
// Creates a new chat branched from a specific turn of the current chat.
type RewindChatCommand struct {
	TurnIndex int `json:"turn_index"` // 0-based index into parent's messages array
}

// SetEffortCommand is the payload for type="set_effort".
// Applies a reasoning effort level to the active session.
type SetEffortCommand struct {
	Level EffortLevel `json:"level"` // "low" | "medium" | "high" | "xhigh" | "max"
}

// EffortLevel is a typed enum for reasoning effort levels.
type EffortLevel string

// Valid effort level constants.
const (
	EffortLow    EffortLevel = "low"
	EffortMedium EffortLevel = "medium"
	EffortHigh   EffortLevel = "high"
	EffortXHigh  EffortLevel = "xhigh"
	EffortMax    EffortLevel = "max"
)

// Valid reports whether e is a recognised effort level.
func (e EffortLevel) Valid() bool {
	switch e {
	case EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax:
		return true
	}
	return false
}

// MergeTangentCommand has no payload beyond the envelope's chat_id.
// Merges the last Q&A pair from the tangent back to the parent.
// type="merge_tangent"

// DiscardTangentCommand has no payload beyond the envelope's chat_id.
// Discards the tangent and unfreezes the parent.
// type="discard_tangent"

// ResolvePendingChangeCommand is the payload for
// type="resolve_pending_change". Accept or reject ONE staged file op.
// The ToolCallID identifies the op; Action is "accept" or "reject".
// Unknown actions or unknown ids produce a 400; idempotent re-resolve
// of an already-settled op produces a success no-op.
type ResolvePendingChangeCommand struct {
	ToolCallID string        `json:"tool_call_id"`
	Action     PendingAction `json:"action"` // "accept" | "reject"
}

// ResolveAllPendingChangesCommand is the payload for
// type="resolve_all_pending_changes". Bulk-resolve every outstanding
// staged op in the chat with the same action. Convenience for the
// Supervised pill's "Accept all" / "Reject all" buttons.
type ResolveAllPendingChangesCommand struct {
	Action PendingAction `json:"action"` // "accept" | "reject"
}

// SetSupervisedModeCommand is the payload for type="set_supervised_mode".
// Toggles the chat's SupervisedMode field. Disabling while ops are
// outstanding auto-rejects them (broadcast as pending_changes_cleared
// with reason="mode_disabled"); the agent unblocks with an error per op.
type SetSupervisedModeCommand struct {
	Enabled bool `json:"enabled"`
}

// ResolvePendingChangePartialCommand is the payload for
// type="resolve_pending_change_partial". Applies a user-authored
// MergedText in place of the agent's proposed write — the fs handler
// unblocks with accept semantics but writes the caller-supplied text
// instead of the staged NewText. Used by per-hunk Accept/Reject in
// the editor: the client starts from OldText, splices in only the
// accepted hunks, and sends the result. Rejecting every hunk should
// use resolve_pending_change with action="reject" instead (no write).
//
// MergedText is capped the same way staging is: 4 MiB max. Larger
// payloads are rejected at the JSON body limit layer.
type ResolvePendingChangePartialCommand struct {
	ToolCallID string `json:"tool_call_id"`
	MergedText string `json:"merged_text"`
}

// TrustPendingChangesCommand has no payload beyond the envelope's chat_id.
// Enables the chat's per-turn trust flag so subsequent fs/write_text_file
// calls in the same agent turn bypass the staging gate. The flag clears on
// turn_ended, cancel, supervised-mode toggle, and chat delete. Also accepts
// every currently-outstanding op so the agent unblocks immediately.
// type="trust_pending_changes"
