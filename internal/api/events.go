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

// ServerEvent is the server→client event envelope broadcast to every SSE
// client: {type, chat_id, payload}. Payload is the event-specific JSON
// object; handlers type-assert based on Type. Construct it through NewEvent,
// which keeps the payload typed at the emit site.
type ServerEvent struct {
	Payload any       `json:"payload,omitempty"`
	Type    EventType `json:"type"`
	ChatID  ChatID    `json:"chat_id,omitempty"`
}

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
	// StopReasonRefusal is kiro-cli 2.13+'s core ACP stop reason for a model
	// refusal (content_filtered): the refusal explanation streamed as the last
	// assistant chunk, tagged with _meta.kiro.refusal (api.RefusalInfo).
	StopReasonRefusal StopReason = "refusal"
)

// SSE event type constants. Using these instead of bare string literals
// makes typos a compile error and the full event vocabulary discoverable.
const (
	EventChatCreated       EventType = "chat_created"
	EventChatUpdated       EventType = "chat_updated"
	EventChatDeleted       EventType = "chat_deleted"
	EventChatStatus        EventType = "chat_status"
	EventCodeReferences    EventType = "code_references"
	EventCompactionStarted EventType = "compaction_started"
	EventConnected         EventType = "connected"
	// EventDecisionSettled retires an ask on every surface that did NOT answer
	// it. The three *_needed events below are offered to every tab and to a
	// watching run tab at once, while only the first answer is accepted, so
	// something has to close the others — and it carries attribution, because a
	// card that collapses for no stated reason reads as a lost click.
	EventDecisionSettled   EventType = "decision_settled"
	EventError             EventType = "error"
	EventElicitationNeeded EventType = "elicitation_needed"
	EventUserInputNeeded   EventType = "user_input_needed"
	EventMCPConfigChanged  EventType = "mcp_config_changed"
	EventMCPConnected      EventType = "mcp_connected"
	EventMCPDisconnected   EventType = "mcp_disconnected"
	EventMCPFailed         EventType = "mcp_failed"
	EventMCPOAuthNeeded    EventType = "mcp_oauth_needed"
	EventMCPPrewarm        EventType = "mcp_prewarm"
	EventMessageAppended   EventType = "message_appended"
	EventMessageChunk      EventType = "message_chunk"
	EventMessageCreated    EventType = "message_created"
	EventMessageUpdated    EventType = "message_updated"
	EventModeChanged       EventType = "mode_changed"
	EventOpenExternalURL   EventType = "open_external_url"
	// EventPermissionNeeded carries a turn's verdict as well as an individual
	// tool ask. There are no pending_change_* or pending_trust_* events:
	// staged writes are KAS's.
	EventPermissionNeeded   EventType = "permission_needed"
	EventPermissionsChanged EventType = "permissions_changed"
	EventPolicyError        EventType = "policy_error"
	// EventRunStarted and the two below are the workflow-run lifecycle. Three,
	// not six — see domain_workflow.go for what the other three would have been
	// and why none of them can exist. All ride the launching chat's topic.
	EventRunStarted       EventType = "run_started"
	EventRunProgress      EventType = "run_progress"
	EventRunFinished      EventType = "run_finished"
	EventForgesChanged    EventType = "forges_changed"
	EventGovernanceState  EventType = "governance_state"
	EventHooksChanged     EventType = "hooks_changed"
	EventSafetyStatus     EventType = "safety_status"
	EventSafetyProperties EventType = "safety_properties"
	EventSettingsUpdated  EventType = "settings_updated"
	// EventSteerQueued and the two below mirror KAS's own three mid-turn
	// steering signals rather than collapsing them. Each answers a different
	// question the chip row asks: queued = it reached the buffer, injected =
	// the model has actually read it, cleared = the turn boundary dropped
	// whatever was still unread. A single "steer_changed" event would make
	// "sent but not yet seen" indistinguishable from "seen", which is the one
	// distinction a user steering a live turn cares about.
	EventSteerQueued   EventType = "steer_queued"
	EventSteerInjected EventType = "steer_injected"
	EventSteerCleared  EventType = "steer_cleared"
	// A notice the AGENT produced, not the user: a workflow step or a subagent
	// reporting progress into the session that launched it.
	//
	// It arrives on KAS's steering channel because that buffer is the only
	// inbound path into a live turn, and it used to be forwarded as a
	// steer_queued carrying a severity. That put it on the chip row inside the
	// composer, whose entire vocabulary is about the USER's outbound messages:
	// waiting for the agent, read by the agent, discard the ones it has not
	// read. None of those is true of text the agent wrote itself, and the
	// discard control cannot meaningfully act on it. So it is its own event,
	// and the severity is a required field of it rather than an optional flag
	// on somebody else's payload.
	EventAgentNotice     EventType = "agent_notice"
	EventTerminalCreated EventType = "terminal_created"
	EventTerminalExited  EventType = "terminal_exited"
	EventTerminalOutput  EventType = "terminal_output"
	EventToolCall        EventType = "tool_call"
	EventToolCallUpdate  EventType = "tool_call_update"
	EventToolJobChanged  EventType = "tool_job_changed"
	EventToolJobOutput   EventType = "tool_job_output"
	EventTurnEnded       EventType = "turn_ended"
	EventTurnState       EventType = "turn_state"
	EventWorkingLabel    EventType = "working_label"
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
