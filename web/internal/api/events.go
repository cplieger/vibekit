package api

// Server events: the wire shapes broadcast over SSE (/api/events) +
// per-type payloads. Event dispatch lives in hub/sse.go and the
// per-method translation lives in hub/translate*.go.

// EventType identifies the kind of SSE event broadcast to clients.
// Using a typed string instead of bare literals makes typos a compile
// error and the full event vocabulary discoverable via IDE completion.
type EventType string

// Event is the server→client event envelope, generic over payload type.
// JSON serialization is identical to ServerEvent: {type, chat_id, payload}.
// Used as a construction helper via NewEvent to provide compile-time type
// safety at emit sites; the result is converted to ServerEvent for broadcast.
type Event[T any] struct {
	Payload T         `json:"payload,omitempty"`
	Type    EventType `json:"type"`
	ChatID  ChatID    `json:"chat_id,omitempty"`
}

// ServerEvent is the envelope broadcast to every SSE client. Payload is
// the event-specific JSON object; handlers type-assert based on Type.
type ServerEvent = Event[any]

// NewEvent constructs a ServerEvent with a typed payload, providing
// compile-time type safety at the construction site.
func NewEvent[T any](t EventType, chatID ChatID, payload T) ServerEvent {
	return ServerEvent{Type: t, ChatID: chatID, Payload: payload}
}

// StopReason identifies why a turn ended. The underlying string is
// the wire value sent over SSE; typed constants prevent typos at
// construction sites.
type StopReason string

const (
	StopReasonEndTurn     StopReason = "end_turn"
	StopReasonCancelled   StopReason = "cancelled"
	StopReasonInterrupted StopReason = "interrupted"
)

