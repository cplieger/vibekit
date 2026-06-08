// Crash-recovery partial file lifecycle (Hub-level orchestration).
//
// The buffer.Lifecycle handles path resolution, fd lifecycle, and
// recovery scanning; this file owns the Hub-level message persistence
// and event broadcasting that follows recovery.

package hub

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/buffer"
)

// openPartialFile opens (or creates) the partial recovery file for a chat.
func (h *Hub) openPartialFile(ctx context.Context, chatID api.ChatID, buf *buffer.Buffer) {
	h.bufLifecycle.OpenPartialFile(ctx, chatID, buf)
}

// closeAndRemovePartial closes the partial file fd and deletes the file.
func (h *Hub) closeAndRemovePartial(ctx context.Context, chatID api.ChatID, buf *buffer.Buffer) {
	h.bufLifecycle.CloseAndRemovePartial(ctx, chatID, buf)
}

// RecoverPartials scans the chats directory for orphaned .partial files
// left by a crash. Each is parsed and merged into its chat as an
// interrupted assistant message. Called once at startup.
func (h *Hub) RecoverPartials() {
	ctx, cancel := h.hubContext()
	defer cancel()

	recovered := h.bufLifecycle.RecoverPartials()
	for i := range recovered {
		r := &recovered[i]
		msg := api.Message{
			ID:        r.Snapshot.MessageID,
			Role:      api.RoleAssistant,
			Ts:        r.Snapshot.Ts,
			Content:   r.Snapshot.Content,
			Reasoning: r.Snapshot.Reasoning,
			ToolCalls: r.Snapshot.ToolCalls,
			Blocks:    r.Snapshot.Blocks,
		}
		if err := h.chatStore.AppendMessage(ctx, r.ChatID, &msg); err != nil {
			slog.Warn("partial recovery: append failed", "chat_id", r.ChatID, "error", err)
		}
		evt := api.Message{
			ID:        newMessageID(),
			Role:      api.RoleEvent,
			Ts:        time.Now().UnixMilli(),
			EventKind: api.EventInterrupted,
			Content:   "Turn interrupted by server restart",
		}
		if err := h.chatStore.AppendMessage(ctx, r.ChatID, &evt); err != nil {
			slog.Warn("partial recovery: append interrupted", "chat_id", r.ChatID, "error", err)
		}
		if rmErr := os.Remove(r.Path); rmErr != nil {
			recovered := r.Path + ".recovered"
			if renameErr := os.Rename(r.Path, recovered); renameErr != nil {
				slog.Warn("partial recovery: remove and rename failed; duplicates likely on next restart",
					"chat_id", r.ChatID, "remove_error", rmErr, "rename_error", renameErr)
			}
		}
		slog.Info("recovered partial turn", "chat_id", r.ChatID,
			"content_len", len(r.Snapshot.Content), "tools", len(r.Snapshot.ToolCalls))
	}
}
