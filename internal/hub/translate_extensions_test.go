package hub

// Tests for the v3 (KAS) _kiro/* translate handlers dispatched through
// translateACPEvent:
//   - _kiro/mcp/status            (translate/v3_notifications.go)
//   - _kiro/customAgent/*         (translate/init_errors.go)
//   - _kiro/error/rate_limit      (translate/init_errors.go)
//   - _kiro/system/notify         (translate/init_errors.go)
//   - session/update sub-kinds    (translate/v3_updates.go)
//
// Shared fixtures live in shared_test.go.

import (
	"context"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// --- MCP (v3 consolidated status) ---

func TestTranslateMCPStatus(t *testing.T) {
	tests := []struct {
		name       string
		params     any
		wantSnap   func(t *testing.T, snap []mcpServerRuntime)
		wantEvents []string
	}{
		{
			name:   "ConnectedRecordsServer",
			params: map[string]any{"servers": []map[string]any{{"name": "github", "status": "connected"}}},
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
			name:   "FailedRecordsError",
			params: map[string]any{"servers": []map[string]any{{"name": "broken", "status": "failed", "errorMessage": "connection refused"}}},
			wantSnap: func(t *testing.T, snap []mcpServerRuntime) {
				t.Helper()
				if len(snap) != 1 || snap[0].State != mcpStateFailed {
					t.Fatalf("snapshot = %+v", snap)
				}
				if snap[0].Error != "connection refused" {
					t.Errorf("error = %q", snap[0].Error)
				}
			},
			wantEvents: []string{"mcp_failed"},
		},
		{
			name:   "FailedWithAuthURLEmitsOAuth",
			params: map[string]any{"servers": []map[string]any{{"name": "linear", "status": "failed", "authorizationUrl": "https://oauth.example/authorize"}}},
			wantSnap: func(t *testing.T, snap []mcpServerRuntime) {
				t.Helper()
				if len(snap) != 1 || snap[0].OAuthURL != "https://oauth.example/authorize" {
					t.Errorf("snapshot = %+v", snap)
				}
			},
			wantEvents: []string{"mcp_oauth_needed"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			before := h.sse.replayBuf.Len()
			msg := &api.RPCResponse{Method: "_kiro/mcp/status", Params: mustJSON(t, tc.params)}
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

// --- Available commands (v3 session/update sub-kind) ---

func TestTranslateV3_AvailableCommandsUpdate(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	before := h.sse.replayBuf.Len()

	msg := &api.RPCResponse{
		Method: api.MethodSessionUpdate,
		Params: mustJSON(t, map[string]any{
			"update": map[string]any{
				"sessionUpdate": "available_commands_update",
				"availableCommands": []map[string]any{
					{"name": "/help", "description": "Show help"},
				},
			},
		}),
	}
	h.translateACPEvent("c1", msg)

	types := extractTypes(t, h.sse.replayBuf.Events()[before:])
	wantSubset(t, types, "commands_updated")
}

// --- Compaction (v3 session_info_update summarization) ---

func TestTranslateV3_SummarizationRunningEmitsTransient(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	before := h.sse.replayBuf.Len()

	msg := &api.RPCResponse{
		Method: api.MethodSessionUpdate,
		Params: mustJSON(t, map[string]any{
			"update": map[string]any{
				"sessionUpdate": "session_info_update",
				"_meta":         map[string]any{"kiro": map[string]any{"summarization": map[string]any{"status": "running"}}},
			},
		}),
	}
	h.translateACPEvent("c1", msg)

	types := extractTypes(t, h.sse.replayBuf.Events()[before:])
	wantSubset(t, types, "compaction_started")
}

func TestTranslateV3_SummarizationSuccessPersistsEvent(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	summary := "summary text"

	msg := &api.RPCResponse{
		Method: api.MethodSessionUpdate,
		Params: mustJSON(t, map[string]any{
			"update": map[string]any{
				"sessionUpdate": "session_info_update",
				"_meta": map[string]any{"kiro": map[string]any{"summarization": map[string]any{
					"status":  "success",
					"summary": map[string]any{"conversationSummary": summary},
				}}},
			},
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

// --- Usage (v3 usage_update sub-kind) ---

func TestTranslateV3_UsageUpdatePersistsContextPct(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	msg := &api.RPCResponse{
		Method: api.MethodSessionUpdate,
		Params: mustJSON(t, map[string]any{
			"update": map[string]any{
				"sessionUpdate": "usage_update",
				"size":          1000,
				"used":          250,
			},
		}),
	}
	h.translateACPEvent("c1", msg)

	chat, _ := cs.Get(context.Background(), "c1")
	if chat.Usage.ContextPct != 25 {
		t.Errorf("context_pct = %v, want 25", chat.Usage.ContextPct)
	}
}

// --- Init errors (v3 _kiro/customAgent/* + _kiro/error/rate_limit) ---

func TestTranslateInitErrors_AgentNotFoundPersistsFallback(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.CurrentModeID = "nonexistent"
		return true
	})
	before := h.sse.replayBuf.Len()
	msg := &api.RPCResponse{
		Method: "_kiro/customAgent/not_found",
		Params: mustJSON(t, map[string]any{
			"requestedAgent": "nonexistent",
			"fallbackAgent":  "vibe",
		}),
	}
	h.translateACPEvent("c1", msg)

	c, _ := cs.Get(context.Background(), "c1")
	if c.CurrentModeID != "vibe" {
		t.Errorf("current_mode_id = %q, want vibe", c.CurrentModeID)
	}
	types := extractTypes(t, h.sse.replayBuf.Events()[before:])
	wantSubset(t, types, "error")
}

func TestTranslateInitErrors_AgentConfigErrorEmitsError(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	before := h.sse.replayBuf.Len()
	msg := &api.RPCResponse{
		Method: "_kiro/customAgent/config_error",
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
		Method: "_kiro/error/rate_limit",
		Params: mustJSON(t, map[string]any{
			"message": "Rate limit exceeded, try again in 30s",
		}),
	}
	h.translateACPEvent("c1", msg)
	types := extractTypes(t, h.sse.replayBuf.Events()[before:])
	wantSubset(t, types, "error")
}

// --- System notify (v3 replacement for session/retry) ---

func TestTranslateSystemNotify_EmitsError(t *testing.T) {
	h, _, _ := newTestHub()
	before := h.sse.replayBuf.Len()
	// No sessionId on _kiro/system/notify — broadcast at bridge scope.
	msg := &api.RPCResponse{
		Method: "_kiro/system/notify",
		Params: mustJSON(t, map[string]any{
			"level":   "warning",
			"message": "The selected model is experiencing high load.",
		}),
	}
	h.translateACPEvent("", msg)
	types := extractTypes(t, h.sse.replayBuf.Events()[before:])
	wantSubset(t, types, "error")
}
