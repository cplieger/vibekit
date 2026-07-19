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
	"path/filepath"
	"strings"
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
//
// Idempotent against the commit-then-delete window: EmitTurnEndedWithStats
// persists the finalized turn BEFORE deleting the .partial, so a crash
// after the commit but before the delete leaves a .partial whose
// MessageID is already in the chat. When that message already exists,
// recovery skips the append (and the interrupted event) — the turn
// completed normally, it wasn't a crash — and only removes the orphan.
func (h *Hub) RecoverPartials() {
	ctx, cancel := h.hubContext()
	defer cancel()

	// Sweep leftover *.partial.recovered from prior boots first so they
	// don't accrue (see removePartialFile's rename fallback).
	h.reapRecoveredPartials()

	recovered := h.bufLifecycle.RecoverPartials()
	for i := range recovered {
		r := &recovered[i]
		if h.chatHasMessage(ctx, r.ChatID, r.Snapshot.MessageID) {
			// Turn already committed (crash after commit, before delete).
			// Don't double-append; just drop the orphan file.
			h.removePartialFile(r)
			slog.Info("partial recovery: skipped already-committed turn",
				"chat_id", r.ChatID, "message_id", r.Snapshot.MessageID)
			continue
		}
		msg := api.Message{
			ID:        r.Snapshot.MessageID,
			Role:      api.RoleAssistant,
			Ts:        r.Snapshot.Ts,
			Content:   r.Snapshot.Content,
			Reasoning: r.Snapshot.Reasoning,
			// Normalize non-terminal tool statuses to failed so a
			// crash-recovered turn doesn't render a permanently-spinning
			// "in_progress" chip on replay — the tool never completed.
			ToolCalls:      recoveredToolCalls(r.Snapshot.ToolCalls),
			Blocks:         r.Snapshot.Blocks,
			CodeReferences: r.Snapshot.CodeReferences,
			Refusal:        r.Snapshot.Refusal,
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
		h.removePartialFile(r)
		slog.Info("recovered partial turn", "chat_id", r.ChatID,
			"content_len", len(r.Snapshot.Content), "tools", len(r.Snapshot.ToolCalls))
	}
}

// chatHasMessage reports whether the chat already contains a message with
// the given id. Makes RecoverPartials idempotent across the
// commit-then-delete window. An empty id never matches (a started turn
// always has a non-empty MessageID; the guard avoids a false match).
func (h *Hub) chatHasMessage(ctx context.Context, chatID api.ChatID, msgID string) bool {
	if msgID == "" {
		return false
	}
	c, ok := h.chatStore.Get(ctx, chatID)
	if !ok {
		return false
	}
	for i := range c.Messages {
		if c.Messages[i].ID == msgID {
			return true
		}
	}
	return false
}

// removePartialFile deletes a recovered .partial file. If the delete
// fails it renames the file to <path>.recovered so this file isn't
// re-scanned every boot; the content it held was already recovered (or
// already committed), and RecoverPartials is idempotent, so re-processing
// would be harmless anyway. reapRecoveredPartials sweeps the renamed
// files on a later boot.
func (h *Hub) removePartialFile(r *buffer.RecoveredPartial) {
	if rmErr := os.Remove(r.Path); rmErr != nil {
		renamed := r.Path + ".recovered"
		if renameErr := os.Rename(r.Path, renamed); renameErr != nil {
			slog.Warn("partial recovery: remove and rename failed",
				"chat_id", r.ChatID, "remove_error", rmErr, "rename_error", renameErr)
		}
	}
}

// reapRecoveredPartials deletes leftover <chat>.partial.recovered files
// (created by removePartialFile when a .partial couldn't be deleted after
// recovery) so they don't accrue across boots. Their content was already
// recovered when they were renamed, so deleting them is always safe.
// Best-effort: a delete failure is logged and skipped.
func (h *Hub) reapRecoveredPartials() {
	dir := h.bufLifecycle.ConfigDir
	if dir == "" {
		return
	}
	chatsDir := filepath.Join(dir, "chats")
	entries, err := os.ReadDir(chatsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".partial.recovered") {
			continue
		}
		path := filepath.Join(chatsDir, e.Name())
		if rmErr := os.Remove(path); rmErr != nil {
			slog.Warn("partial recovery: reap orphan .recovered failed", "path", path, "error", rmErr)
			continue
		}
		slog.Info("partial recovery: reaped orphan .recovered file", "path", path)
	}
}

// recoveredToolCalls normalizes a recovered snapshot's tool calls: any
// non-terminal status (pending / in_progress) becomes failed so a
// crash-recovered turn doesn't render a permanently-spinning chip on
// replay. Terminal statuses (completed / failed) are left as-is. Mutates
// and returns the slice.
func recoveredToolCalls(calls []api.ToolCall) []api.ToolCall {
	for i := range calls {
		if calls[i].Status == api.ToolInProgress || calls[i].Status == api.ToolPending {
			calls[i].Status = api.ToolFailed
		}
	}
	return calls
}
