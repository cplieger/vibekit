package translate

// Compaction domain helpers. On v3 (KAS) compaction status arrives via the
// session_info_update summarization sub-block (see v3_updates.go's
// handleV3Summarization), which calls the handleCompaction* helpers here.

import (
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// handleCompactionCompleted persists the compacted-summary event and records
// the compaction watermark. KAS self-reorients after summarization, so no
// context-recovery prompt is injected: the old injectContextRecovery was dead
// on v3 (it sent session/prompt as a JSON-RPC notification, which KAS drops).
func (t *Translator) handleCompactionCompleted(ctx context.Context, chatID vibekit.ChatID, summaryPtr *string) {
	summary := ""
	if summaryPtr != nil {
		summary = *summaryPtr
	}
	evt := t.newEventMessage(vibekit.EventCompacted, summary)
	if err := t.deps.ChatRecords().AppendMessage(ctx, chatID, &evt); err != nil {
		slog.Error("compaction: append event", "chat_id", chatID, "error", err)
	}
	if ctx.Err() != nil {
		return
	}
	if err := t.deps.ChatRecords().Mutate(ctx, chatID, func(c *vibekit.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.CompactionWatermark = evt.ID
		return true
	}); err != nil {
		slog.Error("compaction: set watermark", "chat_id", chatID, "error", err)
	}
}

// handleCompactionFailed persists a compaction-failed event and broadcasts
// a typed error to the client.
func (t *Translator) handleCompactionFailed(ctx context.Context, chatID vibekit.ChatID, errMsg string) {
	if errMsg == "" {
		errMsg = "compaction failed"
	}
	evt := t.newEventMessage(vibekit.EventCompactFailed, errMsg)
	if err := t.deps.ChatRecords().AppendMessage(ctx, chatID, &evt); err != nil {
		slog.Error("compaction: append failed event", "chat_id", chatID, "error", err)
	}
	t.deps.Broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{Code: vibekit.ErrCodeCompactionFailed, Message: errMsg}))
}
