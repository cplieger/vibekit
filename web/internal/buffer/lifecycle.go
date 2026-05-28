package buffer

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"vibekit/internal/api"
)

// Lifecycle encapsulates buffer partial-file orchestration: path
// resolution, open/close/remove, and crash-recovery scanning. The
// hub delegates to this type for all partial-file coordination.
type Lifecycle struct {
	ConfigDir string
	Store     *Store
}

// PartialPathFor returns the path for a chat's partial recovery file.
func (l *Lifecycle) PartialPathFor(chatID api.ChatID) string {
	return PartialPath(l.ConfigDir, chatID)
}

// OpenPartialFile opens (or creates) the partial recovery file for a chat.
func (l *Lifecycle) OpenPartialFile(ctx context.Context, chatID api.ChatID, buf *Buffer) {
	if l.ConfigDir == "" {
		return
	}
	buf.OpenPartial(ctx, l.PartialPathFor(chatID))
}

// CloseAndRemovePartial closes the partial file fd and deletes the file.
func (l *Lifecycle) CloseAndRemovePartial(ctx context.Context, chatID api.ChatID, buf *Buffer) {
	path := ""
	if l.ConfigDir != "" {
		path = l.PartialPathFor(chatID)
	}
	if buf != nil {
		buf.ClosePartial(ctx, path)
	} else if path != "" {
		os.Remove(path)
	}
}

// RecoverPartials scans the chats directory for orphaned .partial files
// left by a crash. Returns recovered snapshots paired with their chat IDs
// for the caller to persist. The caller is responsible for appending
// messages and broadcasting events.
func (l *Lifecycle) RecoverPartials() []RecoveredPartial {
	if l.ConfigDir == "" {
		return nil
	}
	dir := filepath.Join(l.ConfigDir, "chats")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var recovered []RecoveredPartial
	for _, e := range entries {
		name := e.Name()
		chatID, ok := strings.CutSuffix(name, ".partial")
		if !ok || chatID == "" {
			continue
		}
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
		snap, snapOK := RecoverPartialSnapshot(data)
		if !snapOK {
			os.Remove(path)
			continue
		}
		recovered = append(recovered, RecoveredPartial{
			ChatID:   api.ChatID(chatID),
			Snapshot: snap,
			Path:     path,
		})
	}
	return recovered
}

// RecoveredPartial holds a recovered partial snapshot and its metadata.
type RecoveredPartial struct {
	ChatID   api.ChatID
	Snapshot PartialSnapshot
	Path     string
}
