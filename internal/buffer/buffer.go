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

	"github.com/cplieger/vibekit/internal/vibekit"
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
// SAFETY: every field of a Buffer is written under mu and read under mu, and
// that is not belt-and-braces — it is load-bearing. Three goroutines touch a
// live buffer: the per-chat dispatch loop that fills it, the prompt handler that
// latches the turn's model at dispatch, and the SSE connect handler that calls
// Snapshot for a client joining mid-turn. The exported fields are therefore a
// READ surface for code running on the dispatch loop; a write to one from
// outside this package races Snapshot, and did — measured with -race on
// go1.27.0, Buffer.MessageID written at translate/streaming_tools.go:436 against
// Snapshot's read here. Add a guarded method rather than a field write.
type Buffer struct {
	ToolStartTimes map[string]int64
	ToolCallIndex  map[string]int
	ChangedFiles   map[string]*vibekit.FileChange
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
	Refusal        *vibekit.RefusalInfo
	ToolCalls      []vibekit.ToolCall
	Blocks         []vibekit.Block
	CodeReferences []vibekit.CodeReference
	// chunkSeq counts text/thinking deltas this turn (1-based). Each
	// broadcast chunk carries its value (MessageChunkPayload.Seq) so
	// the connect-time turn_state snapshot can carry a watermark and
	// clients can drop deltas the snapshot already folded in. Guarded
	// by mu like the blocks it counts, and read either from the
	// Append*Delta return values or from Snapshot — which is this
	// buffer's own cross-goroutine read, replacing the runtime-side replica
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

// StartTurn latches the turn's message id and reports whether THIS call opened
// the turn, so exactly one caller announces it. Same latch-and-report shape as
// MarkOverCap above.
//
// Both fields move under ONE lock, which is what makes the pair atomic to a
// reader: Snapshot keys on MessageID and the code-reference handler keys on
// Started, so setting them in sequence left a window where Started was true and
// MessageID was still empty.
func (buf *Buffer) StartTurn(messageID string) bool {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if buf.Started {
		return false
	}
	buf.Started = true
	buf.MessageID = messageID
	return true
}

// BufferedBytes is the turn's accumulated content plus reasoning length, for a
// caller enforcing a byte cap on the pair.
func (buf *Buffer) BufferedBytes() int {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	return buf.Content.Len() + buf.Reasoning.Len()
}

// EmittedNothing reports whether the turn has produced no content of any kind.
//
// All FOUR accumulators, not just Content and ToolCalls: a turn that streamed
// only thinking (an agent_thought_chunk with no agent_message_chunk and no tool
// call) has emitted something, and Blocks is counted because it is the canonical
// chronological array the client renders from, so a future block kind that lands
// in neither builder still counts.
//
// This lives here rather than at its caller because the four fields are exported
// and documented "guarded by mu", and the caller read them from a DIFFERENT
// goroutine than the dispatch loop that appends to them — a mutex every method
// in this file takes and one caller bypassed through the field. The answer is
// only meaningful as of one instant anyway, so it has to be computed under the
// same lock that makes the four fields agree with each other.
//
// Deliberately NOT the same predicate as Snapshot's early return, which tests
// three of these four and omits Blocks. Snapshot is asking "is there a message
// worth sending"; this asks "did the model produce anything at all". Unifying
// them would change what Snapshot returns for a turn holding only a tool-use
// block, which is a wire question, not a locking one.
func (buf *Buffer) EmittedNothing() bool {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	return buf.Content.Len() == 0 &&
		buf.Reasoning.Len() == 0 &&
		len(buf.ToolCalls) == 0 &&
		len(buf.Blocks) == 0
}

// AppendToolCall records a newly opened tool call and returns its index, keeping
// the id index in step with the slice under one lock. By pointer because the
// struct is 264 bytes; the append copies it, so the caller keeps ownership.
func (buf *Buffer) AppendToolCall(call *vibekit.ToolCall) int {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	buf.ToolCalls = append(buf.ToolCalls, *call)
	if buf.ToolCallIndex == nil {
		buf.ToolCallIndex = make(map[string]int)
	}
	idx := len(buf.ToolCalls) - 1
	buf.ToolCallIndex[call.ID] = idx
	return idx
}

// ToolCall returns a COPY of the buffered call with the given id, plus the index
// SetToolCall takes to write it back.
//
// A copy rather than the pointer the caller used to take into the slice, because
// folding an update touches a dozen fields and calls out to the terminal
// registry, the line tracker and the event bus along the way — none of which can
// run under this mutex (two of them re-enter it). Read-modify-write is sound
// because the dispatch loop is the only writer: a concurrent Snapshot sees
// either the whole old call or the whole new one, never a half-folded update.
func (buf *Buffer) ToolCall(id string) (call vibekit.ToolCall, idx int, ok bool) {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	idx, ok = buf.ToolCallIndex[id]
	if !ok || idx < 0 || idx >= len(buf.ToolCalls) {
		return vibekit.ToolCall{}, 0, false
	}
	return buf.ToolCalls[idx], idx, true
}

// SetToolCall writes a folded call back at idx. A stale index is dropped rather
// than panicking: nothing removes a tool call mid-turn, so an out-of-range idx
// means the caller's own bookkeeping is wrong and the turn should not die for it.
func (buf *Buffer) SetToolCall(idx int, call *vibekit.ToolCall) {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if idx < 0 || idx >= len(buf.ToolCalls) {
		return
	}
	buf.ToolCalls[idx] = *call
}

// ToolCallCount is how many tool calls the turn has opened, which the line
// tracker uses as the turn ordinal.
func (buf *Buffer) ToolCallCount() int {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	return len(buf.ToolCalls)
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
func (buf *Buffer) AppendCodeReferences(refs []vibekit.CodeReference) []vibekit.CodeReference {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	seen := make(map[vibekit.CodeReference]struct{}, len(buf.CodeReferences))
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
	return slices.Clone(buf.CodeReferences)
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
func (buf *Buffer) SetRefusal(r *vibekit.RefusalInfo) {
	if r == nil {
		return
	}
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if buf.Refusal == nil {
		buf.Refusal = r
	}
}

// AppendTextDelta accumulates a text delta into the turn's content and extends
// this subtask's newest block with it, or starts a new text block when that
// block isn't text. Returns the index of the (possibly new) text block —
// broadcast on MessageChunkPayload.BlockIndex so the client knows which block the
// delta belongs to — and the delta's sequence number (broadcast as
// MessageChunkPayload.Seq; see the chunkSeq field).
//
// The returned index is NOT monotonic across a turn: a delta extending a block an
// interleaved delegate has since appended past addresses a block behind the tail.
//
// The builder write is IN here rather than beside every call, because all five
// callers did both and a Snapshot needs them consistent: written separately, a
// mid-turn reader could see the block without the content or the content without
// the block, and the two are what it assembles the message from.
//
// subtaskID attributes the block to the agent that produced it ("" =
// top-level agent), and only a block belonging to the SAME subtask is
// ever extended, so a subagent's text never merges into the parent's.
// Which of that subtask's blocks is the candidate is lastBlockOfSubtask's
// answer, not the array's tail.
func (buf *Buffer) AppendTextDelta(delta, subtaskID string) (idx int, seq int64) {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	buf.Content.WriteString(delta)
	buf.chunkSeq++
	if i := buf.lastBlockOfSubtask(subtaskID); i >= 0 && buf.Blocks[i].Type == vibekit.BlockText {
		buf.Blocks[i].Text += delta
		return i, buf.chunkSeq
	}
	buf.Blocks = append(buf.Blocks, vibekit.Block{Type: vibekit.BlockText, Text: delta, AgentSubtaskID: subtaskID})
	return len(buf.Blocks) - 1, buf.chunkSeq
}

// lastBlockOfSubtask is the index of the newest block belonging to subtaskID, or
// -1 when the turn holds none. Must be called with mu held.
//
// A block of a DIFFERENT subtask is SKIPPED rather than read as a break, because
// a delegate and its parent are two independent streams sharing one array: with
// the tail as the only candidate, a delegate's delta landing between two of the
// parent's cut the parent's paragraph in half, and the client renders
// non-contiguous halves as separate bubbles — so one visible break per interleave
// point, mid-word, with any markdown straddling the cut rendered literally.
//
// A same-subtask block of the WRONG kind is a barrier and is not skipped. That is
// the chronology: a tool call between two text runs of one stream really happened
// between them, so merging across it would reorder the transcript.
//
// The scan is O(blocks since this subtask's last one), which is O(1) for the
// common single-stream turn (its own block IS the tail). A per-subtask index
// would make it constant in the interleaved case too, and is deliberately not
// here: the scan is auditable and the observed turns hold tens of blocks.
func (buf *Buffer) lastBlockOfSubtask(subtaskID string) int {
	for i, b := range slices.Backward(buf.Blocks) {
		if b.AgentSubtaskID == subtaskID {
			return i
		}
	}
	return -1
}

// AppendThinkingDelta is the BlockThinking analogue of AppendTextDelta, and
// accumulates into Reasoning rather than Content. One subtask's reasoning chunks
// share a block until that subtask's own tool_use or text block breaks the run;
// another subtask's blocks are skipped, so an interleaved delegate never splits
// the parent's reasoning. subtaskID attributes the block to the producing agent
// ("" = top-level).
func (buf *Buffer) AppendThinkingDelta(delta, subtaskID string) (idx int, seq int64) {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	buf.Reasoning.WriteString(delta)
	buf.chunkSeq++
	if i := buf.lastBlockOfSubtask(subtaskID); i >= 0 && buf.Blocks[i].Type == vibekit.BlockThinking {
		buf.Blocks[i].Thinking += delta
		return i, buf.chunkSeq
	}
	buf.Blocks = append(buf.Blocks, vibekit.Block{Type: vibekit.BlockThinking, Thinking: delta, AgentSubtaskID: subtaskID})
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
	buf.Blocks = append(buf.Blocks, vibekit.Block{Type: vibekit.BlockToolUse, ToolCallID: toolCallID, AgentSubtaskID: subtaskID})
	return len(buf.Blocks) - 1
}

// TrackFileChanges accumulates per-file change stats from tool call diffs.
func (buf *Buffer) TrackFileChanges(diffs []vibekit.ToolDiff, isNewFile bool) {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if buf.ChangedFiles == nil {
		buf.ChangedFiles = make(map[string]*vibekit.FileChange)
	}
	for _, d := range diffs {
		if d.Path == "" {
			continue
		}
		fc, ok := buf.ChangedFiles[d.Path]
		if !ok {
			fc = &vibekit.FileChange{IsNewFile: isNewFile}
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
func (buf *Buffer) MarkCancelledToolsFailed() []vibekit.ToolCall {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	var changed []vibekit.ToolCall
	for i := range buf.ToolCalls {
		if buf.ToolCalls[i].Status == vibekit.ToolInProgress || buf.ToolCalls[i].Status == vibekit.ToolPending {
			buf.ToolCalls[i].Status = vibekit.ToolFailed
			changed = append(changed, buf.ToolCalls[i])
		}
	}
	return changed
}

// Snapshot returns the in-flight turn as an vibekit.Message plus the chunk-sequence
// watermark, for a client that connects mid-turn and needs the accumulated
// transcript rather than only the next delta.
//
// This is the buffer serving its own cross-goroutine read, which the hub used to
// keep a whole SECOND replica for (agent/turn_mirror.go re-folded every broadcast
// event into a parallel vibekit.Message — a duplicate implementation of the block
// assembly happening right here, and one that could drift from it). Everything
// that snapshot needs is already in these fields; the only thing missing was a
// guarded reader, so this is it.
//
// Reports false when the turn has produced nothing yet, which the caller sends
// as a bare busy signal instead of an empty message.
func (buf *Buffer) Snapshot() (vibekit.Message, int64, bool) {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if buf.MessageID == "" {
		return vibekit.Message{}, 0, false
	}
	if buf.Content.Len() == 0 && buf.Reasoning.Len() == 0 && len(buf.ToolCalls) == 0 {
		return vibekit.Message{}, buf.chunkSeq, false
	}
	// Field-for-field the same shape bridge_coord assembles at turn end, so a
	// mid-turn snapshot renders byte-equivalently to the turn that follows it.
	// Slices are copied: the caller reads them off this goroutine while the
	// dispatch loop keeps appending.
	return vibekit.Message{
		ID:             buf.MessageID,
		Role:           vibekit.RoleAssistant,
		Ts:             time.Now().UnixMilli(),
		Content:        buf.Content.String(),
		Reasoning:      buf.Reasoning.String(),
		ToolCalls:      slices.Clone(buf.ToolCalls),
		Blocks:         slices.Clone(buf.Blocks),
		CodeReferences: slices.Clone(buf.CodeReferences),
		Refusal:        buf.Refusal,
	}, buf.chunkSeq, true
}
