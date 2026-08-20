package buffer

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestBufferSnapshot covers the read that replaced agent/turn_mirror.go's parallel
// replica of the in-flight turn.
//
// The mirror re-folded every broadcast event into its own vibekit.Message — a second
// implementation of the block assembly this package already does, free to drift
// from it. These cases pin that the buffer's own snapshot carries what the
// connect-time turn_state needs.
func TestBufferSnapshot(t *testing.T) {
	t.Run("an unstarted turn has no snapshot", func(t *testing.T) {
		var buf Buffer
		if _, _, ok := buf.Snapshot(); ok {
			t.Error("snapshot reported content for a buffer with no message id")
		}
	})

	t.Run("a started but silent turn is a bare busy signal", func(t *testing.T) {
		buf := Buffer{MessageID: "m1"}
		msg, seq, ok := buf.Snapshot()
		if ok {
			t.Errorf("snapshot reported content for an empty turn: %+v", msg)
		}
		if seq != 0 {
			t.Errorf("chunk seq = %d, want 0", seq)
		}
	})

	t.Run("text, thinking and tools all reach the snapshot", func(t *testing.T) {
		// Mirrors translate/streaming_content.go: the Append*Delta calls build
		// the Blocks array, and the caller writes Content/Reasoning alongside.
		// Those two are a second representation the turn-commit path and the
		// 32 MiB cap both read, so a snapshot has to carry them.
		buf := Buffer{MessageID: "m1"}
		buf.AppendTextDelta("hello ", "")
		buf.Content.WriteString("hello ")
		buf.AppendThinkingDelta("pondering", "")
		buf.Reasoning.WriteString("pondering")
		buf.AppendToolUseBlock("tool-1", "")
		buf.ToolCalls = append(buf.ToolCalls, vibekit.ToolCall{ID: "tool-1", Title: "Read File"})
		buf.AppendTextDelta("world", "")
		buf.Content.WriteString("world")

		msg, seq, ok := buf.Snapshot()
		if !ok {
			t.Fatal("snapshot reported no content")
		}
		if msg.ID != "m1" {
			t.Errorf("id = %q, want m1", msg.ID)
		}
		if msg.Role != vibekit.RoleAssistant {
			t.Errorf("role = %q, want assistant", msg.Role)
		}
		if msg.Content != "hello world" {
			t.Errorf("content = %q, want %q", msg.Content, "hello world")
		}
		if msg.Reasoning != "pondering" {
			t.Errorf("reasoning = %q, want %q", msg.Reasoning, "pondering")
		}
		if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "tool-1" {
			t.Errorf("tool calls = %+v, want one tool-1", msg.ToolCalls)
		}
		// Blocks are the chronological order the client renders from: text,
		// thinking, tool_use, then text again as a NEW block (the tool broke
		// the run).
		if len(msg.Blocks) != 4 {
			kinds := make([]string, 0, len(msg.Blocks))
			for _, b := range msg.Blocks {
				kinds = append(kinds, string(b.Type))
			}
			t.Errorf("blocks = %v, want 4 (text, thinking, tool_use, text)", kinds)
		}
		// Two text deltas were counted; the thinking delta counts too.
		if seq == 0 {
			t.Error("chunk seq = 0, want the delta count so a client can drop folded-in chunks")
		}
	})

	t.Run("slices are copied, not aliased", func(t *testing.T) {
		// The caller reads the snapshot off the SSE goroutine while the
		// dispatch loop keeps appending. Handing out the live backing array
		// would be a data race the -race gate cannot always catch.
		buf := Buffer{MessageID: "m1"}
		buf.AppendTextDelta("hi", "")
		buf.Content.WriteString("hi")
		buf.ToolCalls = append(buf.ToolCalls, vibekit.ToolCall{ID: "tool-1"})

		msg, _, ok := buf.Snapshot()
		if !ok {
			t.Fatal("snapshot reported no content")
		}
		// Mutate IN PLACE first. Appending would reallocate (len == cap here)
		// and leave the aliased array untouched, so an append-then-check makes
		// this test vacuous — it passed against a deliberately aliasing
		// implementation before this was fixed.
		buf.ToolCalls[0].Title = "mutated"
		if msg.ToolCalls[0].Title == "mutated" {
			t.Error("snapshot aliases the buffer's tool-call array")
		}
		buf.Blocks[0].Text = "mutated"
		if msg.Blocks[0].Text == "mutated" {
			t.Error("snapshot aliases the buffer's block array")
		}
	})
}
