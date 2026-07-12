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
	EventCodeReferences        EventType = "code_references"
	EventCommandsUpdated       EventType = "commands_updated"
	EventCompactionStarted     EventType = "compaction_started"
	EventConflictDetected      EventType = "conflict_detected"
	EventConnected             EventType = "connected"
	EventError                 EventType = "error"
	EventElicitationNeeded     EventType = "elicitation_needed"
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
	EventOpenExternalURL       EventType = "open_external_url"
	EventPendingChangeAdded    EventType = "pending_change_added"
	EventPendingChangeResolved EventType = "pending_change_resolved"
	EventPendingChangesCleared EventType = "pending_changes_cleared"
	EventPendingTrustCleared   EventType = "pending_trust_cleared"
	EventPendingTrustEnabled   EventType = "pending_trust_enabled"
	EventPermissionNeeded      EventType = "permission_needed"
	EventPermissionsChanged    EventType = "permissions_changed"
	EventPolicyError           EventType = "policy_error"
	EventForgesChanged         EventType = "forges_changed"
	EventGovernanceState       EventType = "governance_state"
	EventHooksChanged          EventType = "hooks_changed"
	EventKnowledgeIndexing     EventType = "knowledge_indexing"
	EventSafetyStatus          EventType = "safety_status"
	EventSafetyProperties      EventType = "safety_properties"
	EventSpecTaskChanged       EventType = "spec_task_changed"
	EventSettingsUpdated       EventType = "settings_updated"
	EventTerminalCreated       EventType = "terminal_created"
	EventTerminalExited        EventType = "terminal_exited"
	EventTerminalOutput        EventType = "terminal_output"
	EventToolCall              EventType = "tool_call"
	EventToolCallUpdate        EventType = "tool_call_update"
	EventTurnEnded             EventType = "turn_ended"
	EventWorkingLabel          EventType = "working_label"
)

// labelRunning is the working label shared by the running-process kinds
// (shell/execute commands, plain commands, and MCP tool calls).
const labelRunning = "Running"

// workingLabelByKind maps each tool kind with a fixed working label to that
// label. Kinds whose label depends on runtime data (ToolKindExecute and
// ToolKindShell incorporate the title) are handled in WorkingLabelForKind
// directly; ToolKindOther and any unrecognized kind fall back to
// WorkingLabelThinking. Keep this in sync with the ToolKind constants.
var workingLabelByKind = map[ToolKind]string{
	ToolKindRead:       "Reading",
	ToolKindSearch:     "Searching",
	ToolKindFetch:      "Fetching",
	ToolKindEdit:       "Writing",
	ToolKindWrite:      "Writing",
	ToolKindThink:      "Reasoning",
	ToolKindDelete:     "Deleting",
	ToolKindMove:       "Moving",
	ToolKindCommand:    labelRunning,
	ToolKindBrowser:    "Browsing",
	ToolKindSwitchMode: "Switching",
	ToolKindMCP:        labelRunning,
	ToolKindHook:       "Running hook",
}

// WorkingLabelForKind maps a tool kind to a human-readable label.
// Matches ASAI's VV() function from the frontend reducer.
func WorkingLabelForKind(kind ToolKind, title string) string {
	if kind == ToolKindExecute || kind == ToolKindShell {
		if title != "" {
			return labelRunning + " " + title
		}
		return labelRunning
	}
	if label, ok := workingLabelByKind[kind]; ok {
		return label
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
