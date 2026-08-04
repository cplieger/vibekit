package api

// Agent user-input question types (kiro-cli v3 _kiro/userInput, 2.14+).
//
// The agent's user_input tool asks the user a structured question mid-turn
// (plan-mode clarifications, spec gates). Because vibekit declares the
// initialize capability _meta.kiro.userInput:true, KAS forwards the
// question as an A→C JSON-RPC request instead of flattening it into a
// permission prompt; the answer returns on the request id as
// {action:"answered", answer:"<text>"} (anything else advances the agent
// to its next phase). Mirrors the elicitation types in elicitation.go.

// UserInputSubOption is one second-level choice under a UserInputOption.
// Sub-options render as a pre-checked multi-select (the TUI's behavior);
// the selected titles fold into the answer text as "Parent [Sub1, Sub2]".
type UserInputSubOption struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// UserInputOption is one selectable answer for a user-input question.
// Recommended marks the option the agent suggests (rendered as a badge).
type UserInputOption struct {
	Title           string               `json:"title"`
	Description     string               `json:"description,omitempty"`
	SubOptionsLabel string               `json:"sub_options_label,omitempty"`
	SubOptions      []UserInputSubOption `json:"sub_options,omitempty"`
	Recommended     bool                 `json:"recommended,omitempty"`
}

// UserInputNeededPayload is the payload for type="user_input_needed": the
// agent asked a structured question and awaits an answer. An empty Options
// slice means a free-form question (the client renders a text field).
// RequestID is the JSON-RPC id the user_input_response command echoes back.
type UserInputNeededPayload struct {
	Question     string `json:"question"`
	ToolCallID   string `json:"tool_call_id,omitempty"`
	SubSessionID string `json:"sub_session_id,omitempty"`
	// RunID/NodeID: as on PermissionNeededPayload — a workflow step's ask,
	// attributed to its run and node from the step-session registry.
	RunID     string            `json:"run_id,omitempty"`
	NodeID    string            `json:"node_id,omitempty"`
	Options   []UserInputOption `json:"options,omitempty"`
	RequestID int64             `json:"request_id"`
}
