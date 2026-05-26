package hub

// Tests for the _kiro.dev/* translate handlers:
//   - translate_mcp.go     (server_initialized, oauth_request)
//   - translate_compact.go (compaction/status)
//   - translate_commands.go(commands/available)
//   - translate_crew.go    (subagent/list_update)
//
// Shared fixtures live in shared_test.go.

import (
	"context"
	"encoding/json"
	"testing"

	"vibekit/internal/api"
	"vibekit/internal/translate"
)

// --- MCP ---

func TestTranslateMCP(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		params     any // passed to mustJSON; use json.RawMessage for raw input
		rawParams  json.RawMessage
		wantSnap   func(t *testing.T, snap []mcpServerRuntime)
		wantEvents []string // subset of emitted SSE event types
	}{
		{
			name:   "ServerInitializedDecodesServerName",
			method: "_kiro.dev/mcp/server_initialized",
			params: map[string]any{"serverName": "github"},
			wantSnap: func(t *testing.T, snap []mcpServerRuntime) {
				t.Helper()
				if len(snap) != 1 || snap[0].Name != "github" {
					t.Fatalf("registry snapshot = %+v", snap)
				}
				if snap[0].State != mcpStateConnected {
					t.Errorf("state = %q, want %q", snap[0].State, mcpStateConnected)
				}
			},
		},
		{
			name:   "ServerInitializedMissingNameDropped",
			method: "_kiro.dev/mcp/server_initialized",
			params: map[string]any{"server": "github"},
			wantSnap: func(t *testing.T, snap []mcpServerRuntime) {
				t.Helper()
				if len(snap) != 0 {
					t.Error("legacy field name incorrectly accepted; kiro-cli 2.0.1 emits serverName only")
				}
			},
		},
		{
			name:   "OAuthRequestEmitsEvent",
			method: "_kiro.dev/mcp/oauth_request",
			params: map[string]any{"serverName": "linear", "oauthUrl": "https://oauth.example/authorize"},
			wantSnap: func(t *testing.T, snap []mcpServerRuntime) {
				t.Helper()
				if len(snap) != 1 || snap[0].OAuthURL != "https://oauth.example/authorize" {
					t.Errorf("snapshot = %+v", snap)
				}
			},
			wantEvents: []string{"mcp_oauth_needed"},
		},
		{
			name:   "OAuthLegacyFieldNamesDropped",
			method: "_kiro.dev/mcp/oauth_request",
			params: map[string]any{"server": "linear", "url": "https://x"},
			wantSnap: func(t *testing.T, snap []mcpServerRuntime) {
				t.Helper()
				if len(snap) != 0 {
					t.Error("legacy oauth field names incorrectly accepted")
				}
			},
		},
		{
			name:   "InitFailureRecordsError",
			method: "_kiro.dev/mcp/server_init_failure",
			params: map[string]any{"serverName": "broken", "error": "connection refused"},
			wantSnap: func(t *testing.T, snap []mcpServerRuntime) {
				t.Helper()
				if len(snap) != 1 || snap[0].Name != "broken" {
					t.Fatalf("snapshot = %+v", snap)
				}
				if snap[0].State != mcpStateFailed {
					t.Errorf("state = %q, want %q", snap[0].State, mcpStateFailed)
				}
				if snap[0].Error != "connection refused" {
					t.Errorf("error = %q", snap[0].Error)
				}
			},
			wantEvents: []string{"mcp_failed"},
		},
		{
			name:      "MalformedServerInitializedDropped",
			method:    "_kiro.dev/mcp/server_initialized",
			rawParams: json.RawMessage(`{"not-json`),
			wantSnap: func(t *testing.T, snap []mcpServerRuntime) {
				t.Helper()
				if len(snap) != 0 {
					t.Error("malformed payload affected registry")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			before := h.sse.replayBuf.Len()

			var params json.RawMessage
			if tc.rawParams != nil {
				params = tc.rawParams
			} else {
				params = mustJSON(t, tc.params)
			}
			msg := &api.RPCResponse{Method: tc.method, Params: params}
			h.translateACPEvent("", msg)

			if tc.wantSnap != nil {
				tc.wantSnap(t, h.mcpRegistry.Snapshot())
			}
			if len(tc.wantEvents) > 0 {
				types := extractTypes(t, h.sse.replayBuf.Events()[before:])
				wantSubset(t, types, tc.wantEvents...)
			}
		})
	}
}

