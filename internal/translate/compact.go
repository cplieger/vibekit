package translate

// Compaction domain helpers, called from v3_updates.go's handleV3Summarization.
//
// Nothing here checks the turn's mute, unlike HandlePlan: a watermark is a fact
// about the SESSION, so it must survive a priming turn.

import (
	"cmp"
	"context"
	"errors"
	"log/slog"

	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/durable"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// handleCompactionCompleted persists the compacted-summary event and records the
// watermark. KAS self-reorients, so no context-recovery prompt is injected.
//
// The turn is SEALED first, because a compaction point is a position INSIDE the
// open turn's block stream: the seal puts the event between the two segments,
// and the segment's message_appended must reach the bus before the event's.
func (t *Translator) handleCompactionCompleted(ctx context.Context, chatID vibekit.ChatID, summaryPtr *string) {
	summary := ""
	if summaryPtr != nil {
		summary = *summaryPtr
	}
	// A watermark naming an event the same shutdown refused would be worse than
	// neither, so every effect below rides one detached context.
	ctx = durable.Context(ctx)
	t.turns.SealTurnSegment(ctx, chatID)
	evt := t.newEventMessage(vibekit.EventCompacted, summary)
	err := t.chats.AppendMessage(ctx, chatID, &evt)
	if errors.Is(err, chat.ErrTombstoned) {
		return
	}
	if err != nil {
		slog.Error("compaction: append event", "chat_id", chatID, "error", err)
	}
	err = t.chats.Mutate(ctx, chatID, func(c *vibekit.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.CompactionWatermark = evt.ID
		return true
	})
	if errors.Is(err, chat.ErrTombstoned) {
		return
	}
	if err != nil {
		slog.Error("compaction: set watermark", "chat_id", chatID, "error", err)
	}
}

// handleCompactionFailed persists a compaction-failed event and broadcasts
// a typed error to the client.
func (t *Translator) handleCompactionFailed(ctx context.Context, chatID vibekit.ChatID, errMsg string) {
	errMsg = cmp.Or(errMsg, "compaction failed")
	evt := t.newEventMessage(vibekit.EventCompactFailed, errMsg)
	err := t.chats.AppendMessage(durable.Context(ctx), chatID, &evt)
	if errors.Is(err, chat.ErrTombstoned) {
		return
	}
	if err != nil {
		slog.Error("compaction: append failed event", "chat_id", chatID, "error", err)
	}
	// Turn-scoped: deriveTurnOutcome grades a turn holding this event as failed.
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{
		Code: vibekit.ErrCodeCompactionFailed, Message: errMsg, TurnScoped: true,
	}))
}
