package hub

// Tests for mcp_control.go: the live MCP control operations (reconnect
// fan-out, getPrompt/getResource one-bridge reads) and their HTTP handlers,
// plus that recordConnected surfaces discovery (prompts/resources) in the
// /api/mcp/status snapshot.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// insertLiveBridge inserts a live bridge for chatID and returns its fake.
func insertLiveBridge(t *testing.T, h *Hub, chatID api.ChatID) *fakeBridge {
	t.Helper()
	sb, _ := h.bridge.mgr.getOrInsert(chatID)
	fb, ok := sb.bridge.(*fakeBridge)
	if !ok {
		t.Fatalf("bridge is %T, want *fakeBridge", sb.bridge)
	}
	return fb
}

// bridgeCalled reports whether the fake bridge received a call to method.
func bridgeCalled(b *fakeBridge, method string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Contains(b.calls, method)
}

func enabledConfig(names ...string) *fakeMCPConfig {
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	return &fakeMCPConfig{enabled: set}
}

func TestReconnectMCPServer_FansOutToAllLiveBridges(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	b1 := insertLiveBridge(t, h, "c1")
	b2 := insertLiveBridge(t, h, "c2")

	n := h.reconnectMCPServer(context.Background(), "everything")
	if n != 2 {
		t.Fatalf("targeted = %d, want 2", n)
	}
	for i, b := range []*fakeBridge{b1, b2} {
		if !bridgeCalled(b, methodV3MCPResetServer) {
			t.Errorf("bridge %d did not receive %s", i, methodV3MCPResetServer)
		}
	}
}

func TestReconnectMCPServer_NoBridgesIsNoOp(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	if n := h.reconnectMCPServer(context.Background(), "everything"); n != 0 {
		t.Fatalf("targeted = %d, want 0", n)
	}
}

func TestGetMCPPrompt_CallsBridgeAndReturnsResult(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	b := insertLiveBridge(t, h, "c1")

	res, err := h.getMCPPrompt(context.Background(), "everything", "simple-prompt", nil)
	if err != nil {
		t.Fatalf("getMCPPrompt: %v", err)
	}
	if !bridgeCalled(b, methodV3MCPGetPrompt) {
		t.Errorf("bridge did not receive %s", methodV3MCPGetPrompt)
	}
	// The fake echoes a canned result; we only assert it round-trips.
	if len(res) == 0 {
		t.Error("empty result")
	}
}

func TestGetMCPResource_CallsBridge(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	b := insertLiveBridge(t, h, "c1")

	if _, err := h.getMCPResource(context.Background(), "everything", "demo://x"); err != nil {
		t.Fatalf("getMCPResource: %v", err)
	}
	if !bridgeCalled(b, methodV3MCPGetResource) {
		t.Errorf("bridge did not receive %s", methodV3MCPGetResource)
	}
}

func TestMCPFetch_NoLiveBridgeErrors(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	_, err := h.getMCPPrompt(context.Background(), "everything", "p", nil)
	if !errors.Is(err, errNoLiveBridge) {
		t.Fatalf("err = %v, want errNoLiveBridge", err)
	}
}

func TestHandleMCPReconnect_MethodNotAllowed(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	req := httptest.NewRequest(http.MethodGet, "/api/mcp/reconnect", nil)
	rec := httptest.NewRecorder()
	h.handleMCPReconnect(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, want 405", rec.Code)
	}
}

func TestHandleMCPReconnect_UnknownServer(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	rec := postJSON(h.handleMCPReconnect, "/api/mcp/reconnect", `{"server":"nope"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestHandleMCPReconnect_OK(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	insertLiveBridge(t, h, "c1")
	rec := postJSON(h.handleMCPReconnect, "/api/mcp/reconnect", `{"server":"everything"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Reconnected int `json:"reconnected"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Reconnected != 1 {
		t.Errorf("reconnected = %d, want 1", body.Reconnected)
	}
}

func TestHandleMCPGetPrompt_MissingPrompt(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	rec := postJSON(h.handleMCPGetPrompt, "/api/mcp/prompt", `{"server":"everything"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestHandleMCPGetPrompt_NoLiveBridgeConflict(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	rec := postJSON(h.handleMCPGetPrompt, "/api/mcp/prompt", `{"server":"everything","prompt":"simple-prompt"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

func TestHandleMCPGetResource_OK(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	insertLiveBridge(t, h, "c1")
	rec := postJSON(h.handleMCPGetResource, "/api/mcp/resource", `{"server":"everything","uri":"demo://x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

// TestMCPRegistry_RecordConnectedStoresDiscovery verifies prompts/resources
// captured at connect time surface in the /api/mcp/status snapshot.
func TestMCPRegistry_RecordConnectedStoresDiscovery(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	prompts := []api.MCPPromptInfo{{Name: "Simple Prompt", PromptName: "simple-prompt", Description: "no args"}}
	resources := []api.MCPResourceInfo{{Name: "doc", URI: "demo://doc", MimeType: "text/markdown"}}
	h.mcpRegistry.recordConnected(context.Background(), "everything", prompts, resources)

	snap := h.mcpRegistry.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if len(snap[0].Prompts) != 1 || snap[0].Prompts[0].PromptName != "simple-prompt" {
		t.Errorf("prompts = %+v", snap[0].Prompts)
	}
	if len(snap[0].Resources) != 1 || snap[0].Resources[0].URI != "demo://doc" {
		t.Errorf("resources = %+v", snap[0].Resources)
	}
}

// postJSON is a tiny helper: POST a JSON body to an http.HandlerFunc.
func postJSON(handler http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}
