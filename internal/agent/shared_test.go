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

// newTestHub builds a Runtime on context.Background(), which is what New itself used
// to root its lifetime at before that context became a required parameter — so
// every one of the ~200 callers keeps exactly the lifetime it had, and a test
// that wants the runtime torn down calls Shutdown as it always did.
func newTestHub() (*Runtime, *fakeChatStore, *fakeBridge) {
	return newTestHubIn("/tmp/work")
}

// newTestHubIn builds a runtime rooted at workDir. Tests that need a real directory
// use this rather than reassigning h.lifecycle.workDir afterwards: the workspace
// paths are read once at wiring time now (command.Workspace is a value), which is
// what production does — New assigns workDir and nothing reassigns it — so a test
// that mutates it after construction is configuring something the wiring has
// already read.
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

// shutdownHub tears h down and fails the test when a wait outlived the budget.
// Rooted at context.Background() rather than t.Context() because callers reach
// for it from t.Cleanup, where t.Context() is already cancelled. 30s is far
// above anything a unit test should need and below go test's own timeout, so an
// expiry here is a real diagnostic rather than a flake.
func shutdownHub(t *testing.T, h *Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Errorf("runtime shutdown: %v", err)
	}
}

// handleCommand is a test helper that delegates to the dispatcher.
func (rt *Runtime) handleCommand(w http.ResponseWriter, r *http.Request) {
	rt.dispatcher.ServeHTTP(w, r)
}

// postCmd POSTs a typed ClientCommand to handleCommand and returns the recorder.
func postCmd(t *testing.T, h *Runtime, cmd vibekit.ClientCommand) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(cmd)
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	h.handleCommand(rec, req)
	return rec
}

// --- Event inspection helpers ---

