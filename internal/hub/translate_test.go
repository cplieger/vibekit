package hub

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// Tests for the translate*.go family: ACP notification → domain-event
// + chat-store translation. Shared fixtures + helpers live in shared_test.go.

func TestTranslateACPEvent_AssistantChunk(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	_, before := h.sse.hub.Bounds()
	raw := json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello "}}`)
	msg := &vibekit.RPCResponse{
		Method: "session/update",
		Params: mustJSON(t, map[string]any{"update": raw}),
	}
	h.translateACPEvent("c1", msg)

	gotTypes := extractTypes(t, bufferedSince(h, before))
	if missing := missingEvents(gotTypes, "message_created", "message_chunk"); len(missing) > 0 {
		t.Errorf("missing events %v; got %v", missing, gotTypes)
	}

	buf := h.bridge.assistantBufs.GetOrInit("c1")
	if !buf.Started || buf.Content.String() != "hello " {
		t.Errorf("buffer = %+v content=%q", buf.Started, buf.Content.String())
	}
}

func TestTranslateACPEvent_SecondChunkReusesMessageID(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	h.translateACPEvent("c1", newChunkMsg(t, "one"))
	firstID := h.bridge.assistantBufs.GetOrInit("c1").MessageID

	h.translateACPEvent("c1", newChunkMsg(t, "two"))
	secondID := h.bridge.assistantBufs.GetOrInit("c1").MessageID

	if firstID != secondID {
		t.Errorf("message_id changed between chunks: %q → %q", firstID, secondID)
	}
	if h.bridge.assistantBufs.GetOrInit("c1").Content.String() != "onetwo" {
		t.Errorf("buffer content = %q, want 'onetwo'", h.bridge.assistantBufs.GetOrInit("c1").Content.String())
	}
}

