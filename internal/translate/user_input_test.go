package translate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// pendingCaptureDeps augments baseDeps with a PendingPermsAdd capture so
// the reconnect-replay registration is observable.
type pendingCaptureDeps struct {
	*baseDeps
	pendingAdds []int64
}

func (d *pendingCaptureDeps) PendingPermsAdd(id int64, _ api.ServerEvent) {
	d.pendingAdds = append(d.pendingAdds, id)
}

func userInputMsg(t *testing.T, id *int64, params map[string]any) *api.RPCResponse {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return &api.RPCResponse{ID: id, Params: raw, Method: api.MethodKiroUserInput}
}

func TestHandleUserInput(t *testing.T) {
	reqID := int64(7)

	t.Run("question with options broadcasts payload and registers pending", func(t *testing.T) {
		base, events := newEventCaptureDeps()
		deps := &pendingCaptureDeps{baseDeps: base}
		tr := New(deps)
		tr.HandleUserInput(context.Background(), "c1", userInputMsg(t, &reqID, map[string]any{
			"sessionId":  "sess_1",
			"toolCallId": "tc-9",
			"question":   "Which approach?",
			"options": []map[string]any{
				{"title": "Fast", "description": "Quick and dirty", "recommended": true},
				{
					"title":           "Thorough",
					"subOptionsLabel": "Include:",
					"subOptions":      []map[string]any{{"title": "Tests"}, {"title": "Docs", "description": "README too"}},
				},
			},
		}))

		var got *api.UserInputNeededPayload
		for _, e := range *events {
			if e.Type == api.EventUserInputNeeded {
				p := e.Payload.(api.UserInputNeededPayload)
				got = &p
			}
		}
		if got == nil {
			t.Fatal("no user_input_needed event broadcast")
		}
		if got.RequestID != reqID || got.Question != "Which approach?" || got.ToolCallID != "tc-9" {
			t.Errorf("payload envelope wrong: %+v", got)
		}
		if len(got.Options) != 2 {
			t.Fatalf("options: %+v", got.Options)
		}
		if !got.Options[0].Recommended || got.Options[0].Description != "Quick and dirty" {
			t.Errorf("option 0 mapping: %+v", got.Options[0])
		}
		o1 := got.Options[1]
		if o1.SubOptionsLabel != "Include:" || len(o1.SubOptions) != 2 || o1.SubOptions[1].Description != "README too" {
			t.Errorf("sub-options mapping: %+v", o1)
		}
		if len(deps.pendingAdds) != 1 || deps.pendingAdds[0] != reqID {
			t.Errorf("pending replay registration: %v", deps.pendingAdds)
		}
	})

	t.Run("free-form question keeps an empty options list", func(t *testing.T) {
		base, events := newEventCaptureDeps()
		deps := &pendingCaptureDeps{baseDeps: base}
		tr := New(deps)
		tr.HandleUserInput(context.Background(), "c1", userInputMsg(t, &reqID, map[string]any{
			"sessionId": "sess_1",
			"question":  "Describe the goal",
		}))
		for _, e := range *events {
			if e.Type == api.EventUserInputNeeded {
				if p := e.Payload.(api.UserInputNeededPayload); len(p.Options) != 0 {
					t.Errorf("expected free-form (no options), got %+v", p.Options)
				}
				return
			}
		}
		t.Fatal("no user_input_needed event broadcast")
	})

	t.Run("request without id is dropped", func(t *testing.T) {
		base, events := newEventCaptureDeps()
		deps := &pendingCaptureDeps{baseDeps: base}
		tr := New(deps)
		tr.HandleUserInput(context.Background(), "c1", userInputMsg(t, nil, map[string]any{
			"question": "unanswerable",
		}))
		if len(*events) != 0 || len(deps.pendingAdds) != 0 {
			t.Errorf("un-answerable request must be dropped: events=%d pending=%v", len(*events), deps.pendingAdds)
		}
	})
}
