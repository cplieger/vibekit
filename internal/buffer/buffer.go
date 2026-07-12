// Package buffer provides the per-chat assistant turn buffering subsystem.
//
// Buffer accumulates streaming deltas per chat until turn_ended writes
// the finalized assistant message to the chat file. Store is a
// concurrency-safe map of per-chat buffers.
package buffer

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// DefaultOutputCap is the shared byte budget for subprocess output
// buffers (agent terminals, stderr line caps, whoami output, MCP
// prewarm logs). 64 KiB covers a full terminal screen at 200 cols ×
// 50 rows with generous ANSI escapes and is well below container
// memory limits.
const DefaultOutputCap = 64 * 1024

// Buffer accumulates streaming deltas per chat until turn_ended
// writes the finalized assistant message to the chat file.
//
// Content and Reasoning are sibling streams that both fill during a
// single turn — extended-thinking models emit reasoning ("Thinking…")
// deltas alongside the regular response. The translator routes each
// chunk to the appropriate builder based on the upstream IsReasoning
// flag.
//
// SAFETY: Buffer is not goroutine-safe by design — the single-writer
// invariant is enforced by the hub's per-chat dispatch loop. The mu
// field guards against silent corruption if the invariant is ever
// violated by a future refactor.
type Buffer struct {
	ToolStartTimes map[string]int64
	ToolCallIndex  map[string]int
	ChangedFiles   map[string]*api.FileChange
	Partial        *WritingPartial
	MessageID      string
	Content        strings.Builder
	Reasoning      strings.Builder
	ToolCalls      []api.ToolCall
	Blocks         []api.Block
	CodeReferences []api.CodeReference
	mu             sync.Mutex
	Started        bool
}

// AppendCodeReferences merges refs into the turn's licensed-code
// attributions, deduping by (licenseName, repository, url) so the KAS
// fan-out (the same references broadcast under every live session id) and
// a completion that reproduces the same snippet twice don't produce
// duplicate chips. Returns the full deduped list so the caller can
// broadcast it idempotently (the client replaces its list rather than
// appending). Empty entries (no license name) are dropped by the caller
// before this point.
func (buf *Buffer) AppendCodeReferences(refs []api.CodeReference) []api.CodeReference {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	seen := make(map[api.CodeReference]struct{}, len(buf.CodeReferences))
	for _, r := range buf.CodeReferences {
		seen[r] = struct{}{}
	}
	for _, r := range refs {
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		buf.CodeReferences = append(buf.CodeReferences, r)
	}
	// Return a copy so the caller's broadcast can't race a later append
	// mutating the backing array.
	out := make([]api.CodeReference, len(buf.CodeReferences))
	copy(out, buf.CodeReferences)
	return out
}

// AppendTextDelta extends the last text block with a delta, or starts
// a new text block if the trailing block isn't text. Returns the index
// of the (possibly new) text block — broadcast on
// MessageChunkPayload.BlockIndex so the client knows which block the
// delta belongs to.
//
// subtaskID attributes the block to the agent that produced it ("" =
// top-level agent). A trailing text block is only extended when it
// belongs to the SAME subtask; a differing subtask starts a new block
// so a subagent's text never merges into the parent's trailing block.
func (buf *Buffer) AppendTextDelta(delta, subtaskID string) int {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if n := len(buf.Blocks); n > 0 && buf.Blocks[n-1].Type == api.BlockText && buf.Blocks[n-1].AgentSubtaskID == subtaskID {
		buf.Blocks[n-1].Text += delta
		return n - 1
	}
	buf.Blocks = append(buf.Blocks, api.Block{Type: api.BlockText, Text: delta, AgentSubtaskID: subtaskID})
	return len(buf.Blocks) - 1
}

// AppendThinkingDelta is the BlockThinking analogue of AppendTextDelta.
// Reasoning chunks share a block until a non-thinking block (tool_use
// or text) breaks the run — or until the subtask id differs, so a
// subagent's reasoning starts its own block rather than merging into
// the parent's trailing thinking block. subtaskID attributes the block
// to the producing agent ("" = top-level).
func (buf *Buffer) AppendThinkingDelta(delta, subtaskID string) int {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if n := len(buf.Blocks); n > 0 && buf.Blocks[n-1].Type == api.BlockThinking && buf.Blocks[n-1].AgentSubtaskID == subtaskID {
		buf.Blocks[n-1].Thinking += delta
		return n - 1
	}
	buf.Blocks = append(buf.Blocks, api.Block{Type: api.BlockThinking, Thinking: delta, AgentSubtaskID: subtaskID})
	return len(buf.Blocks) - 1
}

// AppendToolUseBlock records a new tool_use block referencing the
// given tool call id. Always allocates a new block (one per tool call,
// even if back-to-back tool calls would coalesce into a single
// "thinking" run for text). Returns the new block's index. subtaskID
// attributes the block to the agent that produced it ("" = top-level).
func (buf *Buffer) AppendToolUseBlock(toolCallID, subtaskID string) int {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	buf.Blocks = append(buf.Blocks, api.Block{Type: api.BlockToolUse, ToolCallID: toolCallID, AgentSubtaskID: subtaskID})
	return len(buf.Blocks) - 1
}

// TrackFileChanges accumulates per-file change stats from tool call diffs.
func (buf *Buffer) TrackFileChanges(diffs []api.ToolDiff, isNewFile bool) {
	buf.mu.Lock()
	defer buf.mu.Unlock()
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
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if buf.ToolStartTimes == nil {
		buf.ToolStartTimes = make(map[string]int64)
	}
	buf.ToolStartTimes[toolCallID] = time.Now().UnixMilli()
}

// ComputeDuration returns the elapsed time since the tool started, or 0
// if the start time was not recorded.
func (buf *Buffer) ComputeDuration(toolCallID string) int {
	buf.mu.Lock()
	defer buf.mu.Unlock()
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
	buf.mu.Lock()
	defer buf.mu.Unlock()
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
// No-op if the partial writer was not opened or is disabled.
func (buf *Buffer) WritePartial(ctx context.Context) {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if buf.Partial == nil {
		return
	}
	buf.Partial.Write(ctx, &PartialSnapshot{
		MessageID:      buf.MessageID,
		Content:        buf.Content.String(),
		Reasoning:      buf.Reasoning.String(),
		ToolCalls:      buf.ToolCalls,
		Blocks:         buf.Blocks,
		CodeReferences: buf.CodeReferences,
		Ts:             time.Now().UnixMilli(),
	})
}

// OpenPartial opens the partial recovery file for a chat. Idempotent: a
// no-op once the file is already open, so it can be called on every
// content/reasoning chunk to open the .partial lazily (the first call
// wins). If opening fails, Partial remains nil (degraded mode).
func (buf *Buffer) OpenPartial(ctx context.Context, path string) {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if buf.Partial != nil {
		return
	}
	buf.Partial = OpenPartial(ctx, path)
}

// ClosePartial flushes the final state, closes the partial file fd,
// and removes the file at path.
func (buf *Buffer) ClosePartial(ctx context.Context, path string) {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if buf.Partial == nil {
		return
	}
	buf.Partial.CloseAndRemove(ctx, path)
	buf.Partial = nil
}
