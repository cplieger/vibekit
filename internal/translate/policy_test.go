package translate

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func countEvents(events *[]vibekit.ServerEvent, typ vibekit.EventType) int {
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
	tr := New(deps)

	tr.HandlePolicyChanged(t.Context(), vibekit.ChatID("c1"), &vibekit.RPCResponse{
		Params: mustJSON(t, map[string]any{"sessionId": "sess-1", "status": "success"}),
	})

	if countEvents(events, vibekit.EventPermissionsChanged) != 1 {
		t.Fatalf("permissions_changed count = %d, want 1", countEvents(events, vibekit.EventPermissionsChanged))
	}
	for _, e := range *events {
		if e.Type != vibekit.EventPermissionsChanged {
			continue
		}
		if e.ChatID != "" {
			t.Errorf("event ChatID = %q, want empty (policy is global)", e.ChatID)
		}
		p, ok := e.Payload.(vibekit.PermissionsChangedPayload)
		if !ok || p.Status != "success" {
			t.Errorf("payload = %+v (ok=%v)", e.Payload, ok)
		}
	}
}

// TestHandlePolicyError pins that a _kiro/policy/error notification broadcasts
// one policy_error event carrying the error list.
//
// This is the authority path the rule writer depends on. vibekit no longer
// validates the capability VOCABULARY on write (see policyfile.SanitizeRule and
// server's TestPolicyRuleUnrecognisedCapabilityRoundTrips): a rule naming a
// capability KAS does not have is written, KAS rejects it on load, and the
// rejection reaches the user as this event rendered as a banner. If this handler
// stops broadcasting, a bad rule becomes silent.
func TestHandlePolicyError(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps)

	tr.HandlePolicyError(t.Context(), vibekit.ChatID("c1"), &vibekit.RPCResponse{
		Params: mustJSON(t, map[string]any{
			"sessionId": "sess-1",
			"errors": []map[string]any{
				{"scope": "user", "source": "permissions.yaml", "message": "bad rule", "fatal": true},
			},
		}),
	})

	if countEvents(events, vibekit.EventPolicyError) != 1 {
		t.Fatalf("policy_error count = %d, want 1", countEvents(events, vibekit.EventPolicyError))
	}
	for _, e := range *events {
		if e.Type != vibekit.EventPolicyError {
			continue
		}
		p, ok := e.Payload.(vibekit.PolicyErrorPayload)
		if !ok || len(p.Errors) != 1 || p.Errors[0].Message != "bad rule" || !p.Errors[0].Fatal {
			t.Errorf("payload = %+v (ok=%v)", e.Payload, ok)
		}
	}
}

// TestHandlePolicyMalformedNoop pins that malformed params are dropped
// without a broadcast.
func TestHandlePolicyMalformedNoop(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps)
	tr.HandlePolicyChanged(t.Context(), vibekit.ChatID("c1"), &vibekit.RPCResponse{Params: []byte("{")})
	tr.HandlePolicyError(t.Context(), vibekit.ChatID("c1"), &vibekit.RPCResponse{Params: []byte("{")})
	if len(*events) != 0 {
		t.Errorf("malformed params produced %d events, want 0", len(*events))
	}
}
