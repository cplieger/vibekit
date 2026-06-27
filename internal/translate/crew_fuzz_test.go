package translate

import (
	"encoding/json"
	"testing"
)

// FuzzCrewFromWire pins the wire→domain mapping invariants of
// crewFromWire across arbitrary payloads: every wire subagent maps to
// exactly one domain subagent, the crew group is taken from the first
// subagent, and pending stages are count-preserved. MarshalCrew of the
// result must not panic.
func FuzzCrewFromWire(f *testing.F) {
	f.Add([]byte(`{"subagents":[{"sessionId":"s1","sessionName":"worker","agentName":"a","initialQuery":"q","group":"g1","role":"impl","status":{"type":"running"},"dependsOn":["x"]}],"pendingStages":[{"name":"stage1","agentName":"a2","role":"review","dependsOn":["s1"]}]}`))
	f.Add([]byte(`{"subagents":[{"group":"g","status":{"type":"done","message":"ok"}}]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"subagents":[]}`))
	f.Add([]byte(`null`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var p CrewNotifPayload
		if json.Unmarshal(data, &p) != nil {
			return
		}
		// Mirror the handler guard: skip empty Subagents.
		if len(p.Subagents) == 0 {
			return
		}
		crew := crewFromWire(&p)
		if crew == nil {
			t.Fatal("crewFromWire returned nil for non-empty Subagents")
		}
		if len(crew.Subagents) != len(p.Subagents) {
			t.Fatalf("crew.Subagents len = %d, want %d (every wire subagent must map)", len(crew.Subagents), len(p.Subagents))
		}
		if crew.Group != p.Subagents[0].Group {
			t.Fatalf("crew.Group = %q, want %q (group comes from the first subagent)", crew.Group, p.Subagents[0].Group)
		}
		if len(crew.PendingStages) != len(p.PendingStages) {
			t.Fatalf("crew.PendingStages len = %d, want %d (count must be preserved)", len(crew.PendingStages), len(p.PendingStages))
		}
		// Round-trip sanity: MarshalCrew must not panic.
		_ = MarshalCrew(crew)
	})
}
