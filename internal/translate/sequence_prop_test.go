package translate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/api"

	"pgregory.net/rapid"
)

// TestTranslator_SequenceInvariants_Rapid exercises arbitrary handler call
// sequences and asserts state-machine invariants hold.
func TestTranslator_SequenceInvariants_Rapid(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		deps, events := newEventCaptureDeps()
		tr := New(deps, "/tmp", WithIDGenerator(func() string { return "stub-msg-id" }))
		chatID := api.ChatID("prop-chat")

		type action int
		const (
			actChunk action = iota
			actToolCall
			actToolCallUpdate
			actPlan
		)

		numOps := rapid.IntRange(1, 30).Draw(rt, "numOps")
		var toolIDs []string

		for range numOps {
			act := rapid.IntRange(0, 3).Draw(rt, "action")
			switch action(act) {
			case actChunk:
				payload := mustJSONRapid(map[string]any{
					"content": map[string]any{"type": "text", "text": rapid.String().Draw(rt, "text")},
				})
				tr.HandleAssistantChunk(context.Background(), chatID, payload, false)

			case actToolCall:
				id := rapid.StringMatching(`[a-z]{4,8}`).Draw(rt, "toolID")
				toolIDs = append(toolIDs, id)
				payload := mustJSONRapid(map[string]any{
					"toolCallId": id,
					"title":      "test",
					"kind":       "shell",
					"status":     "in_progress",
					"content":    []any{},
				})
				tr.HandleToolCall(context.Background(), chatID, payload, "")

			case actToolCallUpdate:
				if len(toolIDs) == 0 {
					continue
				}
				idx := rapid.IntRange(0, len(toolIDs)-1).Draw(rt, "toolIdx")
				payload := mustJSONRapid(map[string]any{
					"toolCallId": toolIDs[idx],
					"status":     "completed",
					"content":    []any{},
				})
				tr.HandleToolCallUpdate(context.Background(), chatID, payload, "")

			case actPlan:
				payload := mustJSONRapid(map[string]any{
					"entries": []any{},
				})
				tr.HandlePlan(context.Background(), chatID, payload)
			}
		}

		// Invariant: no panics reached here.
		// Invariant: message_created emitted at most once per chunk sequence.
		createdCount := 0
		for _, evt := range *events {
			if evt.Type == api.EventMessageCreated {
				createdCount++
			}
		}
		// Should be at most 1 message_created (first chunk triggers it).
		if createdCount > 1 {
			rt.Fatalf("message_created emitted %d times, want <= 1", createdCount)
		}
	})
}

func mustJSONRapid(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
