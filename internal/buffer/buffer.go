// Package buffer provides the per-chat assistant turn buffering subsystem.
//
// Buffer accumulates streaming deltas per chat until turn_ended writes
// the finalized assistant message to the chat file. Store is a
// concurrency-safe map of per-chat buffers.
package buffer

import (
	"slices"
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
	MessageID      string
	// steerCarry is text withheld from this turn because it might still grow
	// into a steering acknowledgement marker, and steerCarrySubtask is the
	// attribution of the delta it came from — kept alongside so a flush can put
	// the bytes back in the block they belong to rather than guessing.
	//
	// Marker stripping has to happen mid-stream (KAS never scrubs the marker, so
	// it arrives inside ordinary text deltas) and a marker can straddle any
	// number of chunk boundaries. The withheld bytes therefore need a per-turn
	// home with a single writer, which is exactly what this buffer already is.
	// The rules for what may be withheld live with the filter in
	// translate/steer_marker.go; this is only the storage.
	steerCarry        string
	steerCarrySubtask string
	Content           strings.Builder
	Reasoning         strings.Builder
	// Model is the model that answered this turn, latched when the turn OPENS
	// and persisted onto the final assistant message at turn end.
	//
	// Latched rather than read later, and that is the whole point: the model
	// lives on the Chat, not the Message, so a footer that read the session's
	// current model at render time would relabel every historical turn the
	// moment the user switched models — confidently lying about history. A
	// latch also survives a mid-turn switch honestly, because the value
	// recorded is the one that was running when the turn started.
	//
	// Same shape as Refusal below (first write wins, guarded by mu) so both
	// callers of assistantTurnMessage pick it up with no signature change.
	Model string
	// Refusal marks the in-flight turn as a model refusal (kiro-cli 2.13):
	// set once from the refusal explanation chunk's _meta.kiro.refusal,
	// persisted onto the final assistant message at turn end. Guarded by mu.
	Refusal        *api.RefusalInfo
	ToolCalls      []api.ToolCall
	Blocks         []api.Block
	CodeReferences []api.CodeReference
	// chunkSeq counts text/thinking deltas this turn (1-based). Each
	// broadcast chunk carries its value (MessageChunkPayload.Seq) so
	// the connect-time turn_state snapshot can carry a watermark and
	// clients can drop deltas the snapshot already folded in. Guarded
	// by mu like the blocks it counts, and read either from the
	// Append*Delta return values or from Snapshot — which is this
	// buffer's own cross-goroutine read, replacing the hub-side replica
	// that used to be the snapshot surface.
	chunkSeq int64
	mu       sync.Mutex
	Started  bool
	// overCap latches once the turn has exceeded maxBufferBytes, so the
	// truncation notice and its log line are emitted exactly once. Frames keep
	// arriving after the cap is hit, and one notice per frame would be a second
	// defect on top of the silent drop it replaced.
	overCap bool
}

// MarkOverCap latches the over-cap state and reports whether THIS call was the
// one that set it, so exactly one caller announces the truncation.
func (buf *Buffer) MarkOverCap() bool {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if buf.overCap {
		return false
	}
	buf.overCap = true
	return true
}

// SteerCarry returns the withheld marker-candidate text and its attribution.
func (buf *Buffer) SteerCarry() (text, subtaskID string) {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	return buf.steerCarry, buf.steerCarrySubtask
}

