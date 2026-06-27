package translate

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// TestMarshalCrew_ReturnsNonNilForValidCrew pins that a valid crew
// snapshot is encoded to non-nil JSON bytes that round-trip back to the
// same group. MarshalCrew only returns nil when json.Marshal errors, so
// a successful marshal must surface the bytes rather than nil.
func TestMarshalCrew_ReturnsNonNilForValidCrew(t *testing.T) {
	crew := &api.Crew{
		Group:     "g1",
		Subagents: []api.CrewSubagent{{SessionID: "s1", Status: api.CrewWorking, Group: "g1"}},
	}
	got := MarshalCrew(crew)
	if got == nil {
		t.Fatal("MarshalCrew(validCrew) = nil, want non-nil JSON bytes")
	}
	if len(got) == 0 {
		t.Fatalf("MarshalCrew(validCrew) len = %d, want > 0", len(got))
	}
	var back api.Crew
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("MarshalCrew output is not valid JSON: %v", err)
	}
	if back.Group != "g1" {
		t.Errorf("MarshalCrew round-trip Group = %q, want %q", back.Group, "g1")
	}
}
