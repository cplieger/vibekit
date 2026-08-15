package hub

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

func BenchmarkHandleAssistantChunk(b *testing.B) {
	shortChunk, _ := json.Marshal(map[string]any{
		"content": map[string]any{"type": "text", "text": "Hello wor"},
	})
	longChunk, _ := json.Marshal(map[string]any{
		"content": map[string]any{"type": "text", "text": strings.Repeat("x", 4096)},
	})
	reasoningChunk, _ := json.Marshal(map[string]any{
		"content": map[string]any{"type": "text", "text": "Let me think about this carefully..."},
	})

	cases := []struct {
		name        string
		raw         json.RawMessage
		isReasoning bool
	}{
		{"short_chunk", shortChunk, false},
		{"long_chunk", longChunk, false},
		{"with_reasoning", reasoningChunk, true},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			h, cs, _ := newTestHub()
			_ = cs.Mutate(b.Context(), "bench", func(c *api.Chat, _ bool) bool {
				c.Name = "bench"
				return true
			})
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				h.translator.HandleAssistantChunk(b.Context(), "bench", tc.raw, tc.isReasoning)
			}
		})
	}
}

func TestHandlePlan_PersistsAndClearsOnAllDone(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	// Plan with pending entries → persists as message and sets CurrentPlan.
	raw := json.RawMessage(`{"entries":[{"content":"step 1","priority":"high","status":"pending"},{"content":"step 2","priority":"medium","status":"pending"}]}`)
	h.translator.HandlePlan(t.Context(), "c1", raw)

	c, _ := cs.Get(t.Context(), "c1")
	if len(c.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(c.Messages))
	}
	if len(c.Messages[0].Plan) != 2 {
		t.Errorf("plan entries = %d, want 2", len(c.Messages[0].Plan))
	}
	if len(c.CurrentPlan) != 2 {
		t.Errorf("current_plan = %d, want 2", len(c.CurrentPlan))
	}

	// All entries completed → clears CurrentPlan.
	rawDone := json.RawMessage(`{"entries":[{"content":"step 1","priority":"high","status":"completed"},{"content":"step 2","priority":"medium","status":"completed"}]}`)
	h.translator.HandlePlan(t.Context(), "c1", rawDone)

	c, _ = cs.Get(t.Context(), "c1")
	if len(c.CurrentPlan) != 0 {
		t.Errorf("current_plan should be cleared when all done, got %d entries", len(c.CurrentPlan))
	}
}

func TestHandleModeUpdate_BroadcastsOnlyOnChange(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.CurrentModeID = "code"
		return true
	})

	_, before := h.sse.hub.Bounds()

	// Same mode → no broadcast. KAS's current_mode_update keys the new
	// mode on currentModeId (not modeId — that is the outbound set_mode
	// request's field).
	raw := json.RawMessage(`{"currentModeId":"code"}`)
	h.translator.HandleModeUpdate(t.Context(), "c1", raw)
	if _, head := h.sse.hub.Bounds(); head != before {
		t.Errorf("expected no broadcast for same mode")
	}

	// Different mode → broadcast.
	raw2 := json.RawMessage(`{"currentModeId":"chat"}`)
	h.translator.HandleModeUpdate(t.Context(), "c1", raw2)
	if _, head := h.sse.hub.Bounds(); head == before {
		t.Error("expected broadcast for mode change, got none")
	}

	c, _ := cs.Get(t.Context(), "c1")
	if c.CurrentModeID != "chat" {
		t.Errorf("mode = %q, want chat", c.CurrentModeID)
	}
}
