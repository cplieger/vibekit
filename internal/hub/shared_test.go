package hub

// Utility helpers for hub tests: newTestHub constructor, postCmd helper,
// event inspection helpers, and message builders.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// --- Hub construction helpers ---

func newTestHub() (*Hub, *fakeChatStore, *fakeBridge) {
	cs := newFakeChatStore()
	br := newFakeBridge()
	factory := func() api.ACPBridge { return br }
	h := New("/tmp/work", factory, cs, func() []string { return nil })
	cs.SetBroadcaster(h)
	// Signal MCP readiness immediately so tests don't wait 30 seconds.
	h.mcpRegistry.signalReady()
	return h, cs, br
}

// handleCommand is a test helper that delegates to the dispatcher.
func (h *Hub) handleCommand(w http.ResponseWriter, r *http.Request) {
	h.dispatcher.ServeHTTP(w, r)
}

// postCmd POSTs a typed ClientCommand to handleCommand and returns the recorder.
func postCmd(t *testing.T, h *Hub, cmd api.ClientCommand) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(cmd)
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	h.handleCommand(rec, req)
	return rec
}

// --- Event inspection helpers ---

// extractTypes decodes each event in a replay-buffer slice and returns
// their types in order. Used to assert an emit sequence.
func extractTypes(t *testing.T, events []sseEvent) []string {
	t.Helper()
	out := make([]string, 0, len(events))
	for _, e := range events {
		var msg api.ServerEvent
		if err := json.Unmarshal(e.data, &msg); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		out = append(out, string(msg.Type))
	}
	return out
}

// wantSubset fails the test if any of `want` is missing from `got`.
// Order is not checked; this is for "did these events fire?" assertions.
func wantSubset(t *testing.T, got []string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("missing event %q in %v", w, got)
		}
	}
}

// mustJSON marshals v or fails the test.
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// newChunkMsg builds a session/update agent_message_chunk notification
// with the given text payload.
func newChunkMsg(t *testing.T, text string) *api.RPCResponse {
	t.Helper()
	raw := mustJSON(t, map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": text},
	})
	return &api.RPCResponse{
		Method: "session/update",
		Params: mustJSON(t, map[string]any{"update": raw}),
	}
}

// newToolCallMsg builds a session/update tool_call notification with the
// given ids / title / status.
func newToolCallMsg(t *testing.T, id, title, status string) *api.RPCResponse {
	t.Helper()
	raw := mustJSON(t, map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    id,
		"title":         title,
		"kind":          "read",
		"status":        status,
	})
	return &api.RPCResponse{
		Method: "session/update",
		Params: mustJSON(t, map[string]any{"update": raw}),
	}
}
