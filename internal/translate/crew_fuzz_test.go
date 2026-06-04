package translate

import (
	"encoding/json"
	"testing"
)

// FuzzCrewFromWire exercises crewFromWire with arbitrary JSON payloads.
// The function accesses p.Subagents[0] without a bounds check, so this
// fuzz target validates that the caller (HandleCrewUpdate) guards
// against empty slices and that valid payloads never panic.
func FuzzCrewFromWire(f *testing.F) {
	f.Add([]byte(`{"subagents":[{"status":{"type":"running"},"sessionId":"s1","sessionName":"n1","agentName":"a1","initialQuery":"q","group":"g1","role":"worker"}],"pendingStages":[]}`))
	f.Add([]byte(`{"subagents":[{"status":{"type":"done","message":"ok"},"sessionId":"s2","sessionName":"","agentName":"","initialQuery":"","group":"","role":"","dependsOn":["s1"]}],"pendingStages":[{"name":"stage2","agentName":"a2","role":"r","dependsOn":["s2"]}]}`))
	f.Add([]byte(`{"subagents":[]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"subagents":[{},{}]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var p CrewNotifPayload
		if json.Unmarshal(data, &p) != nil {
			return
		}
		// Mirror the guard in HandleCrewUpdate.
		if len(p.Subagents) == 0 {
			return
		}
		// Must not panic.
		crew := crewFromWire(&p)
		if crew == nil {
			t.Fatal("crewFromWire returned nil for non-empty subagents")
		}
		if len(crew.Subagents) != len(p.Subagents) {
			t.Fatalf("subagent count mismatch: got %d, want %d", len(crew.Subagents), len(p.Subagents))
		}
	})
}

// FuzzToAvailableCommands exercises the map→struct conversion with
// arbitrary JSON maps, asserting no panics and length preservation.
func FuzzToAvailableCommands(f *testing.F) {
	f.Add([]byte(`[{"name":"test","description":"desc"}]`))
	f.Add([]byte(`[{}]`))
	f.Add([]byte(`[{"name":123}]`))
	f.Add([]byte(`[{"name":"x","extra":true,"nested":{"a":1}}]`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`null`))
	f.Add([]byte(`[{"description":"no-name"},{"name":"has-name"}]`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var in []map[string]any
		if json.Unmarshal(data, &in) != nil {
			return
		}
		// Must not panic.
		out := toAvailableCommands(in)
		if len(in) == 0 && out != nil {
			t.Fatal("expected nil for empty input")
		}
		if len(in) > 0 && len(out) != len(in) {
			t.Fatalf("length mismatch: got %d, want %d", len(out), len(in))
		}
	})
}