func TestTranslateACPEvent_ToolCalls(t *testing.T) {
	cases := []struct {
		setup  func(*Hub)
		assert func(*testing.T, *buffer.Buffer)
		name   string
		events []json.RawMessage
	}{
		{
			name: "tool_call_added_to_buffer",
			events: []json.RawMessage{
				json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"readFile","kind":"read","status":"pending"}`),
			},
			assert: func(t *testing.T, buf *buffer.Buffer) {
				if len(buf.ToolCalls) != 1 || buf.ToolCalls[0].ID != "tc-1" || buf.ToolCalls[0].Status != vibekit.ToolPending {
					t.Errorf("buffer tool_calls = %+v", buf.ToolCalls)
				}
			},
		},
		{
			name: "tool_call_update_mutates_existing",
			events: []json.RawMessage{
				json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"readFile","kind":"read","status":"pending"}`),
				json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed","content":[{"type":"content","content":{"text":"file contents"}}]}`),
			},
			assert: func(t *testing.T, buf *buffer.Buffer) {
				if len(buf.ToolCalls) != 1 {
					t.Fatalf("tool_calls = %+v", buf.ToolCalls)
				}
				tc := buf.ToolCalls[0]
				if tc.Status != vibekit.ToolCompleted {
					t.Errorf("status = %q, want completed", tc.Status)
				}
				if !strings.Contains(tc.Output, "file contents") {
					t.Errorf("output = %q", tc.Output)
				}
			},
		},
		{
			// v3: subagents ARE ordinary tool calls now — there is no
			// noise-title filter, so every tool_call passes through.
			name: "tool_call_passes_through_without_noise_filter",
			events: []json.RawMessage{
				json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-noise","title":"Summarizing","kind":"read","status":"pending"}`),
			},
			assert: func(t *testing.T, buf *buffer.Buffer) {
				if len(buf.ToolCalls) != 1 || buf.ToolCalls[0].ID != "tc-noise" {
					t.Errorf("tool call not passed through: %+v", buf.ToolCalls)
				}
			},
		},
		{
			name: "tool_call_with_locations",
			events: []json.RawMessage{
				json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-loc","title":"Reading main.go","kind":"read","status":"pending","locations":[{"path":"main.go","line":42}]}`),
			},
			assert: func(t *testing.T, buf *buffer.Buffer) {
				if len(buf.ToolCalls) != 1 {
					t.Fatalf("tool_calls = %+v", buf.ToolCalls)
				}
				tc := buf.ToolCalls[0]
				if len(tc.Locations) != 1 || tc.Locations[0].Path != "main.go" || tc.Locations[0].Line != 42 {
					t.Errorf("locations = %+v", tc.Locations)
				}
			},
		},
		{
			name: "tool_call_with_diffs",
			events: []json.RawMessage{
				json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-diff","title":"Editing main.go","kind":"edit","status":"pending","locations":[{"path":"main.go","line":1}],"content":[{"type":"diff","path":"/abs/main.go","oldText":"hello","newText":"world"}]}`),
			},
			assert: func(t *testing.T, buf *buffer.Buffer) {
				if len(buf.ToolCalls) != 1 {
					t.Fatalf("tool_calls = %+v", buf.ToolCalls)
				}
				tc := buf.ToolCalls[0]
				if len(tc.Diffs) != 1 || tc.Diffs[0].OldText != "hello" || tc.Diffs[0].NewText != "world" {
					t.Errorf("diffs = %+v", tc.Diffs)
				}
			},
		},
		{
			name: "tool_call_update_with_locations",
			events: []json.RawMessage{
				json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"readFile","kind":"read","status":"pending"}`),
				json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed","locations":[{"path":"config.go"}]}`),
			},
			assert: func(t *testing.T, buf *buffer.Buffer) {
				tc := buf.ToolCalls[0]
				if len(tc.Locations) != 1 || tc.Locations[0].Path != "config.go" {
					t.Errorf("locations = %+v", tc.Locations)
				}
			},
		},
		{
			name: "tool_call_update_with_diffs",
			events: []json.RawMessage{
				json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"editFile","kind":"edit","status":"pending"}`),
				json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed","content":[{"type":"diff","path":"/abs/file.go","oldText":"old","newText":"new"}]}`),
			},
			assert: func(t *testing.T, buf *buffer.Buffer) {
				tc := buf.ToolCalls[0]
				if len(tc.Diffs) != 1 || tc.Diffs[0].Path != "/abs/file.go" {
					t.Errorf("diffs = %+v", tc.Diffs)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
			if tc.setup != nil {
				tc.setup(h)
			}
			for _, raw := range tc.events {
				h.translateACPEvent("c1", &vibekit.RPCResponse{
					Method: "session/update",
					Params: mustJSON(t, map[string]any{"update": raw}),
				})
			}
			buf := h.bridge.assistantBufs.GetOrInit("c1")
			tc.assert(t, buf)
		})
	}
}

func TestTranslateACPEvent_PlanPersistsAsMessage(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	raw := json.RawMessage(`{"sessionUpdate":"plan","entries":[{"content":"step 1","priority":"high","status":"pending"}]}`)
	h.translateACPEvent("c1", &vibekit.RPCResponse{
		Method: "session/update",
		Params: mustJSON(t, map[string]any{"update": raw}),
	})

	c, _ := cs.Get(t.Context(), "c1")
	if len(c.Messages) != 1 {
		t.Fatalf("messages = %+v", c.Messages)
	}
	m := c.Messages[0]
	if m.Role != vibekit.RoleAssistant || len(m.Plan) != 1 || m.Plan[0].Content != "step 1" {
		t.Errorf("plan message mismatch: %+v", m)
	}
}

