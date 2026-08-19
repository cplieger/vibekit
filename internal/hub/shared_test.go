package hub

// Utility helpers for hub tests: newTestHub constructor, postCmd helper,
// event inspection helpers, and message builders.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/sse"
)

// --- Hub construction helpers ---

// newTestHub builds a Hub on context.Background(), which is what New itself used
// to root its lifetime at before that context became a required parameter — so
// every one of the ~200 callers keeps exactly the lifetime it had, and a test
// that wants the hub torn down calls Shutdown as it always did.
func newTestHub() (*Hub, *fakeChatStore, *fakeBridge) {
	cs := newFakeChatStore()
	br := newFakeBridge()
	factory := func() ACPBridge { return br }
	h := New(context.Background(), "/tmp/work", factory, cs)
	cs.Bus = h
	// Signal MCP readiness immediately so tests don't wait 30 seconds.
	h.mcpRegistry.signalReady()
	return h, cs, br
}

// handleCommand is a test helper that delegates to the dispatcher.
func (h *Hub) handleCommand(w http.ResponseWriter, r *http.Request) {
	h.dispatcher.ServeHTTP(w, r)
}

// postCmd POSTs a typed ClientCommand to handleCommand and returns the recorder.
func postCmd(t *testing.T, h *Hub, cmd vibekit.ClientCommand) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(cmd)
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	h.handleCommand(rec, req)
	return rec
}

// --- Event inspection helpers ---

// bufferedSince returns the hub's buffered events with ID greater than
// sinceID — the test-side filter over sse.(*Hub).Buffered (the library's
// inspection surface is a parameterless snapshot).
func bufferedSince(h *Hub, sinceID uint64) []sse.ReplayEvent {
	var out []sse.ReplayEvent
	for _, e := range h.sse.hub.Buffered() {
		if e.ID > sinceID {
			out = append(out, e)
		}
	}
	return out
}

// extractTypes decodes each event in a replay-buffer slice and returns
// their types in order. Used to assert an emit sequence.
func extractTypes(t *testing.T, events []sse.ReplayEvent) []string {
	t.Helper()
	out := make([]string, 0, len(events))
	for _, e := range events {
		var msg vibekit.ServerEvent
		if err := json.Unmarshal(e.Event.Data, &msg); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		out = append(out, string(msg.Type))
	}
	return out
}

// missingEvents returns the members of `want` absent from `got`, in the order
// they were asked for. Order within `got` is not checked; this backs "did
// these events fire?" assertions, which stay at the call site so a failure
// names the case that produced the events rather than a shared helper.
func missingEvents(got []string, want ...string) []string {
	var missing []string
	for _, w := range want {
		if !slices.Contains(got, w) {
			missing = append(missing, w)
		}
	}
	return missing
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
func newChunkMsg(t *testing.T, text string) *vibekit.RPCResponse {
	t.Helper()
	raw := mustJSON(t, map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": text},
	})
	return &vibekit.RPCResponse{
		Method: "session/update",
		Params: mustJSON(t, map[string]any{"update": raw}),
	}
}

// newToolCallMsg builds a session/update tool_call notification with the
// given ids / title / status.
func newToolCallMsg(t *testing.T, id, title, status string) *vibekit.RPCResponse {
	t.Helper()
	raw := mustJSON(t, map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    id,
		"title":         title,
		"kind":          "read",
		"status":        status,
	})
	return &vibekit.RPCResponse{
		Method: "session/update",
		Params: mustJSON(t, map[string]any{"update": raw}),
	}
}

// --- Log capture ---

// logCapture is a mutex-guarded buffer that backs a slog handler so a
// test can assert on (or assert the absence of) log output even when
// logs are written from background goroutines.
type logCapture struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (b *logCapture) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns the captured output so far.
func (b *logCapture) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs swaps the default slog logger for one that writes JSON
// into the returned buffer and restores the previous logger at test
// end. Tests using it must NOT call t.Parallel — it mutates the global
// slog default.
func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	out := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return out
}