// SSE event type constants. Using these instead of bare string literals
// makes typos a compile error and the full event vocabulary discoverable.
const (
	EventChatCreated           EventType = "chat_created"
	EventChatUpdated           EventType = "chat_updated"
	EventChatDeleted           EventType = "chat_deleted"
	EventCheckpointRestored    EventType = "checkpoint_restored"
	EventCommandsUpdated       EventType = "commands_updated"
	EventCompactionStarted     EventType = "compaction_started"
	EventConflictDetected      EventType = "conflict_detected"
	EventConnected             EventType = "connected"
	EventError                 EventType = "error"
	EventMCPConfigChanged      EventType = "mcp_config_changed"
	EventMCPConnected          EventType = "mcp_connected"
	EventMCPDisconnected       EventType = "mcp_disconnected"
	EventMCPFailed             EventType = "mcp_failed"
	EventMCPOAuthNeeded        EventType = "mcp_oauth_needed"
	EventMCPPrewarm            EventType = "mcp_prewarm"
	EventMessageAppended       EventType = "message_appended"
	EventMessageChunk          EventType = "message_chunk"
	EventMessageCreated        EventType = "message_created"
	EventMessageUpdated        EventType = "message_updated"
	EventModeChanged           EventType = "mode_changed"
	EventPendingChangeAdded    EventType = "pending_change_added"
	EventPendingChangeResolved EventType = "pending_change_resolved"
	EventPendingChangesCleared EventType = "pending_changes_cleared"
	EventPendingTrustCleared   EventType = "pending_trust_cleared"
	EventPendingTrustEnabled   EventType = "pending_trust_enabled"
	EventPermissionNeeded      EventType = "permission_needed"
	EventForgesChanged         EventType = "forges_changed"
	EventSessionListUpdated    EventType = "session_list_updated"
	EventSettingsUpdated       EventType = "settings_updated"
	EventSteeringLoaded        EventType = "steering_loaded"
	EventSubagentActivity      EventType = "subagent_activity"
	EventTerminalCreated       EventType = "terminal_created"
	EventTerminalExited        EventType = "terminal_exited"
	EventTerminalOutput        EventType = "terminal_output"
	EventToolCall              EventType = "tool_call"
	EventToolCallUpdate        EventType = "tool_call_update"
	EventTurnEnded             EventType = "turn_ended"
	EventWorkingLabel          EventType = "working_label"
)

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
type MessageChunkPayload struct {
	MessageID   string `json:"message_id"`
	Delta       string `json:"delta"`
	IsReasoning bool   `json:"is_reasoning,omitempty"`
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

// WorkingLabelForKind maps a tool kind to a human-readable label.
// Matches ASAI's VV() function from the frontend reducer.
func WorkingLabelForKind(kind ToolKind, title string) string {
	label, ok := workingLabels[kind]
	if !ok {
		return WorkingLabelThinking
	}
	if (kind == ToolKindExecute || kind == ToolKindShell) && title != "" {
		return label + " " + title
	}
	return label
}

// Working-label constants. Centralised so hub callers reference these
// instead of bare string literals; a future label rename lands in one place.
const (
	WorkingLabelThinking = "Thinking"
	WorkingLabelApproval = "Waiting for approval"
)

var workingLabels = map[ToolKind]string{
	ToolKindExecute: "Running",
	ToolKindShell:   "Running",
	ToolKindRead:    "Reading",
	ToolKindSearch:  "Searching",
	ToolKindFetch:   "Fetching",
	ToolKindEdit:    "Writing",
	ToolKindThink:   "Reasoning",
}

// PendingChangeKind discriminates between the three staged-file
// operations in Supervised mode. Using a typed string prevents typos
// at construction sites and makes the valid set discoverable.
type PendingChangeKind string

const (
	PendingKindCreate PendingChangeKind = "create"
	PendingKindEdit   PendingChangeKind = "edit"
	PendingKindDelete PendingChangeKind = "delete"
)

// PendingChange is one staged file operation awaiting user approval in
// Supervised mode. Key is ToolCallID (kiro-cli's id; unique per call);
// the Kind discriminates between writes, creates (no OldText), and
// deletes (no NewText).
//
// OldText and NewText are capped at pendingTextCap on the server side
// (4 MiB). When truncated, the client's "View diff" tab falls back to
// a file fetch for the full content; the staged payload only carries
// enough for the pill-popover summary and the inline diff preview.
type PendingChange struct {
	ToolCallID string            `json:"tool_call_id"`
	ChatID     ChatID            `json:"chat_id"`
	Path       string            `json:"path"`
	Kind       PendingChangeKind `json:"kind"` // "create" | "edit" | "delete"
	OldText    string            `json:"old_text,omitempty"`
	NewText    string            `json:"new_text,omitempty"`
	CreatedAt  int64             `json:"created_at"`
	Truncated  bool              `json:"truncated,omitempty"`
}

// PendingChangeAddedPayload is the payload for type="pending_change_added".
// Emitted when the Supervised-mode fs handler has received a write from
// kiro-cli and is blocking the agent until the user resolves it. Clients
// render a tool-card-level Accept/Reject pair and surface the op in the
// Supervised pill's popover.
type PendingChangeAddedPayload struct {
	Change PendingChange `json:"change"`
}

// PendingAction discriminates between the two resolution outcomes for
// a staged pending change. Using a typed string prevents typos at
// construction sites and makes the valid set discoverable.
type PendingAction string

const (
	PendingActionAccept PendingAction = "accept"
	PendingActionReject PendingAction = "reject"
)

// PendingChangeResolvedPayload is the payload for
// type="pending_change_resolved". Emitted after the user accepts or
// rejects a staged change, after the fs handler has unblocked and the
// disk state reflects the decision. Clients drop the op from the pending
// pill and update the source tool card's status.
type PendingChangeResolvedPayload struct {
	ToolCallID string        `json:"tool_call_id"`
	Action     PendingAction `json:"action"` // "accept" | "reject"
	Path       string        `json:"path,omitempty"`
}

// PendingChangesClearedPayload is the payload for
// type="pending_changes_cleared". Emitted when a turn is cancelled or
// the chat's Supervised mode is disabled while ops are outstanding;
// every pending op for the chat is rejected server-side and the client
// flushes its pending list.
type PendingChangesClearedPayload struct {
	Reason ClearReason `json:"reason,omitempty"` // "cancelled" | "mode_disabled" | "chat_deleted"
}

// ClearReason identifies why pending changes or trust were cleared.
// Using a typed string prevents typos at construction sites and makes
// the valid set discoverable via IDE completion.
type ClearReason string

const (
	ClearReasonTurnEnded    ClearReason = "turn_ended"
	ClearReasonCancelled    ClearReason = "cancelled"
	ClearReasonModeDisabled ClearReason = "mode_disabled"
	ClearReasonChatDeleted  ClearReason = "chat_deleted"
	ClearReasonShutdown     ClearReason = "shutdown"
	ClearReasonUserCleared  ClearReason = "user_cleared"
)

// PendingTrustEnabledPayload is the payload for
// type="pending_trust_enabled". Emitted when the user clicks "Trust
// remaining" in the Supervised pill, setting the chat's perTurnTrust
// flag so subsequent agent writes in the same turn bypass staging.
// The pill flips to a visibly-distinct "Trusted · this turn" state;
// the flag clears on turn_ended via the paired
// pending_trust_cleared event. Replayed on SSE reconnect so the UI
// state survives disconnects mid-turn.
type PendingTrustEnabledPayload struct{}

// PendingTrustClearedPayload is the payload for
// type="pending_trust_cleared". Emitted when the perTurnTrust flag
// drops: end of turn, cancel, chat-delete, or supervised-mode
// toggle-off. The pill reverts to the standard Supervised state.
// Reason mirrors pending_changes_cleared's vocabulary so both events
// can share dispatch semantics on the client.
type PendingTrustClearedPayload struct {
	Reason ClearReason `json:"reason,omitempty"` // "turn_ended" | "cancelled" | "mode_disabled" | "chat_deleted"
}

// RPC error code constants for typed dispatch in retry logic.
const (
	// RPCCodeInternal is the JSON-RPC 2.0 "Internal error" code (-32603).
	RPCCodeInternal = -32603
	// RPCCodeNotIdle indicates the session is still processing a prior turn.
	RPCCodeNotIdle = -32001
)

// ToolCallPayload is the payload for type="tool_call".
type ToolCallPayload struct {
	MessageID string   `json:"message_id"`
	ToolCall  ToolCall `json:"tool_call"`
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
type ConflictDetectedPayload struct {
	Path        string `json:"path"`
	OtherChat   string `json:"other_chat"`
	ExpectedSHA string `json:"expected_sha"`
	ActualSHA   string `json:"actual_sha"`
	Tag         string `json:"tag"`
	TS          int64  `json:"ts"`
}

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