func TestTranslateACPEvent_PermissionRequestEmitsAndPushes(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	_, before := h.sse.hub.Bounds()
	// v3 wire shape: the correlation id is on the JSON-RPC envelope (msg.ID)
	// and the params are FLAT ({sessionId, toolCall, options}); the option id
	// is camelCase `optionId`. (The pre-fix shape nested id+params inside
	// msg.Params and used option_id — which decoded to an empty, unanswerable
	// request; see HandlePermissionRequest.)
	permID := int64(42)
	msg := &vibekit.RPCResponse{
		ID:     &permID,
		Method: "session/request_permission",
		Params: mustJSON(t, map[string]any{
			"sessionId": "c1-sess",
			"toolCall":  map[string]any{"toolCallId": "tc-1", "title": "writeFile", "kind": "edit"},
			"options": []map[string]any{
				{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
			},
		}),
	}
	h.translateACPEvent("c1", msg)

	types := extractTypes(t, bufferedSince(h, before))
	if missing := missingEvents(types, "permission_needed"); len(missing) > 0 {
		t.Errorf("missing events %v; got %v", missing, types)
	}
}

func TestTranslateACPEvent_MalformedJSONIgnored(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	// Each of these must be a silent no-op, not a panic.
	bad := []*vibekit.RPCResponse{
		{Method: "session/update", Params: json.RawMessage(`{bad`)},
		{Method: "session/update", Params: json.RawMessage(`{"params":{"update":null}}`)},
		{Method: "session/update", Params: nil},
		{Method: "_kiro/mcp/status", Params: json.RawMessage(`not json`)},
		{Method: "session/request_permission", Params: json.RawMessage(`{`)},
		{Method: "unknown_method", Params: json.RawMessage(`{}`)},
	}
	for _, m := range bad {
		h.translateACPEvent("c1", m)
	}
}

// --- Benchmarks ---

// BenchmarkTranslateACPEvent exercises the session/update dispatch hot
// path with representative payloads (agent_message_chunk, tool_call,
// tool_call_update). Surfaces JSON decode regressions and allocation
// growth under varying iteration counts.
func BenchmarkTranslateACPEvent(b *testing.B) {
	payloads := []struct {
		msg  *vibekit.RPCResponse
		name string
	}{
		{
			name: "agent_message_chunk",
			msg: &vibekit.RPCResponse{
				Method: "session/update",
				Params: json.RawMessage(`{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello, world! This is a representative chunk of assistant output."}}}`),
			},
		},
		{
			name: "tool_call",
			msg: &vibekit.RPCResponse{
				Method: "session/update",
				Params: json.RawMessage(`{"update":{"sessionUpdate":"tool_call","toolCallId":"tc-bench-1","title":"readFile","kind":"read","status":"pending","locations":[{"path":"main.go","line":10}]}}`),
			},
		},
		{
			name: "tool_call_update",
			msg: &vibekit.RPCResponse{
				Method: "session/update",
				Params: json.RawMessage(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-bench-1","status":"completed","content":[{"type":"content","content":{"text":"package main\nfunc main() {}\n"}}]}}`),
			},
		},
	}

	for _, p := range payloads {
		b.Run(p.name, func(b *testing.B) {
			h, cs, _ := newTestHub()
			_ = cs.Mutate(b.Context(), "bench", func(c *vibekit.Chat, _ bool) bool {
				c.Name = "bench"
				return true
			})
			// Pre-seed a tool call for tool_call_update to find.
			if p.name == "tool_call_update" {
				buf := h.bridge.assistantBufs.GetOrInit("bench")
				buf.Started = true
				buf.MessageID = "msg-bench"
				buf.ToolCalls = append(buf.ToolCalls, vibekit.ToolCall{ID: "tc-bench-1", Status: vibekit.ToolPending})
				buf.RecordToolStart("tc-bench-1")
			}
			b.ResetTimer()
			b.ReportAllocs()
			for b.Loop() {
				h.translateACPEvent("bench", p.msg)
			}
		})
	}
}

// --- Fuzz targets ---

// FuzzTranslateInitErrors feeds arbitrary byte sequences through the v3
// init-error / notice JSON parsing paths (init_errors.go: rate_limit,
// customAgent/not_found, customAgent/config_error, system/notify). Verifies
// that malformed payloads are silent no-ops (no panics) and that the replay
// buffer length is non-decreasing (no corruption). The v2 commands/available
// path this target used to fuzz is, on v3, a session/update sub-kind covered
// by FuzzHandleSessionUpdate.
func FuzzTranslateInitErrors(f *testing.F) {
	seeds := []string{
		`{"message":"rate limited, retry in 30s"}`,
		`{"requestedAgent":"planner","fallbackAgent":"vibe"}`,
		`{"path":"/config/agents/x.json","error":"parse error"}`,
		`{"level":"warning","message":"model under high load"}`,
		`{}`,
		`{"message":null}`,
		`{"requestedAgent":"` + strings.Repeat("あ", 100) + `","fallbackAgent":""}`,
		`not json at all`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	h, cs, _ := newTestHub()
	_ = cs.Mutate(f.Context(), "fuzz", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "fuzz"
		return true
	})

	methods := []string{
		"_kiro/error/rate_limit",
		"_kiro/customAgent/not_found",
		"_kiro/customAgent/config_error",
		"_kiro/system/notify",
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		_, before := h.sse.hub.Bounds()
		var idx int
		if len(data) > 0 {
			idx = int(data[0]) % len(methods)
		}
		msg := &vibekit.RPCResponse{
			Method: methods[idx],
			Params: data,
		}
		// Must not panic.
		h.translateACPEvent("fuzz", msg)
		if _, head := h.sse.hub.Bounds(); head < before {
			t.Errorf("event head went backwards from %d", before)
		}
	})
}

// FuzzTranslateMCP feeds arbitrary byte sequences through the v3
// _kiro/mcp/status JSON parsing path (HandleMCPStatus, which consolidates
// v2's per-server server_initialized / oauth_request / server_init_failure
// into one status list). Verifies that arbitrary payloads never panic and
// that mcpRegistry state remains consistent.
func FuzzTranslateMCP(f *testing.F) {
	seeds := []string{
		`{"servers":[{"name":"my-server","status":"connected","tools":[{"name":"t1"}]}]}`,
		`{"servers":[{"name":"oauth-srv","status":"failed","authorizationUrl":"https://example.com/auth"}]}`,
		`{"servers":[{"name":"fail-srv","status":"failed","errorMessage":"connection refused"}]}`,
		`{"servers":[{"name":"","status":"connecting"}]}`,
		`{"servers":[]}`,
		`{}`,
		`{"servers":null}`,
		`{"servers":[{"name":"` + strings.Repeat("x", 1000) + `","status":"connected","tools":null}]}`,
		`not json`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	h, cs, _ := newTestHub()
	_ = cs.Mutate(f.Context(), "fuzz", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "fuzz"
		return true
	})

	f.Fuzz(func(t *testing.T, data []byte) {
		msg := &vibekit.RPCResponse{
			Method: "_kiro/mcp/status",
			Params: data,
		}
		// Must not panic.
		h.translateACPEvent("fuzz", msg)
		// mcpRegistry.Snapshot must not panic (concurrent-safe read).
		_ = h.mcpRegistry.Snapshot()
	})
}

// FuzzHandleSessionUpdate feeds arbitrary byte sequences through the
// session/update JSON-envelope dispatcher. Verifies that malformed
// JSON and unknown sessionUpdate subtypes are silent no-ops (no panics).
// Seed corpus covers known update shapes.
func FuzzHandleSessionUpdate(f *testing.F) {
	// Seed corpus: known sessionUpdate subtypes.
	seeds := []string{
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}`,
		`{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"read","kind":"read","status":"pending"}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed","content":[{"type":"content","content":{"text":"out"}}]}`,
		`{"sessionUpdate":"plan","entries":[{"content":"step","priority":"high","status":"pending"}]}`,
		`{"sessionUpdate":"current_mode_update","modeId":"code"}`,
		`{}`,
		`{"sessionUpdate":"unknown_future_subtype","data":123}`,
		`not json at all`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	h, cs, _ := newTestHub()
	_ = cs.Mutate(f.Context(), "fuzz", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "fuzz"
		return true
	})

	f.Fuzz(func(t *testing.T, data []byte) {
		msg := &vibekit.RPCResponse{
			Method: "session/update",
			Params: json.RawMessage(`{"update":` + string(data) + `}`),
		}
		// Must not panic.
		h.translateACPEvent("fuzz", msg)
	})
}

