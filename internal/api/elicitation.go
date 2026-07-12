package api

// MCP elicitation types. When an MCP server needs structured input
// mid-tool-execution it sends elicitation/create to kiro-cli (its MCP
// client); kiro-cli forwards it to us over ACP. We render a form from
// RequestedSchema, collect the user's answer, and reply with an
// ElicitationResult ({action, content}).
//
// The schema shapes mirror kiro-cli's wire contract (verified against
// the kiro-cli 2.6.0 agent + the Kiro IDE 0.3.607 acp-type-covenant
// zod schemas). ElicitationPropertySchema is modelled as one flat
// struct with a Type discriminator rather than a Go union; the client
// branches on Type when rendering each field.

import "encoding/json"

// ElicitationPropertySchema describes one field in an elicitation form.
// Type is the discriminator ("string" | "number" | "integer" |
// "boolean" | "array"); the other fields are populated as relevant to
// that type. Unset fields are omitted on the wire.
type ElicitationPropertySchema struct {
	Default     any      `json:"default,omitempty"`
	MinLength   *int     `json:"minLength,omitempty"`
	MaxLength   *int     `json:"maxLength,omitempty"`
	Type        string   `json:"type"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Format      string   `json:"format,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// ElicitationRequestSchema is the JSON-schema-shaped object describing a
// form: a map of property name to its schema, plus the required set.
type ElicitationRequestSchema struct {
	Type        string                               `json:"type,omitempty"`
	Title       string                               `json:"title,omitempty"`
	Description string                               `json:"description,omitempty"`
	Properties  map[string]ElicitationPropertySchema `json:"properties,omitempty"`
	Required    []string                             `json:"required,omitempty"`
}

// ElicitationNeededPayload is the SSE payload for type="elicitation_needed".
// Mirrors PermissionNeededPayload's role: it carries everything the
// client needs to render the dialog plus the RequestID to echo back in
// the elicitation_response command. Mode is "form" or "url"; URL-mode
// elicitations carry URL and no RequestedSchema.
type ElicitationNeededPayload struct {
	RequestedSchema *ElicitationRequestSchema `json:"requested_schema,omitempty"`
	Mode            string                    `json:"mode,omitempty"`
	Message         string                    `json:"message,omitempty"`
	URL             string                    `json:"url,omitempty"`
	ToolCallID      string                    `json:"tool_call_id,omitempty"`
	SubSessionID    string                    `json:"sub_session_id,omitempty"`
	RequestID       int64                     `json:"request_id"`
}

// Elicitation actions per the MCP ElicitResult contract.
const (
	ElicitationActionAccept  = "accept"
	ElicitationActionDecline = "decline"
	ElicitationActionCancel  = "cancel"
)

// ElicitationResult is the response we send back on elicitation/create.
// Action is one of accept/decline/cancel; Content carries the filled
// form values (object) only on accept.
type ElicitationResult struct {
	Action  string          `json:"action"`
	Content json.RawMessage `json:"content,omitempty"`
}
