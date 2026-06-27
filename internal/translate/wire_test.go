package translate

import "testing"

// TestCrewFromWire_PendingStagesGate pins crewFromWire's handling of the
// pendingStages slice: when the wire payload carries no pending stages
// the domain crew leaves PendingStages nil (no allocation of an empty
// slice); when stages are present they are mapped field-for-field.
func TestCrewFromWire_PendingStagesGate(t *testing.T) {
	t.Run("NoPendingStagesLeavesNil", func(t *testing.T) {
		p := &CrewNotifPayload{
			Subagents: []CrewNotifSubagent{{SessionID: "s1", Group: "g1"}},
		}
		crew := crewFromWire(p)
		if crew.PendingStages != nil {
			t.Errorf("crew.PendingStages = %+v (len %d), want nil when there are no pending stages", crew.PendingStages, len(crew.PendingStages))
		}
	})
	t.Run("PendingStagesPopulated", func(t *testing.T) {
		p := &CrewNotifPayload{
			Subagents: []CrewNotifSubagent{{SessionID: "s1", Group: "g1"}},
			PendingStages: []CrewNotifPendingStage{
				{Name: "stage1", AgentName: "coder", Role: "impl", DependsOn: []string{"x"}},
			},
		}
		crew := crewFromWire(p)
		if got := len(crew.PendingStages); got != 1 {
			t.Fatalf("crew.PendingStages len = %d, want 1 (pending stages must be mapped when present)", got)
		}
		if got := crew.PendingStages[0].Name; got != "stage1" {
			t.Errorf("crew.PendingStages[0].Name = %q, want %q", got, "stage1")
		}
		if got := crew.PendingStages[0].AgentName; got != "coder" {
			t.Errorf("crew.PendingStages[0].AgentName = %q, want %q", got, "coder")
		}
	})
}
