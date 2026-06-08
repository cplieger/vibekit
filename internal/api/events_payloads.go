package api

import (
	checkpoint "github.com/cplieger/vibekit/internal/checkpoint/types"
)

// Per-event payload structs for SSE events. The envelope types and
// event-type constants live in events.go; this file contains only the
// payload shapes that change when new events are added or existing
// payloads gain fields.

// TurnEndedPayload is the payload for type="turn_ended".
type TurnEndedPayload struct {
	ChangedFiles map[string]*FileChange `json:"changed_files,omitempty"`
	StopReason   StopReason             `json:"stop_reason,omitempty"`
	CreditsDelta float64                `json:"credits_delta,omitempty"`
	ElapsedMs    float64                `json:"elapsed_ms,omitempty"`
}

// FileChange tracks per-file change stats during a turn.
type FileChange struct {
	LinesAdded   int  `json:"lines_added"`
	LinesRemoved int  `json:"lines_removed"`
	IsNewFile    bool `json:"is_new_file,omitempty"`
}

// ConnectedPayload is the payload for type="connected", the SSE handshake
// event. Floor is the oldest event ID still in the replay ring; Head is
// the newest. Clients with last-seen-id < Floor know they missed events
// and should refetch authoritative state.
type ConnectedPayload struct {
	Floor uint64 `json:"floor"`
	Head  uint64 `json:"head"`
}

// PermissionNeededPayload is the payload for type="permission_needed".
type PermissionNeededPayload struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	Title      string `json:"title,omitempty"`
	// Kind forwards the ACP toolCall.kind so the client can style
	// distinctive permission prompts (switch_mode gets a different
	// dialog vs an execute_bash prompt).
	Kind         ToolKind           `json:"kind,omitempty"`
	SubSessionID string             `json:"sub_session_id,omitempty"`
	Options      []PermissionOption `json:"options"`
	RequestID    int64              `json:"request_id"`
}

