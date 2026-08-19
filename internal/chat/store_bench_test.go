package chat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func BenchmarkStore_AppendMessage(b *testing.B) {
	dir := b.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		b.Fatalf("NewStore: %v", err)
	}

	chatID := vibekit.ChatID("bench-chat")
	ctx := b.Context()

	// Create chat with 10 pre-existing messages.
	err = s.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool {
		c.Name = "benchmark chat"
		for i := range 10 {
			c.Messages = append(c.Messages, vibekit.Message{
				ID:      fmt.Sprintf("pre-%d", i),
				Role:    vibekit.RoleAssistant,
				Content: strings.Repeat("x", 200),
			})
		}
		return true
	})
	if err != nil {
		b.Fatalf("setup Mutate: %v", err)
	}

	// Realistic message payload (~500 bytes content).
	msg := &vibekit.Message{
		ID:      "bench-msg",
		Role:    vibekit.RoleAssistant,
		Content: strings.Repeat("benchmark content ", 28), // ~504 bytes
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		msg.ID = fmt.Sprintf("bench-%d", i)
		if err := s.AppendMessage(ctx, chatID, msg); err != nil {
			b.Fatalf("AppendMessage: %v", err)
		}
	}
}
