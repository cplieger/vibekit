package buffer

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"time"

	"vibekit/internal/api"
)

// writeThrottleInterval is the minimum time between fsync operations.
// Losing the last 500ms of streaming content on a crash is acceptable
// since the model will re-emit it on retry.
const writeThrottleInterval = 500 * time.Millisecond

// WritingPartial is the active-state handle for the crash-recovery
// partial file. It is only obtainable via OpenPartial, making it
// impossible to call Write on an idle or disabled writer at compile time.
type WritingPartial struct {
	lastWrite time.Time
	file      *os.File
	disabled  bool
}

// Write rewrites the partial file with the given snapshot data.
// Writes are throttled to at most once per writeThrottleInterval to
// reduce fsync pressure during fast model output.
func (wp *WritingPartial) Write(ctx context.Context, snap *PartialSnapshot) {
	if wp.disabled {
		slog.Warn("WritingPartial.Write called in disabled state", "message_id", snap.MessageID)
		return
	}
	if time.Since(wp.lastWrite) < writeThrottleInterval {
		return
	}
	if err := wp.writeOnce(ctx, snap); err != nil {
		slog.Warn("partial snapshot write failed; "+
			"further partial updates disabled for this turn",
			"message_id", snap.MessageID, "error", err)
		_ = wp.file.Close()
		wp.file = nil
		wp.disabled = true
	}
}

// Flush writes the final snapshot unconditionally (ignoring throttle),
// ensuring the last state is captured before the partial file is removed.
func (wp *WritingPartial) Flush(ctx context.Context, snap *PartialSnapshot) {
	if wp.disabled {
		return
	}
	if err := wp.writeOnce(ctx, snap); err != nil {
		slog.Warn("partial flush failed", "message_id", snap.MessageID, "error", err)
	}
}

// CloseAndRemove closes the fd and removes the file at path.
func (wp *WritingPartial) CloseAndRemove(ctx context.Context, path string) {
	if wp.file != nil {
		wp.file.Close()
		wp.file = nil
	}
	if path != "" {
		if err := ctx.Err(); err != nil {
			return
		}
		os.Remove(path)
	}
}

func (wp *WritingPartial) writeOnce(_ context.Context, snap *PartialSnapshot) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	if _, err := wp.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := wp.file.Truncate(0); err != nil {
		return err
	}
	if _, err := wp.file.Write(data); err != nil {
		return err
	}
	wp.lastWrite = time.Now()
	return wp.file.Sync()
}

// OpenPartial transitions from idle to writing by opening the partial
// file. Returns nil if the file cannot be opened or the context is
// cancelled — callers check for nil to detect degraded mode.
func OpenPartial(ctx context.Context, path string) *WritingPartial {
	if err := ctx.Err(); err != nil {
		slog.Warn("partial: open skipped (context cancelled)", "path", path, "error", err)
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		slog.Warn("partial: open failed", "path", path, "error", err)
		return nil
	}
	return &WritingPartial{file: f}
}

// PartialSnapshot is the JSON shape written to .partial files.
type PartialSnapshot struct {
	MessageID string         `json:"message_id"`
	Content   string         `json:"content"`
	Reasoning string         `json:"reasoning,omitempty"`
	ToolCalls []api.ToolCall `json:"tool_calls,omitempty"`
	// Blocks mirrors Buffer.Blocks so a crash mid-turn preserves the
	// chronological order. Without this, recovery would reconstruct
	// the message from Content+ToolCalls only and fall back to the
	// legacy "all text, then all tools" rendering.
	Blocks []api.Block `json:"blocks,omitempty"`
	Ts     int64       `json:"ts"`
}

// PartialPath returns the path for a chat's partial recovery file.
func PartialPath(configDir string, chatID api.ChatID) string {
	if configDir == "" {
		return ""
	}
	return configDir + "/chats/" + string(chatID) + ".partial"
}

// RecoverPartialSnapshot parses a partial file's bytes into a snapshot.
// A snapshot with empty Content is still recoverable when Reasoning is
// non-empty (a turn that was entirely reasoning at crash time).
func RecoverPartialSnapshot(data []byte) (PartialSnapshot, bool) {
	var snap PartialSnapshot
	if json.Unmarshal(data, &snap) != nil {
		return PartialSnapshot{}, false
	}
	if snap.Content == "" && snap.Reasoning == "" {
		return PartialSnapshot{}, false
	}
	return snap, true
}