// PermissionOption is one selectable response in a permission dialog.
type PermissionOption struct {
	OptionID string `json:"option_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// PermissionKindAllowOnce is the wire value kiro-cli sends in
// PermissionOption.Kind to identify the "allow once" choice.
const PermissionKindAllowOnce = "allow_once"

// MessageChunkPayload is the payload for type="message_chunk" (assistant
// streaming deltas). IsReasoning distinguishes reasoning deltas from
// regular content deltas — both flow through the same SSE event but
// land on different fields client-side (Message.Reasoning vs Content).
// BlockIndex addresses the chronological content block this delta
// belongs to (Anthropic's content_block_delta.index): consecutive text
// chunks share an index; a tool_call between text segments bumps the
// next text chunk to a new index. Clients use this to accumulate
// deltas into the right block in Message.Blocks.
type MessageChunkPayload struct {
	MessageID   string `json:"message_id"`
	Delta       string `json:"delta"`
	IsReasoning bool   `json:"is_reasoning,omitempty"`
	BlockIndex  int    `json:"block_index"`
}

// CheckpointRestoredPayload is the payload for type="checkpoint_restored".
// Replaces the ad-hoc map[string]any so the wire shape is discoverable
// via IDE completion and typos in key names become compile errors.
type CheckpointRestoredPayload struct {
	Tag          string `json:"tag"`
	MessageCount int    `json:"message_count"`
}

// ErrorCode identifies an SSE error event class. Using a typed string
// prevents typos at construction sites and makes the valid set
// discoverable via IDE completion.
type ErrorCode string

const (
	ErrCodeRecoveryFailed    ErrorCode = "recovery_failed"
	ErrCodeBridgeStartFailed ErrorCode = "bridge_start_failed"
	ErrCodePromptFailed      ErrorCode = "prompt_failed"
	ErrCodeAgentNotFound     ErrorCode = "agent_not_found"
	ErrCodeModelNotFound     ErrorCode = "model_not_found"
	ErrCodeAgentConfigError  ErrorCode = "agent_config_error"
	ErrCodeRateLimit         ErrorCode = "rate_limit"
	ErrCodeStreamTimeout     ErrorCode = "stream_timeout"
	ErrCodeSpawnFailed       ErrorCode = "spawn_failed"
	ErrCodeSwitchFailed      ErrorCode = "switch_failed"
	ErrCodeCompactionFailed  ErrorCode = "compaction_failed"
)

// ErrorPayload is the payload for type="error". Code lets clients react
// per-class without string-matching.
type ErrorPayload struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// WorkingLabelPayload is the payload for type="working_label". Sent when
// the agent starts or finishes a tool call so the client can show a
// contextual label ("Reading", "Searching", "Running <title>", etc.)
// instead of a generic "Thinking" indicator.
type WorkingLabelPayload struct {
	Label string `json:"label"`
}

// RPC error code constants for typed dispatch in retry logic.
const (
	// RPCCodeInternal is the JSON-RPC 2.0 "Internal error" code (-32603).
	RPCCodeInternal = -32603
	// RPCCodeNotIdle indicates the session is still processing a prior turn.
	RPCCodeNotIdle = -32001
	// RPCCodeBridgeExited is a server-defined code indicating the ACP bridge process exited.
	RPCCodeBridgeExited = -32000
)

// ToolCallPayload is the payload for type="tool_call". BlockIndex is
// the position of the tool_use block in the assistant message's
// chronological Blocks array — the client uses it to insert the tool
// card at the right spot relative to surrounding text blocks.
type ToolCallPayload struct {
	MessageID  string   `json:"message_id"`
	ToolCall   ToolCall `json:"tool_call"`
	BlockIndex int      `json:"block_index"`
}

// ToolCallUpdatePayload is the payload for type="tool_call_update".
type ToolCallUpdatePayload struct {
	MessageID string   `json:"message_id"`
	ToolCall  ToolCall `json:"tool_call"`
}

// TerminalOutputPayload is the payload for type="terminal_output".
type TerminalOutputPayload struct {
	TerminalID string `json:"terminal_id"`
	Data       string `json:"data"`
}

// TerminalExitedPayload is the payload for type="terminal_exited".
type TerminalExitedPayload struct {
	TerminalID string `json:"terminal_id"`
	ExitCode   int    `json:"exit_code"`
}

// TerminalCreatedPayload is the payload for type="terminal_created".
type TerminalCreatedPayload struct {
	TerminalID string   `json:"terminal_id"`
	Command    string   `json:"command"`
	Args       []string `json:"args"`
}

// SubagentActivityPayload is the payload for type="subagent_activity".
type SubagentActivityPayload struct {
	Event        any    `json:"event"`
	SubSessionID string `json:"sub_session_id"`
}

// SessionListUpdatedPayload is the payload for type="session_list_updated".
type SessionListUpdatedPayload struct {
	Sessions []map[string]any `json:"sessions"`
}

// ModeChangedPayload is the payload for type="mode_changed".
type ModeChangedPayload struct {
	ModeID string `json:"mode_id"`
}

// ConflictDetectedPayload is the payload for type="conflict_detected".
// Type alias to checkpoint/types.ConflictPayload — single source of truth.
type ConflictDetectedPayload = checkpoint.ConflictPayload

// ChatDeletedPayload is the payload for type="chat_deleted".
type ChatDeletedPayload struct {
	ID string `json:"id"`
}

// AvailableCommand is one entry in the slash-command catalogue surfaced
// by kiro-cli's _kiro.dev/commands/available notification. The wire
// shape carries opaque metadata; clients consume only Name and Description.
type AvailableCommand struct {
	Meta        map[string]any `json:"meta,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
}

// CommandsUpdatedPayload is the payload for type="commands_updated".
type CommandsUpdatedPayload struct {
	Commands []AvailableCommand `json:"commands"`
	Prompts  []AvailableCommand `json:"prompts,omitempty"`
}

// SteeringLoadedPayload is the payload for type="steering_loaded".
type SteeringLoadedPayload struct {
	Documents []string `json:"documents"`
}

// CompactionStartedPayload is the payload for type="compaction_started".
type CompactionStartedPayload struct{}

// MCPConfigChangedPayload is the payload for type="mcp_config_changed".
type MCPConfigChangedPayload struct{}

// ForgesChangedPayload is the payload for type="forges_changed".
// Sent after a forge is connected, disconnected, or re-probed.
type ForgesChangedPayload struct{}

// SettingsUpdatedPayload is the payload for type="settings_updated".
type SettingsUpdatedPayload struct{}

// --- HTTP response types for checkpoint and MCP endpoints ---

// CheckpointDiffResponse is the typed response for the checkpoint diff endpoint.
type CheckpointDiffResponse struct {
	Files []checkpoint.FileChange `json:"files"`
}

// CheckpointRestorePreviewResponse is the typed response for restore-preview.
type CheckpointRestorePreviewResponse struct {
	Files []string `json:"files"`
}

// CheckpointConflictsResponse is the typed response for the conflicts endpoint.
type CheckpointConflictsResponse struct {
	Conflicts []checkpoint.ConflictPayload `json:"conflicts"`
}
