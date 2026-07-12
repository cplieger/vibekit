package translate

import (
	"context"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// specTaskChangedPayload extracts the single EventSpecTaskChanged payload,
// failing if there isn't exactly one.
func specTaskChangedPayload(t *testing.T, events *[]api.ServerEvent) (api.SpecTaskChangedPayload, api.ChatID) {
	t.Helper()
	var got []api.SpecTaskChangedPayload
	var chatID api.ChatID
	for _, e := range *events {
		if e.Type != api.EventSpecTaskChanged {
			continue
		}
		p, ok := e.Payload.(api.SpecTaskChangedPayload)
		if !ok {
			t.Fatalf("EventSpecTaskChanged payload type = %T, want api.SpecTaskChangedPayload", e.Payload)
		}
		got = append(got, p)
		chatID = e.ChatID
	}
	if len(got) != 1 {
		t.Fatalf("EventSpecTaskChanged broadcast count = %d, want 1", len(got))
	}
	return got[0], chatID
}

func countSpecEvents(events *[]api.ServerEvent) int {
	n := 0
	for _, e := range *events {
		if e.Type == api.EventSpecTaskChanged {
			n++
		}
	}
	return n
}

// TestHandleSpecTaskChanged pins that a taskStatusChanged notification
// broadcasts one global (empty chatID) spec_task_changed event, derives the
// feature name from tasksFilePath, forwards the raw path, and maps every
// change (executionStatus + provenance ids).
func TestHandleSpecTaskChanged(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, "/tmp")

	tr.HandleSpecTaskChanged(context.Background(), api.ChatID("c1"), knowledgeMsg(t, map[string]any{
		"sessionId":     "sess_1",
		"tasksFilePath": "/ws/.kiro/specs/my-feature/tasks.md",
		"changes": []map[string]any{
			{"taskId": "1.2 A", "executionStatus": "running", "lastSessionId": "sess_1", "lastExecutionId": "e1"},
			{"taskId": "2 B", "executionStatus": "aborted"},
		},
	}))

	p, chatID := specTaskChangedPayload(t, events)
	if chatID != "" {
		t.Errorf("event ChatID = %q, want empty (board is workspace-global)", chatID)
	}
	if p.FeatureName != "my-feature" {
		t.Errorf("FeatureName = %q, want my-feature", p.FeatureName)
	}
	if p.TasksFilePath != "/ws/.kiro/specs/my-feature/tasks.md" {
		t.Errorf("TasksFilePath = %q", p.TasksFilePath)
	}
	if len(p.Changes) != 2 {
		t.Fatalf("changes = %d, want 2", len(p.Changes))
	}
	if p.Changes[0].TaskID != "1.2 A" || p.Changes[0].ExecutionStatus != "running" ||
		p.Changes[0].LastSessionID != "sess_1" || p.Changes[0].LastExecutionID != "e1" {
		t.Errorf("change[0] = %+v", p.Changes[0])
	}
	if p.Changes[1].TaskID != "2 B" || p.Changes[1].ExecutionStatus != "aborted" {
		t.Errorf("change[1] = %+v", p.Changes[1])
	}
}

// TestHandleSpecTaskChanged_NoBroadcast pins the drop paths: an empty
// tasksFilePath, no changes, and malformed params each produce no event.
func TestHandleSpecTaskChanged_NoBroadcast(t *testing.T) {
	cases := []struct {
		name string
		msg  *api.RPCResponse
	}{
		{"empty tasksFilePath", knowledgeMsg(t, map[string]any{
			"changes": []map[string]any{{"taskId": "1", "executionStatus": "running"}},
		})},
		{"no changes", knowledgeMsg(t, map[string]any{
			"tasksFilePath": "/ws/.kiro/specs/f/tasks.md", "changes": []map[string]any{},
		})},
		{"malformed", &api.RPCResponse{Params: []byte("{")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, events := newEventCaptureDeps()
			tr := New(deps, "/tmp")
			tr.HandleSpecTaskChanged(context.Background(), api.ChatID("c1"), tc.msg)
			if n := countSpecEvents(events); n != 0 {
				t.Errorf("broadcast count = %d, want 0", n)
			}
		})
	}
}

func TestFeatureNameFromTasksPath(t *testing.T) {
	cases := map[string]string{
		"/ws/.kiro/specs/my-feature/tasks.md": "my-feature",
		".kiro/specs/rel-feature/tasks.md":    "rel-feature",
		"tasks.md":                            ".",
	}
	for in, want := range cases {
		if got := featureNameFromTasksPath(in); got != want {
			t.Errorf("featureNameFromTasksPath(%q) = %q, want %q", in, got, want)
		}
	}
}