// --- Request routing (folded mutant-killing coverage) ---

// An fs/* request (ID != nil) is routed to the FS handler, which
// responds back through the bridge.
func TestTranslateACPEvent_RoutesFSRequest(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "r.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	id := int64(7106)
	msg := &vibekit.RPCResponse{
		ID:     &id,
		Method: vibekit.MethodFSRead,
		Params: mustJSON(t, map[string]any{"path": "r.txt"}),
	}
	h.translateACPEvent("c1", msg)
	select {
	case <-br.done:
	case <-time.After(3 * time.Second):
		t.Fatal("fs/read request was not routed to the FS handler")
	}
}

// A terminal/* request (ID != nil) is routed to the terminal handler,
// which responds back through the bridge.
func TestTranslateACPEvent_RoutesTerminalRequest(t *testing.T) {
	h, br := hubForFSTest(t, t.TempDir())
	// Pre-register a terminal owned by "c1" so termOutput resolves it
	// and responds through the registered respondingBridge.
	h.agentTerms.mu.Lock()
	h.agentTerms.terms["term-1"] = newAgentTerminal(nil, "c1", 64)
	h.agentTerms.mu.Unlock()

	id := int64(7110)
	msg := &vibekit.RPCResponse{
		ID:     &id,
		Method: methodTermOutput,
		Params: mustJSON(t, map[string]any{"terminalId": "term-1"}),
	}
	h.translateACPEvent("c1", msg)
	select {
	case <-br.done:
	case <-time.After(3 * time.Second):
		t.Fatal("terminal/output request was not routed to the terminal handler")
	}
}

