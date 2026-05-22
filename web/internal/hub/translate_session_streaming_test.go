package hub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"vibekit/internal/api"
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
			_ = cs.Mutate(context.Background(), "bench", func(c *api.Chat, _ bool) bool {
				c.Name = "bench"
				return true
			})
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				h.handleAssistantChunk(context.Background(), "bench", tc.raw, tc.isReasoning)
			}
		})
	}
}

func TestHandlePlan_PersistsAndClearsOnAllDone(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	// Plan with pending entries → persists as message and sets CurrentPlan.
	raw := json.RawMessage(`{"entries":[{"content":"step 1","priority":"high","status":"pending"},{"content":"step 2","priority":"medium","status":"pending"}]}`)
	h.handlePlan(context.Background(), "c1", raw)

	c, _ := cs.Get(context.Background(), "c1")
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
	h.handlePlan(context.Background(), "c1", rawDone)

	c, _ = cs.Get(context.Background(), "c1")
	if len(c.CurrentPlan) != 0 {
		t.Errorf("current_plan should be cleared when all done, got %d entries", len(c.CurrentPlan))
	}
}

func TestHandleModeUpdate_BroadcastsOnlyOnChange(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.CurrentModeID = "code"
		return true
	})

	before := h.sse.replayBuf.Len()

	// Same mode → no broadcast.
	raw := json.RawMessage(`{"modeId":"code"}`)
	h.handleModeUpdate(context.Background(), "c1", raw)
	if h.sse.replayBuf.Len() != before {
		t.Errorf("expected no broadcast for same mode, got %d new events", h.sse.replayBuf.Len()-before)
	}

	// Different mode → broadcast.
	raw2 := json.RawMessage(`{"modeId":"chat"}`)
	h.handleModeUpdate(context.Background(), "c1", raw2)
	if h.sse.replayBuf.Len() == before {
		t.Error("expected broadcast for mode change, got none")
	}

	c, _ := cs.Get(context.Background(), "c1")
	if c.CurrentModeID != "chat" {
		t.Errorf("mode = %q, want chat", c.CurrentModeID)
	}
}

func TestHandleSteeringInclusion_ExtractsNames(t *testing.T) {
	h, _, _ := newTestHub()

	before := h.sse.replayBuf.Len()
	raw := json.RawMessage(`{"documents":[{"name":"tech.md","path":"/steering/tech.md"},{"name":"","path":"/steering/go.md"}]}`)
	h.handleSteeringInclusion(context.Background(), "c1", raw)

	events := h.sse.replayBuf.Events()[before:]
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	var evt struct {
		Type    string `json:"type"`
		Payload struct {
			Documents []string `json:"documents"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(events[0].data, &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if evt.Type != string(api.EventSteeringLoaded) {
		t.Errorf("event type = %q, want steering_loaded", evt.Type)
	}
	if len(evt.Payload.Documents) != 2 || evt.Payload.Documents[0] != "tech.md" || evt.Payload.Documents[1] != "/steering/go.md" {
		t.Errorf("documents = %v", evt.Payload.Documents)
	}
}
