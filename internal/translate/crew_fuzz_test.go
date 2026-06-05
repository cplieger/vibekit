package translate

import (
	"encoding/json"
	"testing"
)

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
		// Round-trip sanity: MarshalCrew must not panic.
		_ = MarshalCrew(crew)
	})
}
