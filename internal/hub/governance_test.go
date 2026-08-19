package hub

// Tests for governance.go: the cache set/get + warm-signal, the GET
// /api/governance snapshot serving the cache, and the utility-bridge capture
// path (cacheGovernanceFromUtility) that decodes + caches the copy the utility
// bridge receives.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func sampleGovernance() vibekit.GovernanceStatePayload {
	return vibekit.GovernanceStatePayload{
		Known:        true,
		IsEnterprise: false,
		Features: vibekit.GovernanceFeatures{
			MCPEnabled:        true,
			WebToolsEnabled:   true,
			ContentCollection: true,
			AutonomousAgents:  true,
		},
	}
}

func TestGovernanceCache_SetGetWarm(t *testing.T) {
	c := newGovernanceCache()
	if _, ok := c.get(); ok {
		t.Fatal("cold cache should report !ok")
	}
	select {
	case <-c.warm:
		t.Fatal("warm channel should be open on a cold cache")
	default:
	}

	c.set(sampleGovernance())
	got, ok := c.get()
	if !ok || !got.Known || !got.Features.MCPEnabled {
		t.Fatalf("after set: got=%+v ok=%v", got, ok)
	}
	select {
	case <-c.warm:
	default:
		t.Fatal("warm channel should be closed after the first set")
	}

	// A second set must not panic (close-once) and must overwrite.
	c.set(vibekit.GovernanceStatePayload{Known: true})
	if got, _ := c.get(); got.Features.MCPEnabled {
		t.Error("second set did not overwrite the cached state")
	}
}

func TestHandleGovernance_ServesWarmCache(t *testing.T) {
	h, _, _ := newTestHub()
	// Pre-seed the cache so the handler serves it directly (no bridge warm-up).
	h.SetGovernance(sampleGovernance())

	rec := httptest.NewRecorder()
	h.handleGovernance(rec, httptest.NewRequest(http.MethodGet, "/api/governance", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var got vibekit.GovernanceStatePayload
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Known {
		t.Error("served snapshot Known = false, want true")
	}
	if !got.Features.MCPEnabled || !got.Features.WebToolsEnabled {
		t.Errorf("served features = %+v", got.Features)
	}
}

func TestCacheGovernanceFromUtility(t *testing.T) {
	h, _, _ := newTestHub()
	raw := json.RawMessage(`{"sessionId":"s","isEnterprise":false,"disabledReason":"org policy",` +
		`"features":{"mcpEnabled":false,"webToolsEnabled":true,"usageAnalytics":false,` +
		`"contentCollection":true,"promptLogging":false,"codeReferenceTracker":false,` +
		`"autonomousAgents":true}}`)

	h.cacheGovernanceFromUtility(raw)

	got, ok := h.governance.get()
	if !ok || !got.Known {
		t.Fatalf("cache not populated from utility copy: %+v ok=%v", got, ok)
	}
	if got.Features.MCPEnabled {
		t.Error("mcp_enabled should be false (disabled by org)")
	}
	if got.DisabledReason != "org policy" {
		t.Errorf("disabled_reason = %q, want 'org policy'", got.DisabledReason)
	}

	// An invalid copy must be ignored (no panic, cache unchanged).
	h.cacheGovernanceFromUtility(json.RawMessage("{"))
	if got2, _ := h.governance.get(); got2.DisabledReason != "org policy" {
		t.Error("invalid utility copy clobbered the cache")
	}
}
