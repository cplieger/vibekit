package api

// Server events: the wire shapes broadcast over SSE (/api/events) +
// per-type payloads. Event dispatch lives in hub/sse.go and the
// per-method translation lives in hub/translate*.go.
//
// Payload structs live in events_payloads.go; this file contains the
// envelope types, event-type constants, and working-label logic.

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

// StopReasonEndTurn and the following constants define the valid StopReason values for turn termination.
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
	EventElicitationNeeded     EventType = "elicitation_needed"
	EventElicitationComplete   EventType = "elicitation_complete"
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

// WorkingLabelForKind maps a tool kind to a human-readable label.
// Matches ASAI's VV() function from the frontend reducer.
func WorkingLabelForKind(kind ToolKind, title string) string {
	const labelRunning = "Running"
	//exhaustive:enforce
	switch kind {
	case ToolKindExecute, ToolKindShell:
		if title != "" {
			return labelRunning + " " + title
		}
		return labelRunning
	case ToolKindRead:
		return "Reading"
	case ToolKindSearch:
		return "Searching"
	case ToolKindFetch:
		return "Fetching"
	case ToolKindEdit, ToolKindWrite:
		return "Writing"
	case ToolKindThink:
		return "Reasoning"
	case ToolKindDelete:
		return "Deleting"
	case ToolKindMove:
		return "Moving"
	case ToolKindCommand:
		return labelRunning
	case ToolKindBrowser:
		return "Browsing"
	case ToolKindSwitchMode:
		return "Switching"
	case ToolKindMCP:
		return labelRunning
	case ToolKindHook:
		return "Running hook"
	case ToolKindOther:
		return WorkingLabelThinking
	}
	return WorkingLabelThinking
}

// Working-label constants. Centralised so hub callers reference these
// instead of bare string literals; a future label rename lands in one place.
const (
	WorkingLabelThinking = "Thinking"
	WorkingLabelApproval = "Waiting for approval"
	WorkingLabelInput    = "Waiting for input"
)
