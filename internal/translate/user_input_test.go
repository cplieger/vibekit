package translate

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// pendingCaptureDeps augments baseDeps with a PendingPermsAdd capture so
// the reconnect-replay registration is observable.
type pendingCaptureDeps struct {
	*baseDeps
	pendingAdds []int64
}

func (d *pendingCaptureDeps) PendingPermsAdd(id int64, _ vibekit.ServerEvent) {
	d.pendingAdds = append(d.pendingAdds, id)
}

func userInputMsg(t *testing.T, id *int64, params map[string]any) *vibekit.RPCResponse {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return &vibekit.RPCResponse{ID: id, Params: raw, Method: vibekit.MethodKiroUserInput}
}

func TestHandleUserInput(t *testing.T) {
	reqID := int64(7)

	t.Run("question with options broadcasts payload and registers pending", func(t *testing.T) {
		base, events := newEventCaptureDeps()
		deps := &pendingCaptureDeps{baseDeps: base}
		tr := New(deps)
		tr.HandleUserInput(t.Context(), "c1", userInputMsg(t, &reqID, map[string]any{
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

		var got *vibekit.UserInputNeededPayload
		for _, e := range *events {
			if e.Type == vibekit.EventUserInputNeeded {
				p := e.Payload.(vibekit.UserInputNeededPayload)
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
		tr.HandleUserInput(t.Context(), "c1", userInputMsg(t, &reqID, map[string]any{
			"sessionId": "sess_1",
			"question":  "Describe the goal",
		}))
		for _, e := range *events {
			if e.Type == vibekit.EventUserInputNeeded {
				if p := e.Payload.(vibekit.UserInputNeededPayload); len(p.Options) != 0 {
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
		tr.HandleUserInput(t.Context(), "c1", userInputMsg(t, nil, map[string]any{
			"question": "unanswerable",
		}))
		if len(*events) != 0 || len(deps.pendingAdds) != 0 {
			t.Errorf("un-answerable request must be dropped: events=%d pending=%v", len(*events), deps.pendingAdds)
		}
	})
}

// The agent composes userInput options, so they are model output on a trusted
// channel. These cases pin the three ways forwarding them unchecked goes wrong,
// each of which fails silently rather than loudly.
func TestSanitizeUserInputOptions(t *testing.T) {
	t.Run("drops an empty title, because the title IS the answer", func(t *testing.T) {
		// The reply carries the option's title text, so an empty one sends "" to
		// the agent and renders a card nobody can read.
		got := sanitizeUserInputOptions([]wireUserInputOption{
			{Title: "Keep"}, {Title: "   "}, {Title: ""}, {Title: "Discard"},
		})
		if len(got) != 2 || got[0].Title != "Keep" || got[1].Title != "Discard" {
			t.Errorf("got %+v, want just Keep and Discard", got)
		}
	})

	t.Run("drops a duplicate title, which is ambiguous by construction", func(t *testing.T) {
		got := sanitizeUserInputOptions([]wireUserInputOption{
			{Title: "Retry"}, {Title: "Retry"}, {Title: "Cancel"},
		})
		if len(got) != 2 {
			t.Errorf("got %d options, want 2 (the second Retry is unanswerable)", len(got))
		}
	})

	t.Run("trims, so a padded title is not mistaken for a distinct one", func(t *testing.T) {
		got := sanitizeUserInputOptions([]wireUserInputOption{
			{Title: " Retry "}, {Title: "Retry"},
		})
		if len(got) != 1 || got[0].Title != "Retry" {
			t.Errorf("got %+v, want one trimmed Retry", got)
		}
	})

	t.Run("bounds the list so it cannot push the composer off screen", func(t *testing.T) {
		in := make([]wireUserInputOption, maxUserInputOptions+10)
		for i := range in {
			in[i].Title = "opt-" + strconv.Itoa(i)
		}
		if got := sanitizeUserInputOptions(in); len(got) != maxUserInputOptions {
			t.Errorf("got %d options, want the cap %d", len(got), maxUserInputOptions)
		}
	})

	t.Run("applies the same three rules to sub-options", func(t *testing.T) {
		in := []wireUserInputOption{{
			Title: "Pick",
			SubOptions: []wireUserInputSubOption{
				{Title: "a"}, {Title: ""}, {Title: "a"}, {Title: "b"},
			},
		}}
		got := sanitizeUserInputOptions(in)
		if len(got) != 1 || len(got[0].SubOptions) != 2 {
			t.Fatalf("got %+v, want one option with two usable sub-options", got)
		}
	})

	t.Run("keeps a legitimate question whole", func(t *testing.T) {
		in := []wireUserInputOption{
			{Title: "Yes", Description: "do it", Recommended: true},
			{Title: "No", Description: "stop"},
		}
		got := sanitizeUserInputOptions(in)
		if len(got) != 2 || !got[0].Recommended || got[0].Description != "do it" {
			t.Errorf("got %+v, want both options with their metadata intact", got)
		}
	})

	// A question whose every option is unusable still returns empty rather than
	// nil-vs-empty ambiguity, and the caller treats empty options as free-form.
	t.Run("returns empty, not nil, when nothing survives", func(t *testing.T) {
		got := sanitizeUserInputOptions([]wireUserInputOption{{Title: ""}})
		if got == nil || len(got) != 0 {
			t.Errorf("got %#v, want an empty non-nil slice", got)
		}
	})
}
