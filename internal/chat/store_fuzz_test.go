package chat

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func FuzzStore_MutateGetRoundTrip(f *testing.F) {
	f.Add("test-chat", "hello world")
	f.Add("", "")
	f.Add("unicode-名前", "content with 日本語")
	f.Add("ctrl\x01chars", "null\x00byte")
	f.Add("long-name-"+string(make([]byte, 200)), string(make([]byte, 65536)))

	f.Fuzz(func(t *testing.T, name, content string) {
		dir := t.TempDir()
		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}

		chatID := vibekit.ChatID("fuzz-chat-1")
		ctx := t.Context()

		// Create chat with fuzzed name.
		err = s.Mutate(ctx, chatID, func(c *vibekit.Chat, exists bool) bool {
			c.Name = name
			return true
		})
		if err != nil {
			return // Some names may cause filesystem issues; that's OK.
		}

		// Append message with fuzzed content.
		msg := &vibekit.Message{
			ID:      "msg-1",
			Role:    vibekit.RoleAssistant,
			Content: content,
		}
		err = s.AppendMessage(ctx, chatID, msg)
		if err != nil {
			return
		}

		// Get and verify round-trip.
		chat, ok := s.Get(ctx, chatID)
		if !ok {
			t.Fatal("Get returned false after Mutate+AppendMessage")
		}
		if chat.Name != name {
			t.Fatalf("name mismatch: got %q, want %q", chat.Name, name)
		}
		if len(chat.Messages) == 0 {
			t.Fatal("no messages after AppendMessage")
		}
		last := chat.Messages[len(chat.Messages)-1]
		if last.Content != content {
			t.Fatalf("content mismatch: got %q, want %q", last.Content, content)
		}
	})
}
