package translate

import (
	"context"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

func countEvents(events *[]api.ServerEvent, typ api.EventType) int {
	n := 0
	for _, e := range *events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// TestHandlePolicyChanged pins that a _kiro/policy/changed notification
// broadcasts one global (empty chatID) permissions_changed event carrying the
// status — the signal the client refetches GET /api/permissions on.
func TestHandlePolicyChanged(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, "/tmp")

	tr.HandlePolicyChanged(context.Background(), api.ChatID("c1"), &api.RPCResponse{
		Params: mustJSON(t, map[string]any{"sessionId": "sess-1", "status": "success"}),
	})

	if countEvents(events, api.EventPermissionsChanged) != 1 {
		t.Fatalf("permissions_changed count = %d, want 1", countEvents(events, api.EventPermissionsChanged))
	}
	for _, e := range *events {
		if e.Type != api.EventPermissionsChanged {
			continue
		}
		if e.ChatID != "" {
			t.Errorf("event ChatID = %q, want empty (policy is global)", e.ChatID)
		}
		p, ok := e.Payload.(api.PermissionsChangedPayload)
		if !ok || p.Status != "success" {
			t.Errorf("payload = %+v (ok=%v)", e.Payload, ok)
		}
	}
}

// TestHandlePolicyError pins that a _kiro/policy/error notification broadcasts
// one policy_error event carrying the error list.
func TestHandlePolicyError(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, "/tmp")

	tr.HandlePolicyError(context.Background(), api.ChatID("c1"), &api.RPCResponse{
		Params: mustJSON(t, map[string]any{
			"sessionId": "sess-1",
			"errors": []map[string]any{
				{"scope": "user", "source": "permissions.yaml", "message": "bad rule", "fatal": true},
			},
		}),
	})

	if countEvents(events, api.EventPolicyError) != 1 {
		t.Fatalf("policy_error count = %d, want 1", countEvents(events, api.EventPolicyError))
	}
	for _, e := range *events {
		if e.Type != api.EventPolicyError {
			continue
		}
		p, ok := e.Payload.(api.PolicyErrorPayload)
		if !ok || len(p.Errors) != 1 || p.Errors[0].Message != "bad rule" || !p.Errors[0].Fatal {
			t.Errorf("payload = %+v (ok=%v)", e.Payload, ok)
		}
	}
}

// TestHandlePolicyMalformedNoop pins that malformed params are dropped
// without a broadcast.
func TestHandlePolicyMalformedNoop(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, "/tmp")
	tr.HandlePolicyChanged(context.Background(), api.ChatID("c1"), &api.RPCResponse{Params: []byte("{")})
	tr.HandlePolicyError(context.Background(), api.ChatID("c1"), &api.RPCResponse{Params: []byte("{")})
	if len(*events) != 0 {
		t.Errorf("malformed params produced %d events, want 0", len(*events))
	}
}