// SetSteerCarry replaces the withheld text. An empty text clears the
// attribution too, so a released carry leaves nothing stale behind for the next
// delta to inherit.
func (buf *Buffer) SetSteerCarry(text, subtaskID string) {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	buf.steerCarry = text
	if text == "" {
		subtaskID = ""
	}
	buf.steerCarrySubtask = subtaskID
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

// SetModel records which model is answering this turn. First write wins: a
// mid-turn switch must not rewrite the attribution of work already done under
// the previous model. An empty model is ignored, so an unknowable value leaves
// the field absent rather than stamping a blank.
func (buf *Buffer) SetModel(model string) {
	if model == "" {
		return
	}
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if buf.Model == "" {
		buf.Model = model
	}
}

// HasModel reports whether the turn's model has already been latched, so a
// fallback caller can skip DERIVING one it would not be allowed to store
// (SetModel is first-write-wins). Guarded, because the latch and the fallback
// run on different goroutines: the prompt handler latches at dispatch while the
// dispatch loop is still forwarding frames.
func (buf *Buffer) HasModel() bool {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	return buf.Model != ""
}

// SetRefusal records the turn's model-refusal metadata (first write wins —
// KAS emits at most one refusal chunk per turn, so a duplicate is a replay
// and keeps the original).
func (buf *Buffer) SetRefusal(r *api.RefusalInfo) {
	if r == nil {
		return
	}
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if buf.Refusal == nil {
		buf.Refusal = r
	}
}

// AppendTextDelta extends the last text block with a delta, or starts
// a new text block if the trailing block isn't text. Returns the index
// of the (possibly new) text block — broadcast on
// MessageChunkPayload.BlockIndex so the client knows which block the
// delta belongs to — and the delta's sequence number (broadcast as
// MessageChunkPayload.Seq; see the chunkSeq field).
//
// subtaskID attributes the block to the agent that produced it ("" =
// top-level agent). A trailing text block is only extended when it
// belongs to the SAME subtask; a differing subtask starts a new block
// so a subagent's text never merges into the parent's trailing block.
func (buf *Buffer) AppendTextDelta(delta, subtaskID string) (idx int, seq int64) {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	buf.chunkSeq++
	if n := len(buf.Blocks); n > 0 && buf.Blocks[n-1].Type == api.BlockText && buf.Blocks[n-1].AgentSubtaskID == subtaskID {
		buf.Blocks[n-1].Text += delta
		return n - 1, buf.chunkSeq
	}
	buf.Blocks = append(buf.Blocks, api.Block{Type: api.BlockText, Text: delta, AgentSubtaskID: subtaskID})
	return len(buf.Blocks) - 1, buf.chunkSeq
}

// AppendThinkingDelta is the BlockThinking analogue of AppendTextDelta.
// Reasoning chunks share a block until a non-thinking block (tool_use
// or text) breaks the run — or until the subtask id differs, so a
// subagent's reasoning starts its own block rather than merging into
// the parent's trailing thinking block. subtaskID attributes the block
// to the producing agent ("" = top-level).
func (buf *Buffer) AppendThinkingDelta(delta, subtaskID string) (idx int, seq int64) {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	buf.chunkSeq++
	if n := len(buf.Blocks); n > 0 && buf.Blocks[n-1].Type == api.BlockThinking && buf.Blocks[n-1].AgentSubtaskID == subtaskID {
		buf.Blocks[n-1].Thinking += delta
		return n - 1, buf.chunkSeq
	}
	buf.Blocks = append(buf.Blocks, api.Block{Type: api.BlockThinking, Thinking: delta, AgentSubtaskID: subtaskID})
	return len(buf.Blocks) - 1, buf.chunkSeq
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

// Snapshot returns the in-flight turn as an api.Message plus the chunk-sequence
// watermark, for a client that connects mid-turn and needs the accumulated
// transcript rather than only the next delta.
//
// This is the buffer serving its own cross-goroutine read, which the hub used to
// keep a whole SECOND replica for (hub/turn_mirror.go re-folded every broadcast
// event into a parallel api.Message — a duplicate implementation of the block
// assembly happening right here, and one that could drift from it). Everything
// that snapshot needs is already in these fields; the only thing missing was a
// guarded reader, so this is it.
//
// Reports false when the turn has produced nothing yet, which the caller sends
// as a bare busy signal instead of an empty message.
func (buf *Buffer) Snapshot() (api.Message, int64, bool) {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if buf.MessageID == "" {
		return api.Message{}, 0, false
	}
	if buf.Content.Len() == 0 && buf.Reasoning.Len() == 0 && len(buf.ToolCalls) == 0 {
		return api.Message{}, buf.chunkSeq, false
	}
	// Field-for-field the same shape bridge_coord assembles at turn end, so a
	// mid-turn snapshot renders byte-equivalently to the turn that follows it.
	// Slices are copied: the caller reads them off this goroutine while the
	// dispatch loop keeps appending.
	return api.Message{
		ID:             buf.MessageID,
		Role:           api.RoleAssistant,
		Ts:             time.Now().UnixMilli(),
		Content:        buf.Content.String(),
		Reasoning:      buf.Reasoning.String(),
		ToolCalls:      slices.Clone(buf.ToolCalls),
		Blocks:         slices.Clone(buf.Blocks),
		CodeReferences: slices.Clone(buf.CodeReferences),
		Refusal:        buf.Refusal,
	}, buf.chunkSeq, true
}