// --- Commands ---

func TestTranslateCommands_FiltersBrowserIncompatibleEntries(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	before := h.sse.replayBuf.Len()

	commands := []map[string]any{
		{"name": "/compact", "description": "Summarise"},
		{"name": "/paste", "description": "Clipboard paste"},
		{"name": "/reply", "description": "Open editor"},
		{"name": "/quit", "description": "Exit"},
		{"name": "/chat", "description": "Manage chats"},
		{"name": "/knowledge", "description": "Search docs"},
	}
	msg := &api.RPCResponse{
		Method: "_kiro.dev/commands/available",
		Params: mustJSON(t, map[string]any{"commands": commands}),
	}
	h.translateACPEvent("c1", msg)

	// Find the emitted commands_updated event.
	var payload struct {
		Payload struct {
			Commands []map[string]any `json:"commands"`
		} `json:"payload"`
	}
	var found bool
	for _, ev := range h.sse.replayBuf.Events()[before:] {
		var raw struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(ev.data, &raw)
		if raw.Type == "commands_updated" {
			_ = json.Unmarshal(ev.data, &payload)
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no commands_updated event emitted")
	}
	got := make([]string, 0, len(payload.Payload.Commands))
	for _, c := range payload.Payload.Commands {
		if name, ok := c["name"].(string); ok {
			got = append(got, name)
		}
	}
	// FilterCommands is now a no-op; all commands pass through.
	if len(got) != 6 {
		t.Errorf("got %v, want all 6 commands (filter is no-op)", got)
	}
}

// --- Compaction ---

func TestTranslateCompact_StartedEmitsTransient(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	before := h.sse.replayBuf.Len()

	msg := &api.RPCResponse{
		Method: "_kiro.dev/compaction/status",
		Params: mustJSON(t, map[string]any{"status": map[string]string{"type": "started"}}),
	}
	h.translateACPEvent("c1", msg)

	types := extractTypes(t, h.sse.replayBuf.Events()[before:])
	wantSubset(t, types, "compaction_started")
}

func TestTranslateCompact_CompletedPersistsEvent(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	summary := "summary text"

	msg := &api.RPCResponse{
		Method: "_kiro.dev/compaction/status",
		Params: mustJSON(t, map[string]any{
			"status":  map[string]string{"type": "completed"},
			"summary": summary,
		}),
	}
	h.translateACPEvent("c1", msg)

	chat, _ := cs.Get(context.Background(), "c1")
	if len(chat.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(chat.Messages))
	}
	if chat.Messages[0].EventKind != api.EventCompacted {
		t.Errorf("event_kind = %q", chat.Messages[0].EventKind)
	}
	if chat.Messages[0].Content != summary {
		t.Errorf("content = %q", chat.Messages[0].Content)
	}
}

// --- Crew ---

func TestTranslateCrew_FirstSnapshotCreatesMessage(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	msg := &api.RPCResponse{
		Method: "_kiro.dev/subagent/list_update",
		Params: mustJSON(t, map[string]any{
			"subagents": []map[string]any{{
				"sessionId": "s1", "sessionName": "sub1",
				"agentName": "kiro_default", "initialQuery": "do a thing",
				"status": map[string]string{"type": "working", "message": "running"},
				"group":  "crew-foo", "role": "kiro_default",
			}},
		}),
	}
	h.translateACPEvent("c1", msg)

	chat, _ := cs.Get(context.Background(), "c1")
	if len(chat.Messages) != 1 {
		t.Fatalf("messages = %d", len(chat.Messages))
	}
	m := chat.Messages[0]
	if m.EventKind != api.EventCrew || m.Crew == nil || m.Crew.Group != "crew-foo" {
		t.Errorf("message = %+v", m)
	}
	if len(m.Crew.Subagents) != 1 || m.Crew.Subagents[0].SessionID != "s1" {
		t.Errorf("subagents = %+v", m.Crew.Subagents)
	}
}

