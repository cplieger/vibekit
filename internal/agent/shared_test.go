package agent

// Utility helpers for agent tests: newTestHub constructor, postCmd helper,
// event inspection helpers, and message builders.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/v2/sse"
)

// --- Runtime construction helpers ---

// newTestHub roots the Runtime's lifetime at context.Background(), so a test that wants
// it torn down calls Shutdown.
func newTestHub() (*Runtime, *fakeChatStore, *fakeBridge) {
	return newTestHubIn("/tmp/work")
}

// newTestHubIn builds a runtime rooted at workDir. Use it rather than reassigning
// h.lifecycle.workDir afterwards: the workspace paths are read once at wiring time, so a
// post-construction mutation configures something the wiring has already read.
func newTestHubIn(workDir string) (*Runtime, *fakeChatStore, *fakeBridge) {
	cs := newFakeChatStore()
	br := newFakeBridge()
	factory := func() ACPBridge { return br }
	h := New(context.Background(), workDir, factory, cs)
	cs.Bus = h
	// Signal MCP readiness immediately so tests don't wait 30 seconds.
	h.mcpRegistry.SignalReady()
	return h, cs, br
}

// shutdownHub roots its budget at context.Background() rather than t.Context() because
// callers reach for it from t.Cleanup, where t.Context() is already cancelled. 30s sits
// above anything a unit test needs and below go test's own timeout, so an expiry is a
// diagnostic rather than a flake.
func shutdownHub(t *testing.T, h *Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Errorf("runtime shutdown: %v", err)
	}
}

func (rt *Runtime) handleCommand(w http.ResponseWriter, r *http.Request) {
	rt.dispatcher.ServeHTTP(w, r)
}

func postCmd(t *testing.T, h *Runtime, cmd vibekit.ClientCommand) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(cmd)
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	h.handleCommand(rec, req)
	return rec
}

// --- Event inspection helpers ---

// bufferedSince is the test-side ID filter over sse.Buffered, whose own inspection
// surface is a parameterless snapshot.
func bufferedSince(h *Runtime, sinceID uint64) []sse.ReplayEvent {
	var out []sse.ReplayEvent
	for _, e := range h.bus.fanout.Buffered() {
		if e.ID > sinceID {
			out = append(out, e)
		}
	}
	return out
}

// extractTypes returns the events' types in order, for asserting an emit sequence.
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

// missingEvents ignores order within `got`: it backs did-these-fire assertions, which
// stay at the call site so a failure names the case rather than a shared helper.
func missingEvents(got []string, want ...string) []string {
	var missing []string
	for _, w := range want {
		if !slices.Contains(got, w) {
			missing = append(missing, w)
		}
	}
	return missing
}

// mustJSON takes testing.TB rather than *testing.T so a benchmark builds the same wire
// frames a test does; a benchmark's own copy of a frame is how fixtures drift.
func mustJSON(t testing.TB, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// newChunkMsg is the ONE builder for a session/update agent_message_chunk in this
// package, because the `update` nesting is the protocol and hand-rolled copies of it
// are how a consumer came to read the kind off the outer object and drop every chunk
// while its own tests stayed green. It takes no testing.TB so the fake bridge's Call
// can use it: a builder callable from a fake must not end a test from another goroutine.
func newChunkMsg(text string) *vibekit.RPCResponse {
	return newSessionChunkMsg("", text)
}

// newSessionChunkMsg sets the envelope's `sessionId`, which the utility bridge's
// own-session screen reads. An empty id omits the key.
func newSessionChunkMsg(sessionID, text string) *vibekit.RPCResponse {
	update, _ := json.Marshal(map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": text},
	})
	// json.RawMessage, not []byte: a []byte field marshals to a base64 STRING,
	// which decodes as no frame at all.
	env := map[string]any{"update": json.RawMessage(update)}
	if sessionID != "" {
		env["sessionId"] = sessionID
	}
	params, _ := json.Marshal(env)
	return &vibekit.RPCResponse{Method: vibekit.MethodSessionUpdate, Params: params}
}

// newStepChunkMsg is the shape a chat-parented run's step frames arrive in on the
// LAUNCHING chat's connection: the subtask id is empty exactly as KAS sends it, so
// `_meta.kiro.workflow` is the whole attribution.
func newStepChunkMsg(text, workflowID, nodePath string) *vibekit.RPCResponse {
	update, _ := json.Marshal(map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": text},
		"_meta": map[string]any{
			"kiro": map[string]any{
				"workflow": map[string]any{
					"workflowId": workflowID,
					"nodePath":   []string{nodePath},
					"type":       "step",
				},
			},
		},
	})
	params, _ := json.Marshal(map[string]any{"update": json.RawMessage(update)})
	return &vibekit.RPCResponse{Method: vibekit.MethodSessionUpdate, Params: params}
}

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

// logCapture is mutex-guarded because the logs it captures are written from background
// goroutines.
type logCapture struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (b *logCapture) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logCapture) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs mutates the global slog default, so a test using it must NOT call
// t.Parallel. The previous logger is restored at test end.
func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	out := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return out
}

// --- Turn buffer helpers ---

// stageTurnBuffer opens a wireTurnStart turn when none is open, the test-side equivalent
// of the first frame of a turn vibekit did not prompt. There is no buffer store: a
// buffer belongs to the turn record that installed it.
func (rt *Runtime) stageTurnBuffer(tb testing.TB, chatID vibekit.ChatID) *buffer.Buffer {
	tb.Helper()
	return rt.coord.TurnFoldTarget(tb.Context(), chatID, vibekit.TurnSourceWireTurnStart)
}

