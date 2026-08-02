package api

import (
	"encoding/json"
	"testing"
)

// FuzzChatHeaderConsistency targets the Chat.Header() extraction method.
// Bug class: field-copy omission — if a new field is added to Chat but
// forgotten in Header(), the header will silently omit data. This fuzzer
// verifies that all header-level fields round-trip correctly from a Chat
// populated with fuzzed data.
func FuzzChatHeaderConsistency(f *testing.F) {
	f.Add(`{"id":"c1","name":"test","model":"m","messages":[],"created_at":1,"updated_at":2,"message_count":0}`)
	f.Add(`{"id":"c2","name":"","acp_session_id":"s1","current_mode_id":"code","messages":[{"id":"m1","role":"user","content":"hi","ts":1}],"supervised_mode":true}`)
	f.Add(`{"id":"c3","name":"n","summary":"sum","compaction_watermark":"wm","parent_chat_id":"p1","messages":[{},{}]}`)

	f.Fuzz(func(t *testing.T, data string) {
		var chat Chat
		if json.Unmarshal([]byte(data), &chat) != nil {
			return
		}

		header := chat.Header()

		// Invariant 1: scalar fields must match exactly.
		if header.ID != chat.ID {
			t.Fatalf("ID mismatch: header=%q chat=%q", header.ID, chat.ID)
		}
		if header.Name != chat.Name {
			t.Fatalf("Name mismatch: header=%q chat=%q", header.Name, chat.Name)
		}
		if header.Model != chat.Model {
			t.Fatalf("Model mismatch")
		}
		if header.ACPSessionID != chat.ACPSessionID {
			t.Fatalf("ACPSessionID mismatch")
		}
		if header.CurrentModeID != chat.CurrentModeID {
			t.Fatalf("CurrentModeID mismatch")
		}
		if header.CreatedAt != chat.CreatedAt {
			t.Fatalf("CreatedAt mismatch")
		}
		if header.UpdatedAt != chat.UpdatedAt {
			t.Fatalf("UpdatedAt mismatch")
		}
		if header.SupervisedMode != chat.SupervisedMode {
			t.Fatalf("SupervisedMode mismatch")
		}
		if header.ParentChatID != chat.ParentChatID {
			t.Fatalf("ParentChatID mismatch")
		}
		if header.CompactionWatermark != chat.CompactionWatermark {
			t.Fatalf("CompactionWatermark mismatch")
		}

		// Invariant 2: MessageCount must reflect the actual Messages slice length.
		if header.MessageCount != len(chat.Messages) {
			t.Fatalf("MessageCount = %d, want %d (len(Messages))",
				header.MessageCount, len(chat.Messages))
		}

		// Invariant 3: Usage must be copied by value.
		if header.Usage.ContextPct != chat.Usage.ContextPct ||
			header.Usage.Credits != chat.Usage.Credits ||
			header.Usage.TurnCount != chat.Usage.TurnCount {
			t.Fatalf("Usage mismatch between header and chat")
		}
	})
}
