package translate

import (
	"encoding/json"

	"github.com/cplieger/vibekit/internal/api"
)

// noiseRule documents one tool_call title that duplicates the crew card.
type noiseRule struct {
	Title  string // exact match against toolCall.title
	Reason string // why this title is noise (for maintainers)
}

// subagentNoiseRules lists tool_call titles that are suppressed from
// the main chat flow because they duplicate information already
// visible in the crew card UI. Matching is exact on toolCall.title.
// To add a new rule, append a noiseRule{Title, Reason} entry.
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

// MarshalCrew produces a stable byte digest of the crew snapshot for
// dedup comparison.
func MarshalCrew(c *api.Crew) []byte {
	b, err := json.Marshal(c)
	if err != nil {
		return nil
	}
	return b
}