// bufferedSince returns the runtime's buffered events with ID greater than
// sinceID — the test-side filter over sse.(*Runtime).Buffered (the library's
// inspection surface is a parameterless snapshot).
func bufferedSince(h *Runtime, sinceID uint64) []sse.ReplayEvent {
	var out []sse.ReplayEvent
	for _, e := range h.bus.fanout.Buffered() {
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
//
// testing.TB rather than *testing.T so a benchmark can build the same wire
// frames a test does. A benchmark that hand-rolls its own copy of a frame is
// how the utility runtime's fixtures drifted off the real envelope.
func mustJSON(t testing.TB, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// newChunkMsg builds a session/update agent_message_chunk notification
// with the given text payload.
//
// The `update` nesting is the protocol, not a detail: KAS sends
// {sessionId, update:{sessionUpdate, content}}. This is the ONE builder for
// that frame in the package — tests, the fake bridge and the benchmark all go
// through it — because four hand-rolled copies of it is how forwardChunk came
// to read the kind off the outer object and drop every chunk while its own
// tests stayed green.
//
// It takes no testing.TB, which is what lets the fake bridge's Call use it: the
// payload is a closed literal of strings so the marshal cannot fail, and a
// builder callable from a fake must not be able to end a test from another
// goroutine (go-rulebook §7).
func newChunkMsg(text string) *vibekit.RPCResponse {
	return newSessionChunkMsg("", text)
}

// newSessionChunkMsg is newChunkMsg with the envelope's `sessionId` set, which
// is what every real frame carries and what the utility bridge's own-session
// screen reads. An empty id omits the key, for the callers that only care about
// the `update` object.
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

// newStepChunkMsg builds a session/update agent_message_chunk carrying a WORKFLOW
// STEP's own marker — the shape a chat-parented run's step frames arrive in on the
// LAUNCHING chat's connection. The subtask id is empty, exactly as KAS sends it on
// a content frame; `_meta.kiro.workflow` is the whole attribution.
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

// --- Turn buffer helpers ---

// stageTurnBuffer returns the buffer this chat's frames fold into, opening a
// wireTurnStart turn when none is open — the test-side equivalent of the first
// frame of a turn vibekit did not prompt arriving.
//
// It replaced a per-chat buffer store the tests reached into directly. There is
// no store any more: a buffer belongs to the turn record that installed it.
func (rt *Runtime) stageTurnBuffer(tb testing.TB, chatID vibekit.ChatID) *buffer.Buffer {
	tb.Helper()
	return rt.coord.TurnFoldTarget(tb.Context(), chatID, vibekit.TurnSourceWireTurnStart)
}

// stagePromptTurn opens a PROMPT turn and hands back its epoch and its buffer, so
// a test of a prompt-scoped closer names the turn it is closing.
//
// The epoch matters: an epoch-scoped closer handed zero closes nothing, because
// zero is also what StartTurn answers when it refuses. A test that passed zero was
// exercising the take-whatever-is-open fallthrough rather than its own closer.
func (rt *Runtime) stagePromptTurn(tb testing.TB, chatID vibekit.ChatID) (vibekit.TurnEpoch, *buffer.Buffer) {
	tb.Helper()
	epoch := rt.coord.StartTurn(tb.Context(), chatID, vibekit.TurnSourcePrompt)
	if epoch == 0 {
		tb.Fatalf("StartTurn(%q) refused, so there is no turn to stage", chatID)
	}
	return epoch, rt.coord.TurnFoldTarget(tb.Context(), chatID, vibekit.TurnSourceWireTurnStart)
}

// liveTurnBuffer returns the chat's open turn's buffer, or nil when no turn is
// open. What the NEXT turn would read, which is what a released-buffer assertion
// is about.
func (rt *Runtime) liveTurnBuffer(chatID vibekit.ChatID) *buffer.Buffer {
	facts, open := rt.coord.turns.openTurns()[chatID]
	if !open {
		return nil
	}
	return facts.Buf
}

// --- session_info_update builders ---

// newSessionInfoMsg builds a session_info_update carrying one `_meta.kiro` block.
//
// session_info_update is a CARRIER: 22+ sub-kinds multiplex through it, and
// vibekit dispatches on which sub-BLOCK is present rather than on the kind
// string. So the builder takes the block, and each helper below fills the one
// member its frame is about — which is also what stops a test asserting on a
// shape KAS does not send.
func newSessionInfoMsg(kiro map[string]any) *vibekit.RPCResponse {
	update, _ := json.Marshal(map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta":         map[string]any{"kiro": kiro},
	})
	params, _ := json.Marshal(map[string]any{"update": json.RawMessage(update)})
	return &vibekit.RPCResponse{Method: vibekit.MethodSessionUpdate, Params: params}
}

// newTurnStartMsg is the wire's own turn_start bracket, which KAS emits for every
// turn including one vibekit never prompted.
func newTurnStartMsg() *vibekit.RPCResponse {
	return newSessionInfoMsg(map[string]any{"kind": "turn_start", "turnStart": true})
}

// newTurnEndMsg is the wire's own turn_end bracket, carrying the outcome no local
// closer can know.
func newTurnEndMsg(stop string) *vibekit.RPCResponse {
	return newSessionInfoMsg(map[string]any{
		"kind":    "turn_end",
		"turnEnd": map[string]any{"stopReason": stop},
	})
}

// newTurnCompletionMsg is a metering frame: it consumes a notification and folds
// NOTHING into any turn, which is the shape a settle bounded by folds alone parks
// behind forever.
func newTurnCompletionMsg() *vibekit.RPCResponse {
	return newSessionInfoMsg(map[string]any{
		"kind":                "turn_completion",
		"promptTurnSummaries": []map[string]any{{"unit": "credit", "usage": 0.01}},
		"elapsedTime":         float64(1200),
	})
}

// newReplayedTurnEndMsg is a turn_end from a session/load replay: stored history,
// not something happening now.
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

// newAgentInitiatedChunkMsg is a content frame carrying the ONE flag that can
// tell a prompted turn from an agent-initiated one. It rides content and never
// the bracket, which is why acknowledgement is provisional.
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

// waitForParkedSettle blocks until the settle for epoch has recorded the position
// it is waiting for, so a test can prove the settle is PARKED before it lets the
// folder move.
//
// It polls the registry's own state rather than sleeping: what makes the
// discriminator below sharp is that no frame has been consumed yet, and a sleep
// would only make that likely.
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

// payloadsOfType decodes every buffered event of one type and returns its
// payload.
//
// Generic over the payload so a caller reads the FIELD it cares about rather
// than a decoded map: the assertions this backs are about a stop reason and a
// count, and a map lookup would pass on a renamed field.
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

// hasAssistantContent reports whether the chat holds an assistant message
// carrying want.
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