// stagePromptTurn hands back the epoch as well as the buffer, and the epoch is the point:
// an epoch-scoped closer handed zero closes nothing (zero is what StartTurn answers when
// it refuses), so a test passing zero exercises the fallthrough rather than its closer.
func (rt *Runtime) stagePromptTurn(tb testing.TB, chatID vibekit.ChatID) (vibekit.TurnEpoch, *buffer.Buffer) {
	tb.Helper()
	epoch := rt.coord.StartTurn(tb.Context(), chatID, vibekit.TurnSourcePrompt)
	if epoch == 0 {
		tb.Fatalf("StartTurn(%q) refused, so there is no turn to stage", chatID)
	}
	return epoch, rt.coord.TurnFoldTarget(tb.Context(), chatID, vibekit.TurnSourceWireTurnStart)
}

// liveTurnBuffer is what the NEXT turn would read, which is what a released-buffer
// assertion is about. Nil when no turn is open.
func (rt *Runtime) liveTurnBuffer(chatID vibekit.ChatID) *buffer.Buffer {
	facts, open := rt.coord.turns.openTurns()[chatID]
	if !open {
		return nil
	}
	return facts.Buf
}

// --- session_info_update builders ---

// newSessionInfoMsg takes the `_meta.kiro` block because session_info_update is a
// CARRIER — 22+ sub-kinds multiplex through it and vibekit dispatches on which sub-BLOCK
// is present, so each helper below fills the one member its frame is about.
func newSessionInfoMsg(kiro map[string]any) *vibekit.RPCResponse {
	update, _ := json.Marshal(map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta":         map[string]any{"kiro": kiro},
	})
	params, _ := json.Marshal(map[string]any{"update": json.RawMessage(update)})
	return &vibekit.RPCResponse{Method: vibekit.MethodSessionUpdate, Params: params}
}

// newTurnStartMsg: KAS emits this bracket for every turn, one vibekit never prompted
// included.
func newTurnStartMsg() *vibekit.RPCResponse {
	return newSessionInfoMsg(map[string]any{"kind": "turn_start", "turnStart": true})
}

// newTurnEndMsg carries the outcome no local closer can know.
func newTurnEndMsg(stop string) *vibekit.RPCResponse {
	return newSessionInfoMsg(map[string]any{
		"kind":    "turn_end",
		"turnEnd": map[string]any{"stopReason": stop},
	})
}

// newTurnCompletionMsg consumes a notification and folds NOTHING, the shape a settle
// bounded by folds alone parks behind forever.
func newTurnCompletionMsg() *vibekit.RPCResponse {
	return newSessionInfoMsg(map[string]any{
		"kind":                "turn_completion",
		"promptTurnSummaries": []map[string]any{{"unit": "credit", "usage": 0.01}},
		"elapsedTime":         float64(1200),
	})
}

// newReplayedTurnEndMsg is a turn_end from a session/load replay: history, not now.
func newReplayedTurnEndMsg(stop string) *vibekit.RPCResponse {
	update, _ := json.Marshal(map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta": map[string]any{"kiro": map[string]any{
			"replay":  true,
			"kind":    "turn_end",
			"turnEnd": map[string]any{"stopReason": stop},
		}},
	})
	params, _ := json.Marshal(map[string]any{"update": json.RawMessage(update)})
	return &vibekit.RPCResponse{Method: vibekit.MethodSessionUpdate, Params: params}
}

// newAgentInitiatedChunkMsg carries the ONE flag that tells a prompted turn from an
// agent-initiated one. It rides content and never the bracket, which is why
// acknowledgement is provisional.
func newAgentInitiatedChunkMsg(text string) *vibekit.RPCResponse {
	update, _ := json.Marshal(map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": text},
		"_meta":         map[string]any{"kiro": map[string]any{"agentInitiated": true}},
	})
	params, _ := json.Marshal(map[string]any{"update": json.RawMessage(update)})
	return &vibekit.RPCResponse{Method: vibekit.MethodSessionUpdate, Params: params}
}

// --- sequence helpers ---

// waitForParkedSettle proves the settle is PARKED before the folder is let move. It
// polls the registry's own state rather than sleeping: the discriminator is that no
// frame has been consumed yet, and a sleep would only make that likely.
func waitForParkedSettle(tb testing.TB, reg *turnRegistry, chatID vibekit.ChatID, epoch vibekit.TurnEpoch, want uint64) {
	tb.Helper()
	lc := reg.lifecycleFor(chatID)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		lc.mu.Lock()
		t := lc.turnLocked(epoch)
		parked := t != nil && t.NeedSeq == want
		lc.mu.Unlock()
		if parked {
			return
		}
		runtime.Gosched()
	}
	tb.Fatalf("the settle for epoch %d never recorded NeedSeq %d, so it is not parked", epoch, want)
}

// payloadsOfType is generic over the payload so a caller reads the FIELD it cares about
// rather than a decoded map, where a lookup would pass on a renamed field.
func payloadsOfType[T any](tb testing.TB, events []sse.ReplayEvent, want vibekit.EventType) []T {
	tb.Helper()
	var out []T
	for _, e := range events {
		var env struct {
			Type    vibekit.EventType `json:"type"`
			Payload T                 `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &env); err != nil {
			tb.Fatalf("unmarshal event: %v", err)
		}
		if env.Type == want {
			out = append(out, env.Payload)
		}
	}
	return out
}

func hasAssistantContent(c *vibekit.Chat, want string) bool {
	if c == nil {
		return false
	}
	for i := range c.Messages {
		if c.Messages[i].Role == vibekit.RoleAssistant && strings.Contains(c.Messages[i].Content, want) {
			return true
		}
	}
	return false
}
