package translate

import (
	"maps"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// fullFeatures is the 7-flag governance feature set as it appears on the wire
// (verified live on a Builder-ID login).
func fullFeatures() map[string]any {
	return map[string]any{
		"mcpEnabled":           true,
		"webToolsEnabled":      true,
		"usageAnalytics":       false,
		"contentCollection":    true,
		"promptLogging":        false,
		"codeReferenceTracker": false,
		"autonomousAgents":     true,
	}
}

func govMsg(t *testing.T, sessionID string, extra map[string]any) *vibekit.RPCResponse {
	t.Helper()
	params := map[string]any{"sessionId": sessionID, "features": fullFeatures()}
	maps.Copy(params, extra)
	return &vibekit.RPCResponse{Params: mustJSON(t, params)}
}

// HandleGovernanceState parses the notification, broadcasts a global (empty
// chatID) governance_state event with the resolved flags, and caches it
// hub-side via SetGovernance.
func TestHandleGovernanceState_BroadcastsAndCaches(t *testing.T) {
	deps, events := newEventCaptureDeps()
	var cached *vibekit.GovernanceStatePayload
	deps.onSetGovernance = func(g vibekit.GovernanceStatePayload) { cp := g; cached = &cp }
	tr := New(rolesOf(deps))

	tr.HandleGovernanceState(t.Context(), vibekit.ChatID("c1"),
		govMsg(t, "sess-parent", map[string]any{"isEnterprise": false}))

	if len(*events) != 1 {
		t.Fatalf("expected 1 event, got %d: %v", len(*events), eventTypes(*events))
	}
	evt := (*events)[0]
	if evt.Type != vibekit.EventGovernanceState {
		t.Fatalf("type = %q, want governance_state", evt.Type)
	}
	// Account-global: broadcast carries no chat id so a Settings-only client
	// (subscribed to no chat) still receives it.
	if evt.ChatID != "" {
		t.Errorf("chat_id = %q, want empty (account-global)", evt.ChatID)
	}
	p, ok := evt.Payload.(vibekit.GovernanceStatePayload)
	if !ok {
		t.Fatalf("payload type = %T, want GovernanceStatePayload", evt.Payload)
	}
	if !p.Known {
		t.Error("Known = false, want true (real notification)")
	}
	if !p.Features.MCPEnabled || !p.Features.WebToolsEnabled || !p.Features.AutonomousAgents {
		t.Errorf("capability flags not carried through: %+v", p.Features)
	}
	if p.Features.CodeReferenceTracker || p.Features.PromptLogging || p.Features.UsageAnalytics {
		t.Errorf("off flags leaked as on: %+v", p.Features)
	}
	if p.IsEnterprise {
		t.Error("IsEnterprise = true, want false")
	}
	if cached == nil {
		t.Fatal("SetGovernance was not called (state not cached)")
	}
	if !cached.Known || !cached.Features.MCPEnabled {
		t.Errorf("cached payload = %+v, want Known + MCPEnabled", cached)
	}
}

// A subagent-session copy (sessionId differs from the chat's parent) is skipped
// so the identical account-global flags aren't re-broadcast/cached redundantly.
func TestHandleGovernanceState_SubagentSkipped(t *testing.T) {
	deps, events := newEventCaptureDeps()
	cached := false
	deps.onSetGovernance = func(vibekit.GovernanceStatePayload) { cached = true }
	deps.parent = "sess-parent"
	tr := New(rolesOf(deps))

	tr.HandleGovernanceState(t.Context(), vibekit.ChatID("c1"),
		govMsg(t, "sess-subagent", nil))

	if len(*events) != 0 || cached {
		t.Errorf("subagent copy should be skipped: events=%d cached=%v", len(*events), cached)
	}
}

// Malformed params are dropped without a broadcast or cache write.
func TestHandleGovernanceState_Malformed(t *testing.T) {
	deps, events := newEventCaptureDeps()
	cached := false
	deps.onSetGovernance = func(vibekit.GovernanceStatePayload) { cached = true }
	tr := New(rolesOf(deps))

	tr.HandleGovernanceState(t.Context(), "c1", &vibekit.RPCResponse{Params: []byte("{")})

	if len(*events) != 0 || cached {
		t.Errorf("malformed governance should be dropped: events=%d cached=%v", len(*events), cached)
	}
}

// DecodeGovernanceState (the exported decoder the hub reuses for the utility
// bridge copy) maps the wire shape to the domain payload and rejects
// empty/invalid input.
func TestDecodeGovernanceState(t *testing.T) {
	raw := mustJSON(t, map[string]any{
		"sessionId":      "s",
		"isEnterprise":   true,
		"disabledReason": "org policy",
		"features":       fullFeatures(),
	})
	g, ok := DecodeGovernanceState(raw)
	if !ok {
		t.Fatal("DecodeGovernanceState returned !ok for valid input")
	}
	if !g.Known || !g.IsEnterprise || g.DisabledReason != "org policy" {
		t.Errorf("decoded = %+v", g)
	}
	if !g.Features.MCPEnabled || g.Features.PromptLogging {
		t.Errorf("features = %+v", g.Features)
	}

	if _, ok := DecodeGovernanceState(nil); ok {
		t.Error("empty params should decode to !ok")
	}
	if _, ok := DecodeGovernanceState([]byte("{")); ok {
		t.Error("malformed params should decode to !ok")
	}
}
