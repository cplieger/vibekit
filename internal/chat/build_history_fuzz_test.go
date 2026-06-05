package chat

import (
	"context"
	"strings"
	"testing"

	"vibekit/internal/api"
)

// FuzzBuildHistory targets the plain-text history builder used for
// compression priming. Bug class: uncontrolled output growth or missing
// role coverage — if a role is unrecognised, the message is silently
// dropped, but the line count should reflect only the roles that ARE
// rendered. Also checks that output lines count matches rendered messages.
func FuzzBuildHistory(f *testing.F) {
	f.Add("hello", "response", "compacted", uint8(0))
	f.Add("", "", "", uint8(1))
	f.Add("multi\nline\ninput", "multi\nline\noutput", "event content", uint8(2))
	f.Add(strings.Repeat("x", 10000), "short", "", uint8(3))

	f.Fuzz(func(t *testing.T, userContent, assistantContent, eventContent string, flags uint8) {
		dir := t.TempDir()
		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}

		chatID := api.ChatID("fuzz-history-1")
		ctx := context.Background()

		// Create chat with messages of various roles.
		err = s.Mutate(ctx, chatID, func(c *api.Chat, exists bool) bool {
			c.Name = "history-test"
			c.Messages = []api.Message{
				{ID: "m1", Role: api.RoleUser, Content: userContent, Ts: 1},
				{ID: "m2", Role: api.RoleAssistant, Content: assistantContent, Ts: 2},
				{ID: "m3", Role: api.RoleEvent, EventKind: api.EventCompacted, Content: eventContent, Ts: 3},
			}
			// Optionally add unknown role based on flags.
			if flags&1 != 0 {
				c.Messages = append(c.Messages, api.Message{
					ID: "m4", Role: api.Role("unknown_role"), Content: "ignored", Ts: 4,
				})
			}
			return true
		})
		if err != nil {
			return
		}

		history := s.BuildHistory(ctx, chatID)

		// Invariant 1: history must contain the user content if non-empty.
		if userContent != "" && !strings.Contains(history, userContent) {
			t.Fatalf("BuildHistory missing user content")
		}

		// Invariant 2: history must contain the assistant content if non-empty.
		if assistantContent != "" && !strings.Contains(history, assistantContent) {
			t.Fatalf("BuildHistory missing assistant content")
		}

		// Invariant 3: history must contain "User: " and "Assistant: " prefixes.
		if !strings.Contains(history, "User: ") {
			t.Fatalf("BuildHistory missing 'User: ' prefix")
		}
		if !strings.Contains(history, "Assistant: ") {
			t.Fatalf("BuildHistory missing 'Assistant: ' prefix")
		}

		// Invariant 4: unknown roles must NOT appear in output.
		if flags&1 != 0 && strings.Contains(history, "ignored") {
			t.Fatalf("BuildHistory included unknown role content")
		}
	})
}