// --- Subagent attribution in handleSessionUpdate ---

// registerParentSession registers a bridge for chatID whose SessionID
// is parentSession, so h.parentACPSession(chatID) returns it.
func registerParentSession(t *testing.T, h *Hub, chatID vibekit.ChatID, parentSession string) {
	t.Helper()
	sb, _ := h.bridge.mgr.getOrInsert(chatID)
	br := newFakeBridge()
	br.sessionID = parentSession
	sb.bridge = br
	sb.state = bridgeIdle
}

// captureSubSession installs a capturing sub-handler for the
// agent_message_chunk kind, drives handleSessionUpdate with a
// notification carrying sessionID, and returns the subSessionID the
// dispatcher computed plus whether the handler ran (false => sub
// dispatch returned early).
func captureSubSession(t *testing.T, h *Hub, chatID vibekit.ChatID, sessionID string) (got string, called bool) {
	t.Helper()
	h.sessUpdateHandlers = map[vibekit.ACPUpdateKind]sessionUpdateHandler{
		vibekit.ACPUpdateAgentChunk: func(_ context.Context, _ vibekit.ChatID, _ json.RawMessage, sub string) {
			got = sub
			called = true
		},
	}
	update := mustJSON(t, map[string]any{
		"sessionUpdate": string(vibekit.ACPUpdateAgentChunk),
		"content":       map[string]any{"type": "text", "text": "x"},
	})
	params := mustJSON(t, map[string]any{
		"sessionId": sessionID,
		"update":    update,
	})
	msg := &vibekit.RPCResponse{Method: "session/update", Params: params}
	h.handleSessionUpdate(t.Context(), chatID, msg)
	return got, called
}

// subSessionID is the notification's sessionId only when it is
// non-empty AND a parent session exists AND they differ; otherwise the
// update is attributed to the parent (subSessionID == "").
func TestHandleSessionUpdate_SubSessionAttribution(t *testing.T) {
	cases := []struct {
		name       string
		chatID     vibekit.ChatID
		registerPS string // parent session to register; "" => no bridge (parent == "")
		sessionID  string
		want       string
	}{
		{
			name:       "subagent_when_session_nonempty_parent_set_and_differs",
			chatID:     "chat-sub",
			registerPS: "parent-A",
			sessionID:  "sub-B",
			want:       "sub-B",
		},
		{
			name:       "parent_when_session_equals_parent",
			chatID:     "chat-match",
			registerPS: "same",
			sessionID:  "same",
			want:       "",
		},
		{
			name:       "parent_when_no_parent_bridge",
			chatID:     "chat-noparent",
			registerPS: "",
			sessionID:  "sub-C",
			want:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			defer shutdownHub(t, h)
			if tc.registerPS != "" {
				registerParentSession(t, h, tc.chatID, tc.registerPS)
			}
			got, called := captureSubSession(t, h, tc.chatID, tc.sessionID)
			if !called {
				t.Fatalf("handleSessionUpdate did not invoke the sub-handler (sub-dispatch returned early)")
			}
			if got != tc.want {
				t.Errorf("handleSessionUpdate subSessionID = %q, want %q (parent=%q, sessionID=%q)",
					got, tc.want, tc.registerPS, tc.sessionID)
			}
		})
	}
}

