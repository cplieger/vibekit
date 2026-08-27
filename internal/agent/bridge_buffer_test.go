package agent

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// Tests for bridge_buffer.go: buffer.Buffer lifecycle + emitTurnEndedWithStats
// persistence behaviour. Shared fixtures live in shared_test.go.

func TestTrackFileChanges_Table(t *testing.T) {
	tests := []struct {
		wantFiles map[string]*vibekit.FileChange
		name      string
		diffs     []vibekit.ToolDiff
		isNewFile bool
	}{
		{
			name:      "nil_diffs",
			diffs:     nil,
			wantFiles: nil,
		},
		{
			name:      "empty_path_skipped",
			diffs:     []vibekit.ToolDiff{{Path: "", NewText: "x\n"}},
			wantFiles: map[string]*vibekit.FileChange{},
		},
		{
			name:      "single_create",
			diffs:     []vibekit.ToolDiff{{Path: "a.go", NewText: "line1\nline2\n"}},
			isNewFile: true,
			wantFiles: map[string]*vibekit.FileChange{"a.go": {LinesAdded: 2, IsNewFile: true}},
		},
		{
			name:      "single_edit",
			diffs:     []vibekit.ToolDiff{{Path: "b.go", OldText: "old\n", NewText: "new\nmore\n"}},
			wantFiles: map[string]*vibekit.FileChange{"b.go": {LinesAdded: 2, LinesRemoved: 1}},
		},
		{
			name: "multi_diff_same_path_accumulates",
			diffs: []vibekit.ToolDiff{
				{Path: "c.go", NewText: "a\n"},
				{Path: "c.go", OldText: "x\ny\n", NewText: "z\n"},
			},
			wantFiles: map[string]*vibekit.FileChange{"c.go": {LinesAdded: 2, LinesRemoved: 2}},
		},
		{
			name: "multi_path",
			diffs: []vibekit.ToolDiff{
				{Path: "d.go", NewText: "one\n"},
				{Path: "e.go", OldText: "rm\n"},
			},
			wantFiles: map[string]*vibekit.FileChange{
				"d.go": {LinesAdded: 1},
				"e.go": {LinesRemoved: 1},
			},
		},
		{
			name:      "newText_only_counts_added",
			diffs:     []vibekit.ToolDiff{{Path: "f.go", NewText: "a\nb\nc\n"}},
			wantFiles: map[string]*vibekit.FileChange{"f.go": {LinesAdded: 3}},
		},
		{
			name:      "oldText_only_counts_removed",
			diffs:     []vibekit.ToolDiff{{Path: "g.go", OldText: "x\ny\n"}},
			wantFiles: map[string]*vibekit.FileChange{"g.go": {LinesRemoved: 2}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &buffer.Buffer{}
			buf.TrackFileChanges(tt.diffs, tt.isNewFile)
			if tt.wantFiles == nil {
				if len(buf.ChangedFiles) > 0 {
					t.Fatalf("ChangedFiles = %v, want nil/empty", buf.ChangedFiles)
				}
				return
			}
			if len(buf.ChangedFiles) != len(tt.wantFiles) {
				t.Fatalf("len(ChangedFiles) = %d, want %d", len(buf.ChangedFiles), len(tt.wantFiles))
			}
			for path, want := range tt.wantFiles {
				got, ok := buf.ChangedFiles[path]
				if !ok {
					t.Errorf("missing path %q", path)
					continue
				}
				if got.LinesAdded != want.LinesAdded {
					t.Errorf("%s: LinesAdded = %d, want %d", path, got.LinesAdded, want.LinesAdded)
				}
				if got.LinesRemoved != want.LinesRemoved {
					t.Errorf("%s: LinesRemoved = %d, want %d", path, got.LinesRemoved, want.LinesRemoved)
				}
				if got.IsNewFile != want.IsNewFile {
					t.Errorf("%s: IsNewFile = %v, want %v", path, got.IsNewFile, want.IsNewFile)
				}
			}
		})
	}
}

func TestEmitTurnEnded_PersistsAssistantMessage(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	// Start a turn: one chunk then end.
	h.translateACPEvent("c1", newChunkMsg("finished"))
	h.EmitTurnEndedWithStats(t.Context(), "c1", &vibekit.RPCResponse{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}, command.TurnStats{})

	c, _ := cs.Get(t.Context(), "c1")
	if len(c.Messages) != 1 {
		t.Fatalf("messages = %+v", c.Messages)
	}
	if c.Messages[0].Role != vibekit.RoleAssistant || c.Messages[0].Content != "finished" {
		t.Errorf("message mismatch: %+v", c.Messages[0])
	}
	// Buffer should be cleared.
	if h.bridge.assistantBufs.Get("c1") != nil {
		t.Errorf("buffer not cleared after turn_ended")
	}
}

func TestEmitTurnEnded_CancelledAppendsEventMessage(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	h.EmitTurnEndedWithStats(t.Context(), "c1", &vibekit.RPCResponse{Result: json.RawMessage(`{"stopReason":"cancelled"}`)}, command.TurnStats{})

	c, _ := cs.Get(t.Context(), "c1")
	if len(c.Messages) != 1 || c.Messages[0].Role != vibekit.RoleEvent || c.Messages[0].EventKind != vibekit.EventCancelled {
		t.Errorf("messages = %+v", c.Messages)
	}
}

func TestEmitTurnEnded_NoBufferNoMessagePersisted(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	h.EmitTurnEndedWithStats(t.Context(), "c1", &vibekit.RPCResponse{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}, command.TurnStats{})

	c, _ := cs.Get(t.Context(), "c1")
	if len(c.Messages) != 0 {
		t.Errorf("messages = %+v (expected none)", c.Messages)
	}
}

