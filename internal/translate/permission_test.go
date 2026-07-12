package translate

import (
	"context"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

func TestFindAllowOnce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		want    string
		options []api.PermissionOption
	}{
		{
			name: "MatchesByKind",
			options: []api.PermissionOption{
				{OptionID: "deny", Name: "Deny", Kind: "deny"},
				{OptionID: "opt-42", Name: "Allow once", Kind: "allow_once"},
			},
			want: "opt-42",
		},
		{
			name: "MatchesByOptionID",
			options: []api.PermissionOption{
				{OptionID: "deny", Name: "Deny", Kind: "deny"},
				{OptionID: "allow_once", Name: "Allow once", Kind: "other"},
			},
			want: "allow_once",
		},
		{
			name:    "EmptySliceReturnsEmpty",
			options: nil,
			want:    "",
		},
		{
			name: "NoMatchReturnsEmpty",
			options: []api.PermissionOption{
				{OptionID: "deny", Name: "Deny", Kind: "deny"},
				{OptionID: "always", Name: "Always allow", Kind: "always_allow"},
			},
			want: "",
		},
		{
			name: "PrefersFirstMatch",
			options: []api.PermissionOption{
				{OptionID: "first-allow", Name: "A", Kind: "allow_once"},
				{OptionID: "second-allow", Name: "B", Kind: "allow_once"},
			},
			want: "first-allow",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FindAllowOnce(tt.options)
			if got != tt.want {
				t.Errorf("FindAllowOnce() = %q, want %q", got, tt.want)
			}
		})
	}
}

// findPermissionNeeded returns the first permission_needed payload broadcast.
func findPermissionNeeded(t *testing.T, events *[]api.ServerEvent) (api.PermissionNeededPayload, bool) {
	t.Helper()
	for _, e := range *events {
		if e.Type != api.EventPermissionNeeded {
			continue
		}
		p, ok := e.Payload.(api.PermissionNeededPayload)
		if !ok {
			t.Fatalf("permission_needed payload type = %T, want api.PermissionNeededPayload", e.Payload)
		}
		return p, true
	}
	return api.PermissionNeededPayload{}, false
}

// TestHandlePermissionRequest_DecodesFlatParamsAndEnvelopeID pins the v3 decode
// contract: session/request_permission params are FLAT ({sessionId, toolCall,
// options}) and the correlation id is on the JSON-RPC envelope (msg.ID), not in
// params. The prior code wrapped the fields under a `params` object and read the
// id from params, so unmarshalParams (which decodes msg.Params directly) yielded
// all-zero — an empty dialog with request_id=0. This test fails against that bug
// and passes with the flat decode.
func TestHandlePermissionRequest_DecodesFlatParamsAndEnvelopeID(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, "") // empty configDir → shell-policy path skipped

	id := int64(4242)
	msg := &api.RPCResponse{
		ID: &id,
		Params: mustJSON(t, map[string]any{
			"sessionId": "sess_x",
			"toolCall": map[string]any{
				"toolCallId": "tc-9",
				"title":      "Write config.tf",
				"kind":       "edit",
			},
			"options": []map[string]any{
				{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
				{"optionId": "deny", "name": "Deny", "kind": "reject_once"},
			},
		}),
	}
	tr.HandlePermissionRequest(context.Background(), "c1", msg)

	got, ok := findPermissionNeeded(t, events)
	if !ok {
		t.Fatal("no permission_needed event broadcast")
	}
	if got.RequestID != id {
		t.Errorf("RequestID = %d, want %d (must come from the envelope, not params)", got.RequestID, id)
	}
	if got.ToolCallID != "tc-9" {
		t.Errorf("ToolCallID = %q, want tc-9", got.ToolCallID)
	}
	if got.Title != "Write config.tf" {
		t.Errorf("Title = %q, want 'Write config.tf'", got.Title)
	}
	if len(got.Options) != 2 || got.Options[0].OptionID != "allow" || got.Options[1].OptionID != "deny" {
		t.Errorf("Options = %+v, want 2 options [allow, deny]", got.Options)
	}
}

// TestHandlePermissionRequest_MissingIDDropped pins that a request with no
// envelope id is dropped (its outcome could never be routed back to the agent)
// rather than surfaced as an unanswerable dialog.
func TestHandlePermissionRequest_MissingIDDropped(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, "")

	msg := &api.RPCResponse{ // no ID
		Params: mustJSON(t, map[string]any{
			"sessionId": "s",
			"toolCall":  map[string]any{"toolCallId": "tc", "title": "x", "kind": "edit"},
		}),
	}
	tr.HandlePermissionRequest(context.Background(), "c1", msg)

	if _, ok := findPermissionNeeded(t, events); ok {
		t.Fatal("permission_needed broadcast for a request with no id (should be dropped)")
	}
}
