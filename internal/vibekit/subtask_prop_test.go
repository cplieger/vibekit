package vibekit

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// TestParseStepSubtask_RoundTripsWhatStepSubtaskIDMints is the inverse property:
// the mint and the parse are the two halves of one format, so anything one
// produces the other must read back unchanged.
func TestParseStepSubtask_RoundTripsWhatStepSubtaskIDMints(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// A workflow id carries no colon (KAS mints it), which is what makes the
		// FIRST colon after the prefix an unambiguous split. The node path is drawn
		// unrestricted, colons included, because that is the half the split rule has
		// to survive.
		workflowID := strings.ReplaceAll(rapid.String().Draw(t, "workflowID"), ":", "")
		nodePath := rapid.String().Draw(t, "nodePath")
		if workflowID == "" || nodePath == "" {
			return
		}
		id := StepSubtaskID(workflowID, nodePath)
		got, ok := ParseStepSubtask(id)
		if !ok {
			t.Fatalf("ParseStepSubtask(%q) reported not a step subtask", id)
		}
		want := StepSubtask{WorkflowID: workflowID, NodePath: nodePath}
		if got != want {
			t.Fatalf("ParseStepSubtask(%q) = %+v, want %+v", id, got, want)
		}
	})
}

func TestParseStepSubtask_RefusesTheShapesTheTypeScriptTwinRefuses(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{name: "no_second_colon", id: "wf:only-one-segment"},
		{name: "empty_workflow_id", id: "wf::path"},
		{name: "empty_node_path", id: "wf:id:"},
		{name: "subagent_uuid", id: "c9d0a1b2-3e4f-5a6b-7c8d-9e0f1a2b3c4d"},
		{name: "empty", id: ""},
		{name: "prefix_alone", id: StepSubtaskPrefix},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseStepSubtask(tc.id)
			if ok {
				t.Errorf("ParseStepSubtask(%q) = %+v, true; want not a step subtask", tc.id, got)
			}
			if got != (StepSubtask{}) {
				t.Errorf("ParseStepSubtask(%q) returned %+v alongside its refusal, want the zero value", tc.id, got)
			}
		})
	}
}

// A malformed `wf:` id is still the chat's own work, so the prefix test accepts
// what the parse refuses. That asymmetry is what the renderer's delegate-box
// fallback depends on, which is why it is pinned rather than left to the two
// functions' doc comments.
func TestIsStepSubtask_AcceptsAMalformedIDTheParseRefuses(t *testing.T) {
	const id = "wf:only-one-segment"
	if !IsStepSubtask(id) {
		t.Errorf("IsStepSubtask(%q) = false, want true", id)
	}
	if _, ok := ParseStepSubtask(id); ok {
		t.Errorf("ParseStepSubtask(%q) accepted a malformed id", id)
	}
}

func TestIsStepSubtask_RejectsASubagentID(t *testing.T) {
	const id = "c9d0a1b2-3e4f-5a6b-7c8d-9e0f1a2b3c4d"
	if IsStepSubtask(id) {
		t.Errorf("IsStepSubtask(%q) = true, want false", id)
	}
}