func TestTranslateCrew_IdenticalSnapshotDeduplicates(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	snap := mustJSON(t, map[string]any{
		"subagents": []map[string]any{{
			"sessionId": "s1", "sessionName": "sub1",
			"agentName": "kiro_default", "initialQuery": "q",
			"status": map[string]string{"type": "working"},
			"group":  "g",
		}},
	})
	msg := &api.RPCResponse{Method: "_kiro.dev/subagent/list_update", Params: snap}

	h.translateACPEvent("c1", msg)
	firstCount := len(cs.chats["c1"].Messages)

	// Same snapshot again: must not append or emit anything.
	before := h.sse.replayBuf.Len()
	h.translateACPEvent("c1", msg)
	if len(cs.chats["c1"].Messages) != firstCount {
		t.Error("duplicate snapshot caused a second message append")
	}
	if h.sse.replayBuf.Len() != before {
		t.Errorf("duplicate snapshot emitted %d new events", h.sse.replayBuf.Len()-before)
	}
}

func TestTranslateCrew_StatusChangeUpdatesInPlace(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	msg1 := &api.RPCResponse{
		Method: "_kiro.dev/subagent/list_update",
		Params: mustJSON(t, map[string]any{
			"subagents": []map[string]any{{
				"sessionId": "s1", "group": "g",
				"status": map[string]string{"type": "working"},
			}},
		}),
	}
	h.translateACPEvent("c1", msg1)

	// Change status: same group key → in-place update, no new message.
	msg2 := &api.RPCResponse{
		Method: "_kiro.dev/subagent/list_update",
		Params: mustJSON(t, map[string]any{
			"subagents": []map[string]any{{
				"sessionId": "s1", "group": "g",
				"status": map[string]string{"type": "terminated"},
			}},
		}),
	}
	h.translateACPEvent("c1", msg2)

	chat, _ := cs.Get(context.Background(), "c1")
	if len(chat.Messages) != 1 {
		t.Fatalf("expected 1 message after status change, got %d", len(chat.Messages))
	}
	if chat.Messages[0].Crew == nil ||
		len(chat.Messages[0].Crew.Subagents) == 0 ||
		chat.Messages[0].Crew.Subagents[0].Status != api.CrewTerminated {
		t.Errorf("message not updated in place: %+v", chat.Messages[0].Crew)
	}
}

func TestTranslateCrew_EmptySnapshotIgnored(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	msg := &api.RPCResponse{
		Method: "_kiro.dev/subagent/list_update",
		Params: mustJSON(t, map[string]any{"subagents": []map[string]any{}}),
	}
	h.translateACPEvent("c1", msg)

	chat, _ := cs.Get(context.Background(), "c1")
	if len(chat.Messages) != 0 {
		t.Errorf("empty snapshot created %d messages", len(chat.Messages))
	}
}

func TestIsSubagentNoiseTitle(t *testing.T) {
	tests := []struct {
		title string
		want  bool
	}{
		{"subagent", true},
		{"summary", true},
		{"Summarizing", true},
		{"Spawning agent crew", true},
		{"readFile", false},
		{"write", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := translate.IsSubagentNoiseTitle(tc.title); got != tc.want {
			t.Errorf("IsSubagentNoiseTitle(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
}

// --- Crew cache ---

func TestCrewCache_LookupAfterAppend(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	crew := &api.Crew{Group: "g-1"}
	h.translator.PersistCrew(context.Background(), "c1", crew)

	id, ok := h.translator.LookupCrewCache("c1", "g-1")
	if !ok || id == "" {
		t.Fatalf("cache lookup miss after persist: id=%q ok=%v", id, ok)
	}
}

func TestCrewCache_ClearedOnChatDelete(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	h.translator.PersistCrew(context.Background(), "c1", &api.Crew{Group: "g-1"})

	rec := postCmd(t, h, api.ClientCommand{
		Type: "delete_chat", ChatID: "c1", RequestID: "r-1",
	})
	if rec.Code != 200 {
		t.Fatalf("delete_chat status = %d", rec.Code)
	}

	if _, ok := h.translator.LookupCrewCache("c1", "g-1"); ok {
		t.Error("crew cache not cleared after chat delete")
	}
}

// --- Init errors ---

func TestTranslateInitErrors_AgentNotFoundPersistsFallback(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Agent = "nonexistent"
		return true
	})
	before := h.sse.replayBuf.Len()
	msg := &api.RPCResponse{
		Method: "_kiro.dev/agent/not_found",
		Params: mustJSON(t, map[string]any{
			"requestedAgent": "nonexistent",
			"fallbackAgent":  "kiro_default",
		}),
	}
	h.translateACPEvent("c1", msg)

	c, _ := cs.Get(context.Background(), "c1")
	if c.Agent != "kiro_default" {
		t.Errorf("agent = %q, want kiro_default", c.Agent)
	}
	types := extractTypes(t, h.sse.replayBuf.Events()[before:])
	wantSubset(t, types, "error")
}

func TestTranslateInitErrors_ModelNotFoundPersistsFallback(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "old-model"
		return true
	})
	msg := &api.RPCResponse{
		Method: "_kiro.dev/model/not_found",
		Params: mustJSON(t, map[string]any{
			"requestedModel": "old-model",
			"fallbackModel":  "claude-sonnet",
		}),
	}
	h.translateACPEvent("c1", msg)

	c, _ := cs.Get(context.Background(), "c1")
	if c.Model != "claude-sonnet" {
		t.Errorf("model = %q, want claude-sonnet", c.Model)
	}
}

