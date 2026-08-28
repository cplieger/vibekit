package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
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
			_ = cs.Mutate(b.Context(), "bench", func(c *vibekit.Chat, _ bool) bool {
				c.Name = "bench"
				return true
			})
			b.ResetTimer()
			b.ReportAllocs()
			for b.Loop() {
				h.translator.HandleAssistantChunk(b.Context(), "bench", tc.raw, tc.isReasoning)
			}
		})
	}
}

// Two plan frames for one turn leave ONE row, carrying the newest entries.
//
// ACP resends the whole entries array per update, so an append per frame left N
// snapshots of one plan in the transcript and the client drew N cards, each stuck
// at a different stage. The id is minted per frame and the store discards it on
// the update path, so the surviving row keeps the FIRST frame's id — which is what
// lets the client merge by id instead of mounting a second card.
func TestHandlePlan_OneRowPerTurnCarryingTheNewestEntries(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	pending := json.RawMessage(`{"entries":[{"content":"step 1","priority":"high","status":"pending"},{"content":"step 2","priority":"medium","status":"pending"}]}`)
	h.translator.HandlePlan(t.Context(), "c1", pending)

	c, _ := cs.Get(t.Context(), "c1")
	if len(c.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(c.Messages))
	}
	firstID := c.Messages[0].ID
	if len(c.Messages[0].Plan) != 2 {
		t.Errorf("plan entries = %d, want 2", len(c.Messages[0].Plan))
	}

	done := json.RawMessage(`{"entries":[{"content":"step 1","priority":"high","status":"completed"},{"content":"step 2","priority":"medium","status":"completed"}]}`)
	h.translator.HandlePlan(t.Context(), "c1", done)

	c, _ = cs.Get(t.Context(), "c1")
	if len(c.Messages) != 1 {
		t.Fatalf("messages = %d after a second plan frame, want 1: a plan update must overwrite the turn's row, not append a second", len(c.Messages))
	}
	if got := c.Messages[0].ID; got != firstID {
		t.Errorf("plan row id = %q, want the first frame's %q; the client merges by id, so a new id mounts a second card", got, firstID)
	}
	for i, e := range c.Messages[0].Plan {
		if e.Status != vibekit.PlanCompleted {
			t.Errorf("entry %d status = %q, want %q: the row must carry the NEWEST entries", i, e.Status, vibekit.PlanCompleted)
		}
	}
}

// A user message opens a turn, so a plan after one is that turn's first plan and
// appends rather than overwriting the previous turn's row. Without the boundary
// every plan in a chat would fold onto the first one ever recorded.
func TestHandlePlan_AUserMessageStartsANewTurnsPlan(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	first := json.RawMessage(`{"entries":[{"content":"turn one","priority":"high","status":"pending"}]}`)
	h.translator.HandlePlan(t.Context(), "c1", first)

	_ = cs.AppendMessage(t.Context(), "c1", &vibekit.Message{
		ID: "m-user", Role: vibekit.RoleUser, Content: "next thing please",
	})

	second := json.RawMessage(`{"entries":[{"content":"turn two","priority":"high","status":"pending"}]}`)
	h.translator.HandlePlan(t.Context(), "c1", second)

	c, _ := cs.Get(t.Context(), "c1")
	if len(c.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (plan, user, plan)", len(c.Messages))
	}
	if got := c.Messages[0].Plan[0].Content; got != "turn one" {
		t.Errorf("first turn's plan = %q, want %q: a later turn must not overwrite it", got, "turn one")
	}
	if got := c.Messages[2].Plan[0].Content; got != "turn two" {
		t.Errorf("second turn's plan = %q, want %q", got, "turn two")
	}
}

func TestHandleModeUpdate_BroadcastsOnlyOnChange(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.CurrentModeID = "code"
		return true
	})

	_, before := h.bus.fanout.Bounds()

	// Same mode → no broadcast. KAS's current_mode_update keys the new
	// mode on currentModeId (not modeId — that is the outbound set_mode
	// request's field).
	raw := json.RawMessage(`{"currentModeId":"code"}`)
	h.translator.HandleModeUpdate(t.Context(), "c1", raw)
	if _, head := h.bus.fanout.Bounds(); head != before {
		t.Errorf("expected no broadcast for same mode")
	}

	// Different mode → broadcast.
	raw2 := json.RawMessage(`{"currentModeId":"chat"}`)
	h.translator.HandleModeUpdate(t.Context(), "c1", raw2)
	if _, head := h.bus.fanout.Bounds(); head == before {
		t.Error("expected broadcast for mode change, got none")
	}

	c, _ := cs.Get(t.Context(), "c1")
	if c.CurrentModeID != "chat" {
		t.Errorf("mode = %q, want chat", c.CurrentModeID)
	}
}
