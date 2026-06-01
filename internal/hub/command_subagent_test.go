package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"vibekit/internal/api"
)

// TestSubagentCommand_ValidationRejections consolidates the 7 individual
// validation tests that share the pattern: post a command with a
// missing/invalid field, assert HTTP 400.
func TestSubagentCommand_ValidationRejections(t *testing.T) {
	tests := []struct {
		payload   any
		name      string
		cmdType   api.CommandType
		chatID    api.ChatID
		wantCode  int
		needsChat bool
	}{
		{
			name:    "spawn_requires_chat_id",
			cmdType: "spawn_subagent",
			payload: api.SpawnSubagentCommand{Task: "do stuff"},
		},
		{
			name:      "spawn_requires_task",
			cmdType:   "spawn_subagent",
			chatID:    "c1",
			payload:   api.SpawnSubagentCommand{},
			needsChat: true,
		},
		{
			name:      "message_requires_fields",
			cmdType:   "message_subagent",
			chatID:    "c1",
			payload:   api.MessageSubagentCommand{},
			needsChat: true,
		},
		{
			name:    "set_auto_approve_crew_requires_chat_id",
			cmdType: "set_auto_approve_crew",
			payload: map[string]bool{"enabled": true},
		},
		{
			name:      "terminate_requires_sub_session_id",
			cmdType:   "terminate_subagent",
			chatID:    "c1",
			payload:   map[string]string{},
			needsChat: true,
		},
		{
			name:    "attach_requires_chat_id",
			cmdType: "attach_subagent",
			payload: map[string]string{"sub_session_id": "sub-1"},
		},
		{
			name:      "attach_requires_sub_session_id",
			cmdType:   "attach_subagent",
			chatID:    "c1",
			payload:   map[string]string{},
			needsChat: true,
		},
		{
			name:      "spawn_invalid_payload",
			cmdType:   "spawn_subagent",
			chatID:    "c1",
			needsChat: true,
			wantCode:  http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			if tt.needsChat {
				_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
			}

			var payload json.RawMessage
			if tt.payload != nil {
				payload = mustJSON(t, tt.payload)
			} else {
				payload = json.RawMessage(`{bad`)
			}

			wantCode := tt.wantCode
			if wantCode == 0 {
				wantCode = http.StatusBadRequest
			}

			rec := postCmd(t, h, api.ClientCommand{
				Type:      tt.cmdType,
				RequestID: "r1",
				ChatID:    tt.chatID,
				Payload:   payload,
			})
			if rec.Code != wantCode {
				t.Errorf("status = %d, want %d", rec.Code, wantCode)
			}
		})
	}
}

// TestSubagentCommand_NoBridgeReturnsConflict consolidates the tests
// that verify a 409 Conflict when a chat exists but has no bridge.
func TestSubagentCommand_NoBridgeReturnsConflict(t *testing.T) {
	tests := []struct {
		payload any
		name    string
		cmdType api.CommandType
	}{
		{
			name:    "spawn_no_bridge",
			cmdType: "spawn_subagent",
			payload: api.SpawnSubagentCommand{Task: "do stuff"},
		},
		{
			name:    "attach_no_bridge",
			cmdType: "attach_subagent",
			payload: map[string]string{"sub_session_id": "sub-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
			rec := postCmd(t, h, api.ClientCommand{
				Type:      tt.cmdType,
				RequestID: "r1",
				ChatID:    "c1",
				Payload:   mustJSON(t, tt.payload),
			})
			if rec.Code != http.StatusConflict {
				t.Errorf("status = %d, want 409", rec.Code)
			}
		})
	}
}