func TestEmitTurnEnded_CancelledMarksToolsFailed(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	// Simulate a tool call in progress.
	h.translateACPEvent("c1", newToolCallMsg(t, "tc1", "Reading file", "in_progress"))
	// Cancel the turn.
	h.EmitTurnEndedWithStats(t.Context(), "c1", &vibekit.RPCResponse{Result: json.RawMessage(`{"stopReason":"cancelled"}`)}, command.TurnStats{})

	c, _ := cs.Get(t.Context(), "c1")
	// Should have the assistant message + the cancelled event.
	if len(c.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(c.Messages), c.Messages)
	}
	// The assistant message should have the tool call marked as failed.
	assistantMsg := c.Messages[0]
	if len(assistantMsg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(assistantMsg.ToolCalls))
	}
	if assistantMsg.ToolCalls[0].Status != vibekit.ToolFailed {
		t.Errorf("tool status = %q, want %q", assistantMsg.ToolCalls[0].Status, vibekit.ToolFailed)
	}
}

func TestToolStartTimeTracking(t *testing.T) {
	buf := &buffer.Buffer{ToolStartTimes: make(map[string]int64)}
	buf.RecordToolStart("tc1")

	// Small sleep to ensure non-zero duration.
	dur := buf.ComputeDuration("tc1")
	if dur < 0 {
		t.Errorf("duration = %d, want >= 0", dur)
	}
	// Second call should return 0 (start time consumed).
	dur2 := buf.ComputeDuration("tc1")
	if dur2 != 0 {
		t.Errorf("second duration = %d, want 0", dur2)
	}
}

func TestMarkCancelledToolsFailed(t *testing.T) {
	buf := &buffer.Buffer{
		ToolCalls: []vibekit.ToolCall{
			{ID: "tc1", Status: vibekit.ToolInProgress},
			{ID: "tc2", Status: vibekit.ToolCompleted},
			{ID: "tc3", Status: vibekit.ToolPending},
		},
	}
	changed := buf.MarkCancelledToolsFailed()
	if len(changed) != 2 {
		t.Fatalf("changed = %d, want 2", len(changed))
	}
	if buf.ToolCalls[0].Status != vibekit.ToolFailed {
		t.Errorf("tc1 status = %q, want failed", buf.ToolCalls[0].Status)
	}
	if buf.ToolCalls[1].Status != vibekit.ToolCompleted {
		t.Errorf("tc2 status = %q, want completed (unchanged)", buf.ToolCalls[1].Status)
	}
	if buf.ToolCalls[2].Status != vibekit.ToolFailed {
		t.Errorf("tc3 status = %q, want failed", buf.ToolCalls[2].Status)
	}
}

func TestThoughtChunkPopulatesReasoningField(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	// Send a thought chunk.
	raw := mustJSON(t, map[string]any{
		"sessionUpdate": "agent_thought_chunk",
		"content":       map[string]any{"type": "text", "text": "Let me think..."},
	})
	h.translateACPEvent("c1", &vibekit.RPCResponse{
		Method: "session/update",
		Params: mustJSON(t, map[string]any{"update": raw}),
	})

	// End the turn.
	h.EmitTurnEndedWithStats(t.Context(), "c1", &vibekit.RPCResponse{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}, command.TurnStats{})

	c, _ := cs.Get(t.Context(), "c1")
	if len(c.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(c.Messages))
	}
	if c.Messages[0].Reasoning != "Let me think..." {
		t.Errorf("Reasoning = %q, want %q", c.Messages[0].Reasoning, "Let me think...")
	}
	if c.Messages[0].Content != "" {
		t.Errorf("Content = %q, want empty", c.Messages[0].Content)
	}
}

func TestToolCallDurationMs(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	// Send a tool call.
	h.translateACPEvent("c1", newToolCallMsg(t, "tc1", "Reading file", "in_progress"))

	// Send a tool call update with completed status.
	raw := mustJSON(t, map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    "tc1",
		"status":        "completed",
		"content":       []any{},
	})
	h.translateACPEvent("c1", &vibekit.RPCResponse{
		Method: "session/update",
		Params: mustJSON(t, map[string]any{"update": raw}),
	})

	// End the turn and check the persisted tool call has a duration.
	h.EmitTurnEndedWithStats(t.Context(), "c1", &vibekit.RPCResponse{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}, command.TurnStats{})

	c, _ := cs.Get(t.Context(), "c1")
	if len(c.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(c.Messages))
	}
	if len(c.Messages[0].ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(c.Messages[0].ToolCalls))
	}
	tc := c.Messages[0].ToolCalls[0]
	if tc.DurationMs < 0 {
		t.Errorf("duration_ms = %d, want >= 0", tc.DurationMs)
	}
	if tc.Status != vibekit.ToolCompleted {
		t.Errorf("status = %q, want completed", tc.Status)
	}
}

func TestEmitTurnEnded_DifferentChatID(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c2", func(c *vibekit.Chat, _ bool) bool { c.Name = "B"; return true })

	// Start a turn on a different chat ID to exercise the chatID parameter.
	h.translateACPEvent("c2", newChunkMsg("hello from c2"))
	h.EmitTurnEndedWithStats(t.Context(), "c2", &vibekit.RPCResponse{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}, command.TurnStats{})

	c, _ := cs.Get(t.Context(), "c2")
	if len(c.Messages) != 1 {
		t.Fatalf("messages = %+v", c.Messages)
	}
	if c.Messages[0].Content != "hello from c2" {
		t.Errorf("content = %q, want 'hello from c2'", c.Messages[0].Content)
	}
}
