package buffer

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/cplieger/atomicfile/v2"
	"github.com/cplieger/vibekit/internal/api"
)

// writeThrottleInterval is the minimum time between fsync operations.
// Losing the last 500ms of streaming content on a crash is acceptable
// since the model will re-emit it on retry.
const writeThrottleInterval = 500 * time.Millisecond

// WritingPartial is the active-state handle for the crash-recovery
// partial file. It is only obtainable via OpenPartial, making it
// impossible to call Write on an idle or disabled writer at compile time.
//
// Each flush replaces the file atomically (temp → fsync → rename via
// atomicfile) instead of truncate-and-rewrite on one inode: a crash
// mid-rewrite previously left truncated JSON that RecoverPartialSnapshot
// dropped as unrecoverable, silently losing the crash-recovery copy of
// the in-flight turn.
type WritingPartial struct {
	lastWrite time.Time
	path      string
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
		wp.disabled = true
	}
}

// CloseAndRemove removes the file at path. (No fd is held between
// flushes — each Write is an atomic replacement.)
func (wp *WritingPartial) CloseAndRemove(ctx context.Context, path string) {
	if path != "" {
		if err := ctx.Err(); err != nil {
			return
		}
		os.Remove(path)
	}
}

func (wp *WritingPartial) writeOnce(ctx context.Context, snap *PartialSnapshot) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	if _, err := atomicfile.WriteFile(ctx, wp.path, data, atomicfile.WithMode(0o600)); err != nil {
		return err
	}
	wp.lastWrite = time.Now()
	return nil
}

// OpenPartial transitions from idle to writing for the partial file at
// path. Returns nil if path is empty or the context is cancelled —
// callers check for nil to detect degraded mode. The file itself is
// created by the first flush (atomically); an open failure surfaces
// there and disables further writes for the turn.
func OpenPartial(ctx context.Context, path string) *WritingPartial {
	if err := ctx.Err(); err != nil {
		slog.Warn("partial: open skipped (context cancelled)", "path", path, "error", err)
		return nil
	}
	if path == "" {
		return nil
	}
	return &WritingPartial{path: path}
}

// PartialSnapshot is the JSON shape written to .partial files.
type PartialSnapshot struct {
	// Refusal preserves the model-refusal metadata (kiro-cli 2.13) so a
	// crash between the refusal chunk and the turn commit still recovers
	// the refusal callout onto the interrupted assistant message.
	Refusal   *api.RefusalInfo `json:"refusal,omitempty"`
	MessageID string           `json:"message_id"`
	Content   string           `json:"content"`
	Reasoning string           `json:"reasoning,omitempty"`
	ToolCalls []api.ToolCall   `json:"tool_calls,omitempty"`
	// Blocks mirrors Buffer.Blocks so a crash mid-turn preserves the
	// chronological order. Without this, recovery would reconstruct
	// the message from Content+ToolCalls only and fall back to the
	// legacy "all text, then all tools" rendering.
	Blocks []api.Block `json:"blocks,omitempty"`
	// CodeReferences preserves licensed-code attributions accumulated
	// during the turn so a crash mid-turn recovers them onto the
	// interrupted assistant message.
	CodeReferences []api.CodeReference `json:"code_references,omitempty"`
	Ts             int64               `json:"ts"`
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
