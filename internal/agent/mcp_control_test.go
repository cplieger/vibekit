package agent

// Tests for mcp_control.go: the live MCP control operations (reconnect
// fan-out, getPrompt/getResource one-bridge reads) and their HTTP handlers,
// plus that recordConnected surfaces discovery (prompts/resources) in the
// /api/mcp/status snapshot.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// insertLiveBridge inserts a live bridge for chatID and returns its fake.
func insertLiveBridge(t *testing.T, h *Runtime, chatID vibekit.ChatID) *fakeBridge {
	t.Helper()
	sb, _ := h.bridge.mgr.orInsert(chatID)
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

// enabledConfig stages names as the user's own, enabled servers: present in all
// three sets, which is the nesting a real store produces for an enabled entry.
func enabledConfig(names ...string) *fakeMCPConfig {
	set := nameSet(names...)
	return &fakeMCPConfig{enabled: set, configured: nameSet(names...), all: nameSet(names...)}
}

func nameSet(names ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	return set
}

func TestReconnectMCPServer_FansOutToAllLiveBridges(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	b1 := insertLiveBridge(t, h, "c1")
	b2 := insertLiveBridge(t, h, "c2")

	n := h.mcpRegistry.reconnectServer(t.Context(), "everything")
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
	if n := h.mcpRegistry.reconnectServer(t.Context(), "everything"); n != 0 {
		t.Fatalf("targeted = %d, want 0", n)
	}
}

func TestGetMCPPrompt_CallsBridgeAndReturnsResult(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	b := insertLiveBridge(t, h, "c1")

	res, err := h.mcpRegistry.promptFor(t.Context(), "everything", "simple-prompt", nil)
	if err != nil {
		t.Fatalf("promptFor: %v", err)
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

	if _, err := h.mcpRegistry.resourceFor(t.Context(), "everything", "demo://x"); err != nil {
		t.Fatalf("resourceFor: %v", err)
	}
	if !bridgeCalled(b, methodV3MCPGetResource) {
		t.Errorf("bridge did not receive %s", methodV3MCPGetResource)
	}
}

func TestMCPFetch_NoLiveBridgeErrors(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	_, err := h.mcpRegistry.promptFor(t.Context(), "everything", "p", nil)
	if !errors.Is(err, errNoLiveBridge) {
		t.Fatalf("err = %v, want errNoLiveBridge", err)
	}
}

func TestHandleMCPReconnect_MethodNotAllowed(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	req := httptest.NewRequest(http.MethodGet, "/api/mcp/reconnect", nil)
	rec := httptest.NewRecorder()
	h.mcpRegistry.handleReconnect(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, want 405", rec.Code)
	}
}

func TestHandleMCPReconnect_UnknownServer(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	rec := postJSON(h.mcpRegistry.handleReconnect, "/api/mcp/reconnect", `{"server":"nope"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestHandleMCPReconnect_OK(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	insertLiveBridge(t, h, "c1")
	rec := postJSON(h.mcpRegistry.handleReconnect, "/api/mcp/reconnect", `{"server":"everything"}`)
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
	rec := postJSON(h.mcpRegistry.handlePrompt, "/api/mcp/prompt", `{"server":"everything"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestHandleMCPGetPrompt_NoLiveBridgeConflict(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	rec := postJSON(h.mcpRegistry.handlePrompt, "/api/mcp/prompt", `{"server":"everything","prompt":"simple-prompt"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

func TestHandleMCPGetResource_OK(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("everything"))
	insertLiveBridge(t, h, "c1")
	rec := postJSON(h.mcpRegistry.handleResource, "/api/mcp/resource", `{"server":"everything","uri":"demo://x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

// TestMCPRegistry_RecordConnectedStoresDiscovery verifies prompts/resources
// captured at connect time surface in the /api/mcp/status snapshot.
func TestMCPRegistry_RecordConnectedStoresDiscovery(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	prompts := []vibekit.MCPPromptInfo{{Name: "Simple Prompt", PromptName: "simple-prompt", Description: "no args"}}
	resources := []vibekit.MCPResourceInfo{{Name: "doc", URI: "demo://doc", MimeType: "text/markdown"}}
	h.mcpRegistry.RecordConnected(t.Context(), "everything", nil, prompts, resources)

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

// TestReconnectMCPServer_ReportsABridgeThatRefusedTheReset is the failure half of
// the fan-out, in both directions.
//
// A per-bridge failure is deliberately not fatal — one wedged chat must not stop the
// others reconnecting — and the count returned is bridges TARGETED, so the HTTP
// reply says "reconnected: 1" whether the reset landed or not. The log line is the
// only place a refusal is recorded, and a guard flipped here emits one on every
// successful reconnect instead, which buries the real ones.
func TestReconnectMCPServer_ReportsABridgeThatRefusedTheReset(t *testing.T) {
	const wantLine = "mcp reconnect: bridge call failed"

	t.Run("a bridge that refused is reported", func(t *testing.T) {
		logs := captureLogs(t)
		h := newHubWithMCPConfig(enabledConfig("everything"))
		b := insertLiveBridge(t, h, "c1")
		b.callErrs = map[string]error{methodV3MCPResetServer: errors.New("bridge wedged")}

		if n := h.mcpRegistry.reconnectServer(t.Context(), "everything"); n != 1 {
			t.Fatalf("targeted = %d, want 1", n)
		}
		out := logs.String()
		if !strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a bridge that refused the reset said nothing, while the reply counts it as "+
				"reconnected; want a line reading %q. Got: %s", wantLine, out)
		}
		if !strings.Contains(out, `"server":"everything"`) {
			t.Errorf("the failure line does not name the server it is about: %s", out)
		}
	})

	t.Run("an ordinary reconnect is quiet about it", func(t *testing.T) {
		logs := captureLogs(t)
		h := newHubWithMCPConfig(enabledConfig("everything"))
		insertLiveBridge(t, h, "c1")

		if n := h.mcpRegistry.reconnectServer(t.Context(), "everything"); n != 1 {
			t.Fatalf("targeted = %d, want 1", n)
		}
		if out := logs.String(); strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a reconnect every bridge accepted was reported as failed: %s", out)
		}
	})
}

// TestGetMCPPrompt_SendsAnArgumentsObjectEitherWay pins the argument shape
// TestGetMCPPrompt_CallsBridgeAndReturnsResult leaves unasserted.
//
// An MCP server's prompt schema is generated from its argument list, and a server
// with no arguments still declares an object — so `"arguments": null` fails
// validation server-side where `{}` passes. Substituting the empty object for
// arguments the caller DID supply is the same bug inverted: the prompt renders with
// every placeholder unfilled and nothing reports why.
func TestGetMCPPrompt_SendsAnArgumentsObjectEitherWay(t *testing.T) {
	cases := []struct {
		args map[string]any
		name string
		want string
	}{
		{
			name: "no arguments become an empty object, never null",
			args: nil,
			want: `{}`,
		},
		{
			name: "the caller's arguments travel unchanged",
			args: map[string]any{"repo": "vibekit"},
			want: `{"repo":"vibekit"}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHubWithMCPConfig(enabledConfig("everything"))
			b := insertLiveBridge(t, h, "c1")

			if _, err := h.mcpRegistry.promptFor(t.Context(), "everything", "p", c.args); err != nil {
				t.Fatalf("promptFor: %v", err)
			}
			params := b.paramsFor(methodV3MCPGetPrompt)
			if params == nil {
				t.Fatal("no getPrompt call was issued, so there are no arguments to inspect")
			}
			sent, err := json.Marshal(params["arguments"])
			if err != nil {
				t.Fatalf("marshal the arguments the call carried: %v", err)
			}
			if string(sent) != c.want {
				t.Errorf("promptFor(args=%v) sent arguments %s, want %s", c.args, sent, c.want)
			}
		})
	}
}

// TestWriteMCPResult_AlwaysWritesADecodableObject pins the fallback's purpose and
// its limit.
//
// The result is relayed VERBATIM because vibekit models no MCP payload shapes, which
// leaves one gap the client cannot handle: a server that answered with nothing
// produces an empty body, and the client's decode fails on a reply that is not an
// error either. The fallback covers exactly that case, and must not reach a result
// that does exist — a replaced payload is a prompt or resource silently emptied.
func TestWriteMCPResult_AlwaysWritesADecodableObject(t *testing.T) {
	cases := []struct {
		name string
		res  json.RawMessage
		want string
	}{
		{name: "an absent result becomes an empty object", res: nil, want: `{}`},
		{name: "an empty result becomes an empty object", res: json.RawMessage(``), want: `{}`},
		{
			name: "a real result is relayed verbatim",
			res:  json.RawMessage(`{"messages":[{"role":"user"}]}`),
			want: `{"messages":[{"role":"user"}]}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeMCPResult(rec, c.res)
			if got := strings.TrimSpace(rec.Body.String()); got != c.want {
				t.Errorf("writeMCPResult(%q) wrote %s, want %s", c.res, got, c.want)
			}
		})
	}
}
