// Crash-recovery partial file lifecycle (Hub-level orchestration).
//
// The buffer.PartialWriter handles the fd lifecycle; this file owns
// the Hub-level path resolution, recovery scan, and cleanup.

package hub

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vibekit/internal/api"
	"vibekit/internal/buffer"
)

// partialPath returns the path for a chat's partial recovery file.
func (h *Hub) partialPath(chatID api.ChatID) string {
	return buffer.PartialPath(h.lifecycle.configDir, chatID)
}

// openPartialFile opens (or creates) the partial recovery file for a chat.
func (h *Hub) openPartialFile(chatID api.ChatID, buf *buffer.Buffer) {
	if h.lifecycle.configDir == "" {
		return
	}
	buf.OpenPartial(h.partialPath(chatID))
}

// closeAndRemovePartial closes the partial file fd and deletes the file.
func (h *Hub) closeAndRemovePartial(chatID api.ChatID, buf *buffer.Buffer) {
	path := ""
	if h.lifecycle.configDir != "" {
		path = h.partialPath(chatID)
	}
	if buf != nil {
		buf.ClosePartial(path)
	} else if path != "" {
		os.Remove(path)
	}
}

// RecoverPartials scans the chats directory for orphaned .partial files
// left by a crash. Each is parsed and merged into its chat as an
// interrupted assistant message. Called once at startup.
func (h *Hub) RecoverPartials() {
	if h.lifecycle.configDir == "" {
		return
	}
	ctx, cancel := h.hubContext()
	defer cancel()
	dir := filepath.Join(h.lifecycle.configDir, "chats")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		chatID, ok := strings.CutSuffix(name, ".partial")
		if !ok || chatID == "" {
			continue
		}
		cid := api.ChatID(chatID)
		if e.IsDir() {
			slog.Debug("partial recovery: skipping directory",
				"chat_id", chatID, "path", filepath.Join(dir, name))
			continue
		}
		path := filepath.Join(dir, name)
		data, readErr := os.ReadFile(path)
		if readErr != nil || len(data) == 0 {
			os.Remove(path)
			continue
		}
		snap, ok := buffer.RecoverPartialSnapshot(data)
		if !ok {
			os.Remove(path)
			continue
		}
		msg := api.Message{
			ID:        snap.MessageID,
			Role:      api.RoleAssistant,
			Ts:        snap.Ts,
			Content:   snap.Content,
			Reasoning: snap.Reasoning,
			ToolCalls: snap.ToolCalls,
		}
		if err := h.chatStore.AppendMessage(ctx, cid, &msg); err != nil {
			slog.Warn("partial recovery: append failed", "chat_id", chatID, "error", err)
		}
		evt := api.Message{
			ID:        newMessageID(),
			Role:      api.RoleEvent,
			Ts:        time.Now().UnixMilli(),
			EventKind: api.EventInterrupted,
			Content:   "Turn interrupted by server restart",
		}
		if err := h.chatStore.AppendMessage(ctx, cid, &evt); err != nil {
			slog.Warn("partial recovery: append interrupted", "chat_id", chatID, "error", err)
		}
		if rmErr := os.Remove(path); rmErr != nil {
			recovered := path + ".recovered"
			if renameErr := os.Rename(path, recovered); renameErr != nil {
				slog.Warn("partial recovery: remove and rename failed; duplicates likely on next restart",
					"chat_id", chatID, "remove_error", rmErr, "rename_error", renameErr)
			}
		}
		slog.Info("recovered partial turn", "chat_id", chatID,
			"content_len", len(snap.Content), "tools", len(snap.ToolCalls))
	}
}
