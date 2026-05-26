// Package buffer provides the per-chat assistant turn buffering subsystem.
//
// Buffer accumulates streaming deltas per chat until turn_ended writes
// the finalized assistant message to the chat file. Store is a
// concurrency-safe map of per-chat buffers.
package buffer

import (
	"context"
	"strings"
	"time"

	"vibekit/internal/api"
)

// Buffer accumulates streaming deltas per chat until turn_ended
// writes the finalized assistant message to the chat file.
//
// Content and Reasoning are sibling streams that both fill during a
// single turn — extended-thinking models emit reasoning ("Thinking…")
// deltas alongside the regular response. The translator routes each
// chunk to the appropriate builder based on the upstream IsReasoning
// flag.
type Buffer struct {
	ToolStartTimes map[string]int64
	ToolCallIndex  map[string]int
	ChangedFiles   map[string]*api.FileChange
	Partial        PartialWriter
	MessageID      string
	Content        strings.Builder
	Reasoning      strings.Builder
	ToolCalls      []api.ToolCall
	Started        bool
}

// TrackFileChanges accumulates per-file change stats from tool call diffs.
func (buf *Buffer) TrackFileChanges(diffs []api.ToolDiff, isNewFile bool) {
	if buf.ChangedFiles == nil {
		buf.ChangedFiles = make(map[string]*api.FileChange)
	}
	for _, d := range diffs {
		if d.Path == "" {
			continue
		}
		fc, ok := buf.ChangedFiles[d.Path]
		if !ok {
			fc = &api.FileChange{IsNewFile: isNewFile}
			buf.ChangedFiles[d.Path] = fc
		}
		if d.NewText != "" {
			fc.LinesAdded += strings.Count(d.NewText, "\n")
		}
		if d.OldText != "" {
			fc.LinesRemoved += strings.Count(d.OldText, "\n")
		}
	}
}

// RecordToolStart records the start time for a tool call so we can
// compute DurationMs on completion.
func (buf *Buffer) RecordToolStart(toolCallID string) {
	if buf.ToolStartTimes == nil {
		buf.ToolStartTimes = make(map[string]int64)
	}
	buf.ToolStartTimes[toolCallID] = time.Now().UnixMilli()
}

// ComputeDuration returns the elapsed time since the tool started, or 0
// if the start time was not recorded.
func (buf *Buffer) ComputeDuration(toolCallID string) int {
	start, ok := buf.ToolStartTimes[toolCallID]
	if !ok {
		return 0
	}
	delete(buf.ToolStartTimes, toolCallID)
	return int(time.Now().UnixMilli() - start)
}

// MarkCancelledToolsFailed sets all in-progress tool calls to failed.
// Called on cancel so the client doesn't show stuck spinners.
func (buf *Buffer) MarkCancelledToolsFailed() []api.ToolCall {
	var changed []api.ToolCall
	for i := range buf.ToolCalls {
		if buf.ToolCalls[i].Status == api.ToolInProgress || buf.ToolCalls[i].Status == api.ToolPending {
			buf.ToolCalls[i].Status = api.ToolFailed
			changed = append(changed, buf.ToolCalls[i])
		}
	}
	return changed
}

// WritePartial rewrites the partial file with the current buffer state.
func (buf *Buffer) WritePartial(ctx context.Context) {
	buf.Partial.Write(ctx, PartialSnapshot{
		MessageID: buf.MessageID,
		Content:   buf.Content.String(),
		Reasoning: buf.Reasoning.String(),
		ToolCalls: buf.ToolCalls,
		Ts:        time.Now().UnixMilli(),
	})
}

// OpenPartial opens the partial recovery file for a chat.
func (buf *Buffer) OpenPartial(path string) {
	buf.Partial.Open(path)
}

// ClosePartial closes the partial file fd and removes the file at path.
func (buf *Buffer) ClosePartial(path string) {
	buf.Partial.CloseAndRemove(path)
}