// --- Replayed frames must not reach the live handlers ---

// dispatchUpdate installs a capturing sub-handler for `kind`, drives
// handleSessionUpdate with an update carrying that kind plus whatever extra
// fields `extra` supplies, and reports whether the live handler ran.
func dispatchUpdate(t *testing.T, h *Hub, kind vibekit.ACPUpdateKind, extra map[string]any) (called bool) {
	t.Helper()
	h.sessUpdateHandlers = map[vibekit.ACPUpdateKind]sessionUpdateHandler{
		kind: func(_ context.Context, _ vibekit.ChatID, _ json.RawMessage, _ string) { called = true },
	}
	update := map[string]any{"sessionUpdate": string(kind)}
	maps.Copy(update, extra)
	params := mustJSON(t, map[string]any{"sessionId": "", "update": mustJSON(t, update)})
	h.handleSessionUpdate(t.Context(), "c1",
		&vibekit.RPCResponse{Method: "session/update", Params: params})
	return called
}

// TestHandleSessionUpdate_DropsReplayedFrames pins the gate that keeps stored
// history out of the live path.
//
// KAS replays a session's entire transcript as ordinary session/update
// notifications when vibekit calls session/load — which it does on every
// container-restart resume and every model-switch fallback. Measured against
// kiro-cli 2.16.0: a load of a one-turn session returns 9 frames, 6 of them
// tagged `_meta.kiro.replay: true`.
//
// Ungated, the replayed agent_message_chunk reaches HandleAssistantChunk,
// which opens a PHANTOM turn: a fresh message id whose message_created and
// message_chunk events re-stream history to every connected client as though
// the agent were typing it now.
//
// The nesting is the trap this pins. The flag rides `update._meta.kiro.replay`,
// NOT `params._meta` — reading it a level up yields false for every frame,
// which is indistinguishable from a wire that never sets it.
func TestHandleSessionUpdate_DropsReplayedFrames(t *testing.T) {
	replayMeta := map[string]any{"kiro": map[string]any{"replay": true}}

	tests := []struct {
		name       string
		extra      map[string]any
		wantCalled bool
	}{
		{
			name:       "a live frame reaches its handler",
			extra:      map[string]any{"content": map[string]any{"type": "text", "text": "x"}},
			wantCalled: true,
		},
		{
			name: "a replay-tagged frame is dropped",
			extra: map[string]any{
				"content": map[string]any{"type": "text", "text": "x"},
				"_meta":   replayMeta,
			},
			wantCalled: false,
		},
		{
			name: "replay:false is not a replay",
			extra: map[string]any{
				"content": map[string]any{"type": "text", "text": "x"},
				"_meta":   map[string]any{"kiro": map[string]any{"replay": false}},
			},
			wantCalled: true,
		},
		{
			name: "the flag one level UP does not gate — it rides `update`, not `params`",
			extra: map[string]any{
				"content": map[string]any{"type": "text", "text": "x"},
				"_meta":   map[string]any{"replay": true}, // missing the kiro nesting
			},
			wantCalled: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			if got := dispatchUpdate(t, h, vibekit.ACPUpdateAgentChunk, tt.extra); got != tt.wantCalled {
				t.Errorf("live handler called = %v, want %v", got, tt.wantCalled)
			}
		})
	}
}

// TestHandleSessionUpdate_CatalogFrameSurvivesALoad pins that the gate is
// per-frame rather than "suppress everything while loading".
//
// KAS does NOT tag config_option_update as replay (3 of the 9 measured frames
// are untagged and this is one): it carries the session's CURRENT model/mode
// selection, not its history. Gating on the load OPERATION instead of the
// per-frame flag would drop it, and the mode pill would come back empty after
// every resume.
//
// available_commands_update was the other witness to this property and is no
// longer dispatched at all (the slash-command catalog had no consumer), so
// config_option_update is the only one left — which makes this test MORE
// load-bearing, not less.
func TestHandleSessionUpdate_CatalogFrameSurvivesALoad(t *testing.T) {
	h, _, _ := newTestHub()
	if !dispatchUpdate(t, h, vibekit.ACPUpdateConfigOption, nil) {
		t.Error("config_option_update was dropped; it is untagged by KAS and carries current session state")
	}
}
