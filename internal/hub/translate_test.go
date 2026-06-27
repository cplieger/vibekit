package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/buffer"
)

// Tests for the translate*.go family: ACP notification → domain-event
// + chat-store translation. Shared fixtures + helpers live in shared_test.go.

func TestTranslateACPEvent_AssistantChunk(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	before := h.sse.replayBuf.Len()
	raw := json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello "}}`)
	msg := &api.RPCResponse{
		Method: "session/update",
		Params: mustJSON(t, map[string]any{"update": raw}),
	}
	h.translateACPEvent("c1", msg)

	gotTypes := extractTypes(t, h.sse.replayBuf.Events()[before:])
	wantSubset(t, gotTypes, "message_created", "message_chunk")

	buf := h.bridge.assistantBufs.GetOrInit("c1")
	if !buf.Started || buf.Content.String() != "hello " {
		t.Errorf("buffer = %+v content=%q", buf.Started, buf.Content.String())
	}
}

func TestTranslateACPEvent_SecondChunkReusesMessageID(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

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
				if len(buf.ToolCalls) != 1 || buf.ToolCalls[0].ID != "tc-1" || buf.ToolCalls[0].Status != api.ToolPending {
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
				if tc.Status != api.ToolCompleted {
					t.Errorf("status = %q, want completed", tc.Status)
				}
				if !strings.Contains(tc.Output, "file contents") {
					t.Errorf("output = %q", tc.Output)
				}
			},
		},
		{
			name: "summarizing_tool_call_dropped",
			events: []json.RawMessage{
				json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-noise","title":"Summarizing","kind":"read","status":"pending"}`),
			},
			assert: func(t *testing.T, buf *buffer.Buffer) {
				if len(buf.ToolCalls) != 0 {
					t.Errorf("Summarizing tool call not filtered: %+v", buf.ToolCalls)
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
			_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
			if tc.setup != nil {
				tc.setup(h)
			}
			for _, raw := range tc.events {
				h.translateACPEvent("c1", &api.RPCResponse{
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
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	raw := json.RawMessage(`{"sessionUpdate":"plan","entries":[{"content":"step 1","priority":"high","status":"pending"}]}`)
	h.translateACPEvent("c1", &api.RPCResponse{
		Method: "session/update",
		Params: mustJSON(t, map[string]any{"update": raw}),
	})

	c, _ := cs.Get(context.Background(), "c1")
	if len(c.Messages) != 1 {
		t.Fatalf("messages = %+v", c.Messages)
	}
	m := c.Messages[0]
	if m.Role != api.RoleAssistant || len(m.Plan) != 1 || m.Plan[0].Content != "step 1" {
		t.Errorf("plan message mismatch: %+v", m)
	}
}

func TestTranslateACPEvent_PermissionRequestEmitsAndPushes(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	before := h.sse.replayBuf.Len()
	msg := &api.RPCResponse{
		Method: "session/request_permission",
		Params: mustJSON(t, map[string]any{
			"id": 42,
			"params": map[string]any{
				"toolCall": map[string]any{"toolCallId": "tc-1", "title": "writeFile"},
				"options": []map[string]any{
					{"option_id": "allow", "name": "Allow", "kind": "allow_once"},
				},
			},
		}),
	}
	h.translateACPEvent("c1", msg)

	types := extractTypes(t, h.sse.replayBuf.Events()[before:])
	wantSubset(t, types, "permission_needed")
}

func TestTranslateACPEvent_MetadataUpdatesUsage(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	msg := &api.RPCResponse{
		Method: "kiro/metadata",
		Params: mustJSON(t, map[string]any{
			"contextUsagePercentage": 42.5,
			"turnDurationMs":         1500.0,
			"meteringUsage":          []map[string]any{{"value": 0.123, "unitPlural": "credits", "unitSingular": "credit"}},
		}),
	}
	h.translateACPEvent("c1", msg)

	c, _ := cs.Get(context.Background(), "c1")
	if c.Usage.ContextPct != 42.5 || c.Usage.LastTurnMs != 1500 || c.Usage.Credits != 0.123 {
		t.Errorf("usage = %+v", c.Usage)
	}
	if c.Usage.TurnCount != 1 {
		t.Errorf("turn_count = %d, want 1", c.Usage.TurnCount)
	}
	if !c.Usage.HasRealData {
		t.Errorf("has_real_data = false")
	}
	if len(c.Usage.MeteringItems) != 1 || c.Usage.MeteringItems[0].UnitPlural != "credits" {
		t.Errorf("metering items = %+v", c.Usage.MeteringItems)
	}
}

func TestTranslateACPEvent_MetadataMissingContextPctDoesNotOverwrite(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Usage.ContextPct = 60
		c.Usage.HasRealData = true
		return true
	})

	// Metadata without contextUsagePercentage: must not flash the pill to 0.
	msg := &api.RPCResponse{
		Method: "kiro/metadata",
		Params: mustJSON(t, map[string]any{
			"turnDurationMs": 500.0,
		}),
	}
	h.translateACPEvent("c1", msg)

	c, _ := cs.Get(context.Background(), "c1")
	if c.Usage.ContextPct != 60 {
		t.Errorf("context_pct = %v, want preserved 60", c.Usage.ContextPct)
	}
}

func TestTranslateACPEvent_MetadataSubagentSessionIDIgnored(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Usage.ContextPct = 50
		return true
	})
	// Spin up a bridge so parentACPSession returns a real id.
	sb, err := h.coord.GetOrCreateBridge(context.Background(), "c1", "", "")
	if err != nil {
		t.Fatalf("getOrCreateBridge: %v", err)
	}
	defer sb.bridge.Stop()

	msg := &api.RPCResponse{
		Method: "kiro/metadata",
		Params: mustJSON(t, map[string]any{
			"sessionId":              "subagent-session-id",
			"contextUsagePercentage": 99.0,
		}),
	}
	h.translateACPEvent("c1", msg)

	c, _ := cs.Get(context.Background(), "c1")
	if c.Usage.ContextPct != 50 {
		t.Errorf("context_pct = %v, want parent value 50 (subagent metadata must not apply)", c.Usage.ContextPct)
	}
}

func TestTranslateACPEvent_MalformedJSONIgnored(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	// Each of these must be a silent no-op, not a panic.
	bad := []*api.RPCResponse{
		{Method: "session/update", Params: json.RawMessage(`{bad`)},
		{Method: "session/update", Params: json.RawMessage(`{"params":{"update":null}}`)},
		{Method: "session/update", Params: nil},
		{Method: "kiro/metadata", Params: json.RawMessage(`not json`)},
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
		msg  *api.RPCResponse
		name string
	}{
		{
			name: "agent_message_chunk",
			msg: &api.RPCResponse{
				Method: "session/update",
				Params: json.RawMessage(`{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello, world! This is a representative chunk of assistant output."}}}`),
			},
		},
		{
			name: "tool_call",
			msg: &api.RPCResponse{
				Method: "session/update",
				Params: json.RawMessage(`{"update":{"sessionUpdate":"tool_call","toolCallId":"tc-bench-1","title":"readFile","kind":"read","status":"pending","locations":[{"path":"main.go","line":10}]}}`),
			},
		},
		{
			name: "tool_call_update",
			msg: &api.RPCResponse{
				Method: "session/update",
				Params: json.RawMessage(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-bench-1","status":"completed","content":[{"type":"content","content":{"text":"package main\nfunc main() {}\n"}}]}}`),
			},
		},
	}

	for _, p := range payloads {
		b.Run(p.name, func(b *testing.B) {
			h, cs, _ := newTestHub()
			_ = cs.Mutate(context.Background(), "bench", func(c *api.Chat, _ bool) bool {
				c.Name = "bench"
				return true
			})
			// Pre-seed a tool call for tool_call_update to find.
			if p.name == "tool_call_update" {
				buf := h.bridge.assistantBufs.GetOrInit("bench")
				buf.Started = true
				buf.MessageID = "msg-bench"
				buf.ToolCalls = append(buf.ToolCalls, api.ToolCall{ID: "tc-bench-1", Status: api.ToolPending})
				buf.RecordToolStart("tc-bench-1")
			}
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				h.translateACPEvent("bench", p.msg)
			}
		})
	}
}

// --- Fuzz targets ---

// FuzzTranslateCommands feeds arbitrary byte sequences through the
// _kiro.dev/commands/available JSON parsing path. Verifies that
// malformed payloads are silent no-ops (no panics) and that the
// replay buffer length is non-decreasing (no corruption).
func FuzzTranslateCommands(f *testing.F) {
	seeds := []string{
		`{"commands":[{"name":"/help","description":"Show help"}],"prompts":[{"name":"review"}],"tools":[{},{}],"mcpServers":[{"name":"srv","status":"running","tools":["t1"]}]}`,
		`{"commands":[],"prompts":[],"tools":[],"mcpServers":[]}`,
		`{"commands":[{"name":"/tools","description":"List tools"}],"tools":[1,2,3],"mcpServers":[{"name":"a","status":"running","tools":[]},{"name":"b","status":"idle","tools":[]}]}`,
		`{}`,
		`{"commands":[{"name":"/mcp","description":"MCP servers"}],"mcpServers":[{"name":"` + strings.Repeat("あ", 100) + `","status":"running","tools":[]}]}`,
		`not json at all`,
		`{"commands":null,"prompts":null}`,
		`{"commands":[{"name":"/paste"}],"tools":[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20]}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "fuzz", func(c *api.Chat, _ bool) bool {
		c.Name = "fuzz"
		return true
	})

	f.Fuzz(func(t *testing.T, data []byte) {
		before := h.sse.replayBuf.Len()
		msg := &api.RPCResponse{
			Method: "_kiro.dev/commands/available",
			Params: data,
		}
		// Must not panic.
		h.translateACPEvent("fuzz", msg)
		if h.sse.replayBuf.Len() < before {
			t.Errorf("replay buffer shrank: %d → %d", before, h.sse.replayBuf.Len())
		}
	})
}

// FuzzTranslateMCP feeds arbitrary byte sequences through the
// _kiro.dev/mcp/* JSON parsing paths (handleMCPInitialized,
// handleMCPOAuth, handleMCPInitFailure). Verifies that arbitrary
// payloads never panic and that mcpRegistry state remains consistent.
func FuzzTranslateMCP(f *testing.F) {
	seeds := []string{
		`{"serverName":"my-server"}`,
		`{"serverName":"oauth-srv","oauthUrl":"https://example.com/auth"}`,
		`{"serverName":"fail-srv","error":"connection refused"}`,
		`{"serverName":""}`,
		`{}`,
		`{"serverName":null}`,
		`{"serverName":"` + strings.Repeat("x", 1000) + `"}`,
		`not json`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "fuzz", func(c *api.Chat, _ bool) bool {
		c.Name = "fuzz"
		return true
	})

	methods := []string{
		"_kiro.dev/mcp/server_initialized",
		"_kiro.dev/mcp/oauth_request",
		"_kiro.dev/mcp/server_init_failure",
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Route through all three methods based on first byte.
		var idx int
		if len(data) > 0 {
			idx = int(data[0]) % len(methods)
		}
		msg := &api.RPCResponse{
			Method: methods[idx],
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
	_ = cs.Mutate(context.Background(), "fuzz", func(c *api.Chat, _ bool) bool {
		c.Name = "fuzz"
		return true
	})

	f.Fuzz(func(t *testing.T, data []byte) {
		msg := &api.RPCResponse{
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
	msg := &api.RPCResponse{
		ID:     &id,
		Method: api.MethodFSRead,
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
	h.agentTerms.terms["term-1"] = &agentTerminal{
		done:   make(chan struct{}),
		output: newByteRing(64),
		chatID: "c1",
	}
	h.agentTerms.mu.Unlock()

	id := int64(7110)
	msg := &api.RPCResponse{
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
func registerParentSession(t *testing.T, h *Hub, chatID api.ChatID, parentSession string) {
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
func captureSubSession(t *testing.T, h *Hub, chatID api.ChatID, sessionID string) (got string, called bool) {
	t.Helper()
	h.sessUpdateHandlers = map[api.ACPUpdateKind]sessionUpdateHandler{
		api.ACPUpdateAgentChunk: func(_ context.Context, _ api.ChatID, _ json.RawMessage, sub string) {
			got = sub
			called = true
		},
	}
	update := mustJSON(t, map[string]any{
		"sessionUpdate": string(api.ACPUpdateAgentChunk),
		"content":       map[string]any{"type": "text", "text": "x"},
	})
	params := mustJSON(t, map[string]any{
		"sessionId": sessionID,
		"update":    update,
	})
	msg := &api.RPCResponse{Method: "session/update", Params: params}
	h.handleSessionUpdate(context.Background(), chatID, msg)
	return got, called
}

// subSessionID is the notification's sessionId only when it is
// non-empty AND a parent session exists AND they differ; otherwise the
// update is attributed to the parent (subSessionID == "").
func TestHandleSessionUpdate_SubSessionAttribution(t *testing.T) {
	cases := []struct {
		name       string
		chatID     api.ChatID
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
			defer h.Shutdown()
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
