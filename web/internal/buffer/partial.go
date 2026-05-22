package buffer

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"

	"vibekit/internal/api"
)

// PartialState represents the lifecycle state of the crash-recovery
// partial file writer.
type PartialState int

const (
	PartialIdle     PartialState = iota // not started; no fd
	PartialWriting                      // fd open, writing snapshots
	PartialDisabled                     // error occurred; fd closed, no further writes
)

// Compile-time assertion: update String() when adding variants.
var _ [PartialDisabled + 1]struct{} = [3]struct{}{}

// String returns a human-readable name for the partial file lifecycle state.
func (s PartialState) String() string {
	switch s {
	case PartialIdle:
		return "idle"
	case PartialWriting:
		return "writing"
	case PartialDisabled:
		return "disabled"
	default:
		return "partialState(unknown)"
	}
}

// PartialWriter encapsulates the crash-recovery partial file lifecycle.
type PartialWriter struct {
	file  *os.File
	state PartialState
}

// Open transitions from Idle to Writing by opening the partial file.
func (pw *PartialWriter) Open(path string) {
	if pw.state == PartialWriting {
		panic("PartialWriter.Open called in Writing state (missing CloseAndRemove)")
	}
	if pw.state != PartialIdle {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		slog.Warn("partial: open failed", "path", path, "error", err)
		pw.state = PartialDisabled
		return
	}
	pw.file = f
	pw.state = PartialWriting
}

// Write rewrites the partial file with the given snapshot data.
func (pw *PartialWriter) Write(ctx context.Context, snap PartialSnapshot) {
	if pw.state == PartialDisabled {
		slog.Warn("PartialWriter.Write called in Disabled state", "message_id", snap.MessageID)
		return
	}
	if pw.state != PartialWriting {
		return
	}
	if err := pw.writeOnce(ctx, snap); err != nil {
		slog.Warn("partial snapshot write failed; "+
			"further partial updates disabled for this turn",
			"message_id", snap.MessageID, "error", err)
		_ = pw.file.Close()
		pw.file = nil
		pw.state = PartialDisabled
	}
}

// CloseAndRemove closes the fd (if open) and removes the file at path.
func (pw *PartialWriter) CloseAndRemove(path string) {
	if pw.file != nil {
		pw.file.Close()
		pw.file = nil
	}
	pw.state = PartialIdle
	if path != "" {
		os.Remove(path)
	}
}

func (pw *PartialWriter) writeOnce(_ context.Context, snap PartialSnapshot) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	if _, err := pw.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := pw.file.Truncate(0); err != nil {
		return err
	}
	if _, err := pw.file.Write(data); err != nil {
		return err
	}
	return pw.file.Sync()
}

// PartialSnapshot is the JSON shape written to .partial files.
type PartialSnapshot struct {
	MessageID   string         `json:"message_id"`
	Content     string         `json:"content"`
	ToolCalls   []api.ToolCall `json:"tool_calls,omitempty"`
	IsReasoning bool           `json:"is_reasoning,omitempty"`
	Ts          int64          `json:"ts"`
}

// PartialPath returns the path for a chat's partial recovery file.
func PartialPath(configDir string, chatID api.ChatID) string {
	if configDir == "" {
		return ""
	}
	return configDir + "/chats/" + string(chatID) + ".partial"
}

// RecoverPartialSnapshot parses a partial file's bytes into a snapshot.
func RecoverPartialSnapshot(data []byte) (PartialSnapshot, bool) {
	var snap PartialSnapshot
	if json.Unmarshal(data, &snap) != nil || snap.Content == "" {
		return PartialSnapshot{}, false
	}
	return snap, true
}