func TestListSessions_NoBridgeReturnsEmpty(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	rec := postCmd(t, h, api.ClientCommand{
		Type:      "list_sessions",
		RequestID: "r1",
		ChatID:    "c1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if string(resp["sessions"]) != "[]" {
		t.Errorf("sessions = %s, want []", resp["sessions"])
	}
}

// --- Subagent happy-path coverage (F5, F6) ---

func TestAttachSubagent_HappyPathCallsBridge(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	sb, err := h.coord.GetOrCreateBridge(context.Background(), "c1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	fb := sb.bridge.(*fakeBridge)
	fb.mu.Lock()
	fb.calls = nil
	fb.mu.Unlock()

	rec := postCmd(t, h, api.ClientCommand{
		Type: "attach_subagent", RequestID: "r1", ChatID: "c1",
		Payload: mustJSON(t, map[string]string{"sub_session_id": "sub-42"}),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	fb.mu.Lock()
	calls := append([]string(nil), fb.calls...)
	fb.mu.Unlock()
	if len(calls) != 1 || calls[0] != "session/attach" {
		t.Errorf("bridge calls = %v, want [session/attach]", calls)
	}
}

func TestTerminateSubagent_HappyPathCallsBridge(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	sb, err := h.coord.GetOrCreateBridge(context.Background(), "c1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	fb := sb.bridge.(*fakeBridge)
	fb.mu.Lock()
	fb.calls = nil
	fb.mu.Unlock()

	rec := postCmd(t, h, api.ClientCommand{
		Type: "terminate_subagent", RequestID: "r1", ChatID: "c1",
		Payload: mustJSON(t, map[string]string{"sub_session_id": "sub-42"}),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	fb.mu.Lock()
	calls := append([]string(nil), fb.calls...)
	fb.mu.Unlock()
	if len(calls) != 1 || calls[0] != "session/terminate" {
		t.Errorf("bridge calls = %v, want [session/terminate]", calls)
	}
}

func TestSpawnSubagent_HappyPath(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	sb, err := h.coord.GetOrCreateBridge(context.Background(), "c1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	fb := sb.bridge.(*fakeBridge)
	fb.mu.Lock()
	fb.calls = nil
	fb.mu.Unlock()

	rec := postCmd(t, h, api.ClientCommand{
		Type: "spawn_subagent", RequestID: "r1", ChatID: "c1",
		Payload: mustJSON(t, api.SpawnSubagentCommand{Task: "investigate", Name: "worker"}),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	fb.mu.Lock()
	calls := append([]string(nil), fb.calls...)
	fb.mu.Unlock()
	if len(calls) == 0 || calls[len(calls)-1] != "session/spawn" {
		t.Errorf("last bridge call = %v, want session/spawn", calls)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["ok"] != true {
		t.Errorf("resp.ok = %v, want true", resp["ok"])
	}
	if _, exists := resp["session_id"]; !exists {
		t.Error("resp missing session_id")
	}
}

func TestMessageSubagent_HappyPath(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	sb, err := h.coord.GetOrCreateBridge(context.Background(), "c1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	fb := sb.bridge.(*fakeBridge)
	fb.mu.Lock()
	fb.calls = nil
	fb.mu.Unlock()

	rec := postCmd(t, h, api.ClientCommand{
		Type: "message_subagent", RequestID: "r1", ChatID: "c1",
		Payload: mustJSON(t, api.MessageSubagentCommand{
			SubSessionID: "sub-42", Text: "status?",
		}),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	fb.mu.Lock()
	calls := append([]string(nil), fb.calls...)
	fb.mu.Unlock()
	if !slices.Contains(calls, "message/send") {
		t.Errorf("message/send not called; calls = %v", calls)
	}
}

// --- cmdSetAutoApproveCrew ---

func TestSetAutoApproveCrew_TogglesFlag(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	rec := postCmd(t, h, api.ClientCommand{
		Type:      "set_auto_approve_crew",
		RequestID: "r1",
		ChatID:    "c1",
		Payload:   mustJSON(t, map[string]bool{"enabled": true}),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	chat, _ := cs.Get(context.Background(), "c1")
	if !chat.AutoApproveCrew {
		t.Error("AutoApproveCrew not set")
	}
}

func TestSetAutoApproveCrew_Missing404(t *testing.T) {
	h, _, _ := newTestHub()
	rec := postCmd(t, h, api.ClientCommand{
		Type: "set_auto_approve_crew", RequestID: "r1", ChatID: "missing",
		Payload: mustJSON(t, map[string]bool{"enabled": true}),
	})
	if rec.Code != http.StatusNotFound {
		t.Errorf("code=%d, want 404", rec.Code)
	}
}

func TestSetAutoApproveCrew_DisableAfterEnable(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	reqIDs := []string{"r1", "r2", "r3"}
	for i, want := range []bool{true, false, false} {
		rec := postCmd(t, h, api.ClientCommand{
			Type: "set_auto_approve_crew", RequestID: reqIDs[i], ChatID: "c1",
			Payload: mustJSON(t, map[string]bool{"enabled": want}),
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("want=%v status=%d body=%s", want, rec.Code, rec.Body.String())
		}
	}
	c, _ := cs.Get(context.Background(), "c1")
	if c.AutoApproveCrew {
		t.Error("AutoApproveCrew should be false after disable")
	}
}
