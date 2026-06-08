// Package translate handles ACP → domain event translation.
//
// The Translator receives raw ACP notifications from kiro-cli bridges
// and converts them into domain events (SSE broadcasts, chat-store
// mutations). Hub remains the coordinator; this package owns the
// protocol-specific decode + dispatch logic.
package translate

import (
	"encoding/json"

	"github.com/cplieger/vibekit/internal/api"
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

// ContentTypeContent is the ACP content-block type discriminator value "content".
// Distinct from jsonFieldContent which is the JSON field *name* "content".
const ContentTypeContent = "content"

// ContentTypeDiff is the ACP content-block type for file-change diffs
// in tool_call and tool_call_update payloads.
const ContentTypeDiff = "diff"

// ExtUpdateToolCallChunk is the extension session-update subtype for
// subagent tool-call streaming chunks.
const ExtUpdateToolCallChunk = "tool_call_chunk"

// --- Crew notification wire types ---

// CrewNotifPayload mirrors kiro-cli's wire format for subagent/list_update.
type CrewNotifPayload struct {
	Subagents     []CrewNotifSubagent     `json:"subagents"`
	PendingStages []CrewNotifPendingStage `json:"pendingStages"`
}

// CrewNotifSubagent is one subagent in the crew notification.
type CrewNotifSubagent struct {
	Status struct {
		Type    string `json:"type"`
		Message string `json:"message,omitempty"`
	} `json:"status"`
	SessionID    string   `json:"sessionId"`
	SessionName  string   `json:"sessionName"`
	AgentName    string   `json:"agentName"`
	InitialQuery string   `json:"initialQuery"`
	Group        string   `json:"group"`
	Role         string   `json:"role"`
	DependsOn    []string `json:"dependsOn,omitempty"`
}

// CrewNotifPendingStage is one pending stage in the crew notification.
type CrewNotifPendingStage struct {
	Name      string   `json:"name"`
	AgentName string   `json:"agentName"`
	Role      string   `json:"role"`
	DependsOn []string `json:"dependsOn,omitempty"`
}

// crewFromWire converts a wire-format CrewNotifPayload into the domain
// type *api.Crew. This is the single place where wire→domain field
// mapping lives; when kiro-cli adds fields to the notification, only
// this adapter changes.
func crewFromWire(p *CrewNotifPayload) *api.Crew {
	crew := &api.Crew{
		Group:     p.Subagents[0].Group,
		Subagents: make([]api.CrewSubagent, len(p.Subagents)),
	}
	for i := range p.Subagents {
		s := &p.Subagents[i]
		crew.Subagents[i] = api.CrewSubagent{
			SessionID:    s.SessionID,
			SessionName:  s.SessionName,
			AgentName:    s.AgentName,
			InitialQuery: s.InitialQuery,
			Status:       api.CrewStatus(s.Status.Type),
			StatusMsg:    s.Status.Message,
			Group:        s.Group,
			Role:         s.Role,
			DependsOn:    s.DependsOn,
		}
	}
	if len(p.PendingStages) > 0 {
		crew.PendingStages = make([]api.CrewPendingStage, len(p.PendingStages))
		for i := range p.PendingStages {
			ps := &p.PendingStages[i]
			crew.PendingStages[i] = api.CrewPendingStage{
				Name:      ps.Name,
				AgentName: ps.AgentName,
				Role:      ps.Role,
				DependsOn: ps.DependsOn,
			}
		}
	}
	return crew
}
