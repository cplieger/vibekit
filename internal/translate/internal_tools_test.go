package translate

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestHandleToolCall_InternalToolSuppression pins the internal-tool drop: KAS
// announces its session-boot cloud-config fetch as a tool_call tagged
// _meta.kiro.toolId "fetch_cloud_config", its own TUI never renders it, and the
// frame arriving before the prompt's turn used to open a wire turn the prompt
// then displaced — the phantom "Agent-initiated turn". The frame must reach
// neither the buffer nor the wire, and its follow-up update must be dropped
// BEFORE TurnFoldTarget can open a turn for it.
func TestHandleToolCall_InternalToolSuppression(t *testing.T) {
	cloudConfig := map[string]any{
		"toolCallId": "cc-1",
		"title":      "Fetching your cloud config",
		"kind":       "other",
		"status":     "in_progress",
		"_meta": map[string]any{"kiro": map[string]any{
			"toolId": "fetch_cloud_config",
		}},
	}

	t.Run("CallIsDropped", func(t *testing.T) {
		base, events := newEventCaptureDeps()
		tr := New(rolesOf(base), withIDGenerator(func() string { return "id" }))
		chatID := vibekit.ChatID("c1")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, cloudConfig), FrameAttribution{})
		if hasToolCallEvent(events) {
			t.Error("internal tool call broadcast a tool_call event; want suppressed")
		}
		if n := len(base.bufStore.GetOrInit(chatID).ToolCalls); n != 0 {
			t.Errorf("buffered tool calls = %d, want 0 (internal tool must not be buffered)", n)
		}
	})

	t.Run("UpdateIsDroppedWithoutTouchingTheFoldTarget", func(t *testing.T) {
		base, events := newEventCaptureDeps()
		tr := New(rolesOf(base), withIDGenerator(func() string { return "id" }))
		chatID := vibekit.ChatID("c1")
		tr.HandleToolCall(t.Context(), chatID, mustJSON(t, cloudConfig), FrameAttribution{})
		for range 2 {
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
				"toolCallId": "cc-1",
				"status":     "completed",
			}), FrameAttribution{})
		}
		for _, e := range *events {
			if e.Type == vibekit.EventToolCallUpdate {
				t.Error("suppressed internal tool's update was broadcast; want dropped")
			}
		}
		// The load-bearing half: the update must not have opened a turn. The fold
		// target lazily creates a buffer per chat, so an untouched store is the proof
		// the update asked OpenTurnBuffer rather than TurnFoldTarget.
		if base.bufStore.Get(chatID) != nil {
			t.Error("the suppressed update opened a buffer; want dropped, its create was never buffered")
		}
	})

	t.Run("OrdinaryOtherKindToolIsShown", func(t *testing.T) {
		base, events := newEventCaptureDeps()
		tr := New(rolesOf(base), withIDGenerator(func() string { return "id" }))
		tr.HandleToolCall(t.Context(), vibekit.ChatID("c1"), mustJSON(t, map[string]any{
			"toolCallId": "ws-1",
			"title":      "web_search",
			"kind":       "other",
			"status":     "in_progress",
			"_meta": map[string]any{"kiro": map[string]any{
				"toolId": "web_search",
			}},
		}), FrameAttribution{})
		if !hasToolCallEvent(events) {
			t.Error("ordinary kind:other tool call suppressed; only internal toolIds are gated")
		}
	})
}

func TestHandleToolCallUpdate_UnknownToolCallOpensNoTurn(t *testing.T) {
	base, events := newEventCaptureDeps()
	tr := New(rolesOf(base))

	tr.HandleToolCallUpdate(t.Context(), "c1", mustJSON(t, map[string]any{
		"toolCallId": "unknown",
		"status":     "completed",
	}), FrameAttribution{})

	if base.bufStore.Get("c1") != nil {
		t.Error("unknown tool update opened a turn buffer")
	}
	if len(*events) != 0 {
		t.Errorf("unknown tool update emitted %d events, want 0", len(*events))
	}
}