func TestTranslateInitErrors_AgentConfigErrorEmitsError(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	before := h.sse.replayBuf.Len()
	msg := &api.RPCResponse{
		Method: "_kiro.dev/agent/config_error",
		Params: mustJSON(t, map[string]any{
			"path":  "/home/user/.kiro/agents/broken.md",
			"error": "invalid YAML frontmatter",
		}),
	}
	h.translateACPEvent("c1", msg)
	types := extractTypes(t, h.sse.replayBuf.Events()[before:])
	wantSubset(t, types, "error")
}

func TestTranslateInitErrors_RateLimitEmitsError(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	before := h.sse.replayBuf.Len()
	msg := &api.RPCResponse{
		Method: "_kiro.dev/error/rate_limit",
		Params: mustJSON(t, map[string]any{
			"message": "Rate limit exceeded, try again in 30s",
		}),
	}
	h.translateACPEvent("c1", msg)
	types := extractTypes(t, h.sse.replayBuf.Events()[before:])
	wantSubset(t, types, "error")
}

// --- Agent switched ---

func TestTranslateAgentSwitched_PersistsEventAndUpdatesChat(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Agent = "kiro_default"
		c.Model = "old-model"
		return true
	})
	msg := &api.RPCResponse{
		Method: "_kiro.dev/agent/switched",
		Params: mustJSON(t, map[string]any{
			"agentName":         "kiro_planner",
			"previousAgentName": "kiro_default",
			"welcomeMessage":    "Ready to plan!",
			"model":             "claude-opus",
		}),
	}
	h.translateACPEvent("c1", msg)

	c, _ := cs.Get(context.Background(), "c1")
	if c.Agent != "kiro_planner" {
		t.Errorf("agent = %q, want kiro_planner", c.Agent)
	}
	if c.Model != "claude-opus" {
		t.Errorf("model = %q, want claude-opus", c.Model)
	}
	if len(c.Messages) != 1 || c.Messages[0].EventKind != api.EventAgentSwitched {
		t.Errorf("messages = %+v", c.Messages)
	}
	if c.Messages[0].Content != "Ready to plan!" {
		t.Errorf("content = %q", c.Messages[0].Content)
	}
}

// --- Compaction failed ---

func TestTranslateCompact_FailedPersistsEventAndEmitsError(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	before := h.sse.replayBuf.Len()
	msg := &api.RPCResponse{
		Method: "_kiro.dev/compaction/status",
		Params: mustJSON(t, map[string]any{
			"status": map[string]string{"type": "failed", "error": "context too large"},
		}),
	}
	h.translateACPEvent("c1", msg)

	c, _ := cs.Get(context.Background(), "c1")
	if len(c.Messages) != 1 || c.Messages[0].EventKind != api.EventCompactFailed {
		t.Fatalf("messages = %+v", c.Messages)
	}
	if c.Messages[0].Content != "context too large" {
		t.Errorf("content = %q", c.Messages[0].Content)
	}
	types := extractTypes(t, h.sse.replayBuf.Events()[before:])
	wantSubset(t, types, "error")
}

// --- Clear status (noop) ---

func TestTranslateClearStatus_NoopNoEvents(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	before := h.sse.replayBuf.Len()
	msg := &api.RPCResponse{
		Method: "_kiro.dev/clear/status",
		Params: mustJSON(t, map[string]any{}),
	}
	h.translateACPEvent("c1", msg)
	if h.sse.replayBuf.Len() != before {
		t.Errorf("clear/status emitted %d events, want 0", h.sse.replayBuf.Len()-before)
	}
}
