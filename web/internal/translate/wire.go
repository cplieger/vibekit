// Package translate handles ACP → domain event translation.
//
// The Translator receives raw ACP notifications from kiro-cli bridges
// and converts them into domain events (SSE broadcasts, chat-store
// mutations). Hub remains the coordinator; this package owns the
// protocol-specific decode + dispatch logic.
package translate

import (
	"encoding/json"

	"vibekit/internal/api"
)

// Shared ACP wire-format decode types for the translate layer. These
// replace per-handler anonymous structs so the ACP protocol surface is
// explicit, greppable, and maintained in one place. When kiro-cli adds
// a field, it lands here once rather than in N handler-local structs.

// ACPChunkWire is the wire shape for agent_message_chunk and
// agent_thought_chunk session updates.
type ACPChunkWire struct {
	Content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// ACPToolCallContentBlock is one element in a tool_call or
// tool_call_update's content array.
type ACPToolCallContentBlock struct {
	Type    string `json:"type"`
	Path    string `json:"path"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
	Content struct {
		Text string `json:"text"`
	} `json:"content"`
}

// ACPToolCallWire is the wire shape for tool_call session updates.
type ACPToolCallWire struct {
	ToolCallID string                    `json:"toolCallId"`
	Title      string                    `json:"title"`
	Kind       api.ToolKind              `json:"kind"`
	Status     api.ToolStatus            `json:"status"`
	RawInput   json.RawMessage           `json:"rawInput"`
	Locations  []api.ToolLocation        `json:"locations"`
	Content    []ACPToolCallContentBlock `json:"content"`
}

// ACPToolCallUpdateWire is the wire shape for tool_call_update session
// updates.
type ACPToolCallUpdateWire struct {
	ToolCallID string                    `json:"toolCallId"`
	Status     api.ToolStatus            `json:"status"`
	Locations  []api.ToolLocation        `json:"locations"`
	Content    []ACPToolCallContentBlock `json:"content"`
}

// ACPPlanWire is the wire shape for plan session updates.
type ACPPlanWire struct {
	Entries []api.PlanEntry `json:"entries"`
}

// ACPModeUpdateWire is the wire shape for current_mode_update session
// updates.
type ACPModeUpdateWire struct {
	ModeID string `json:"modeId"`
}

// ACPSteeringWire is the wire shape for steering_inclusion session
// updates.
type ACPSteeringWire struct {
	Documents []struct {
		Name string `json:"name"`
		Path string `json:"path"`
	} `json:"documents"`
}

// ACPSessionUpdateEnvelope is the outer envelope for session/update
// notifications.
type ACPSessionUpdateEnvelope struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// ACPSessionUpdateBase extracts the sessionUpdate kind discriminator.
type ACPSessionUpdateBase struct {
	Kind api.ACPUpdateKind `json:"sessionUpdate"`
}

// JSON field name constants — the wire protocol uses these strings
// in many places; constants keep them in one place and silence
// goconst warnings.
const (
	jsonFieldName    = "name"
	jsonFieldContent = "content"
)

// ContentTypeContent is the ACP content-block type discriminator value "content".
// Distinct from jsonFieldContent which is the JSON field *name* "content".
const ContentTypeContent = "content"
