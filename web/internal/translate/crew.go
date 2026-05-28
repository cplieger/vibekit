package translate

import (
	"encoding/json"

	"vibekit/internal/api"
)

// noiseRule documents one tool_call title that duplicates the crew card.
type noiseRule struct {
	Title  string // exact match against toolCall.title
	Reason string // why this title is noise (for maintainers)
}

// subagentNoiseRules is the declarative table of tool_call titles that
// duplicate the crew card.
var subagentNoiseRules = []noiseRule{
	{"Summarizing", "duplicates crew card summary phase"},
	{"Spawning agent crew", "duplicates crew card spawn phase"},
	{"subagent", "generic subagent tool entry"},
	{"summary", "duplicates crew card completion summary"},
}

// subagentNoiseTitles is the lookup map built from subagentNoiseRules.
var subagentNoiseTitles = func() map[string]struct{} {
	m := make(map[string]struct{}, len(subagentNoiseRules))
	for _, r := range subagentNoiseRules {
		m[r.Title] = struct{}{}
	}
	return m
}()

// IsSubagentNoiseTitle returns true if the title should be filtered
// from the main chat flow (it duplicates the crew card).
func IsSubagentNoiseTitle(title string) bool {
	_, ok := subagentNoiseTitles[title]
	return ok
}

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

// MarshalCrew produces a stable byte digest of the crew snapshot for
// dedup comparison.
func MarshalCrew(c *api.Crew) []byte {
	b, err := json.Marshal(c)
	if err != nil {
		return nil
	}
	return b
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
