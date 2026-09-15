package translate

// The `user_message_id_assigned` sub-kind: the agent naming the record id it has just
// persisted a prompt under. What that id is for: vibekit.Message.KASMessageID.

import (
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// handleUserMessageID records that id onto the prompt this frame belongs to. The frame
// carries {kind, userMessageId} and no key, and it is emitted between the append and the
// model call, so the chat's newest prompt-class user row IS that prompt's row; a second
// frame carrying a DIFFERENT id is the empty-turn retry's second record, which must win.
func (t *Translator) handleUserMessageID(ctx context.Context, chatID vibekit.ChatID, kasID string) {
	_, err := t.chats.Mutate(ctx, chatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			return false
		}
		for i := len(c.Messages) - 1; i >= 0; i-- {
			m := &c.Messages[i]
			if !m.IsPrompt() {
				continue
			}
			if m.KASMessageID == kasID {
				return false
			}
			m.KASMessageID = kasID
			return true
		}
		return false
	})
	if err != nil {
		// A tombstoned chat is expected here, and a row that missed its stamp is the
		// population Message.AgentSideID's fallback to ID already answers for.
		slog.Debug("user message id: not recorded", "chat_id", chatID, "error", err)
	}
}
