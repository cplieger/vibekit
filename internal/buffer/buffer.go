// Package buffer provides the per-turn assistant content buffer: a Buffer accumulates
// streaming deltas until the turn's closer takes it and writes the finalized assistant
// message. One per TURN, owned by the turn record that installed it — there is no per-chat
// store, because a buffer keyed by chat outlives the turn that filled it.
package buffer

import (
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// DefaultOutputCap is the shared byte budget for subprocess output buffers. 64 KiB covers a
// full terminal screen at 200×50 with generous ANSI escapes, well below container limits.
const DefaultOutputCap = 64 * 1024

// nextID mints Buffer.id: unique per buffer for the process lifetime, which is what makes
// the live_turn version collision-free across turns (a per-turn watermark repeats).
var nextID atomic.Uint64

// New returns an empty buffer ready to fold a turn's frames into. One per TURN, installed
// when the turn opens, which is what stops a partial reply outliving the turn that produced
// it — a buffer keyed by chat is one the next turn's frames can extend. The ONE
// constructor: a composite literal would carry id 0 and every such buffer would present
// the same version.
func New() *Buffer {
	return &Buffer{ToolStartTimes: make(map[string]int64), id: nextID.Add(1)}
}

// Buffer accumulates one turn's streaming deltas; Content and Reasoning are sibling streams.
//
// SAFETY: every field is written under mu and read under mu, and that is load-bearing — three
// goroutines touch a live buffer (the dispatch loop filling it, the prompt handler latching the
// model, the SSE connect handler calling SnapshotCapped). The exported fields are a READ surface for
// code on the dispatch loop, so add a guarded method rather than writing one from outside this
// package. A turn's CLOSER is not on the dispatch loop either and reads through TakeTurn.
type Buffer struct {
	ToolStartTimes map[string]int64
	ToolCallIndex  map[string]int
	ChangedFiles   map[string]*vibekit.FileChange
	MessageID      string
	// KASMessageID is the id KAS's own session log holds this segment's message under
	// (`<uuid>-say`), so a later replay can pair its projected row with the persisted
	// one. KAS's id space, never the MessageID above, which is vibekit's own.
	KASMessageID string
	// steerCarry is text withheld because it might still grow into a steering acknowledgement
	// marker; steerCarrySubtask attributes the delta it came from, so a flush puts the bytes
	// back in their own block. A marker can straddle any number of chunk boundaries, so the
	// withheld bytes need a per-turn home. The RULES live in translate/steer_marker.go.
	steerCarry        string
	steerCarrySubtask string
	Content           strings.Builder
	Reasoning         strings.Builder
	// Model is the model that answered this turn, latched when the turn OPENS. Latched
	// rather than read later because the model lives on the Chat, not the Message: a footer
	// reading the session's current model at render time would relabel every historical turn
	// the moment the user switched. First write wins, guarded by mu.
	Model string
	// Refusal marks the in-flight turn as a model refusal, set once from the explanation
	// chunk's _meta.kiro.refusal and persisted at turn end. Guarded by mu.
	Refusal        *vibekit.RefusalInfo
	ToolCalls      []vibekit.ToolCall
	Blocks         []vibekit.Block
	CodeReferences []vibekit.CodeReference
	// chunkSeq counts text/thinking deltas this turn (1-based). Every broadcast chunk carries
	// it as MessageChunkPayload.Seq so a client holding the GET's live_turn snapshot has a
	// watermark and drops deltas already folded in. Read from the Append*Delta returns or
	// from Snapshot.ChunkSeq. It is NOT the version: it moves only on deltas, so a tab that
	// slept across "tool running → completed" would read unchanged.
	chunkSeq int64
	// id and rev compose the live_turn version "<id>:<rev>": id is minted once by New and
	// never written again, rev moves on every write. Only write and read take mu.
	id      uint64
	rev     uint64
	mu      sync.Mutex
	Started bool
	// segmented latches once the turn has been split mid-flight, so the closer knows an
	// earlier segment already carries part of this turn's content. Reported on every
	// TurnContent, because the closer reads the snapshot rather than the fields.
	segmented bool
	// overCap latches once the turn has exceeded maxBufferBytes, so the truncation notice is
	// emitted exactly once: frames keep arriving after the cap, and one notice per frame
	// would be a second defect on top of the silent drop it replaced.
	overCap bool
}

// write runs fn under mu, bumps rev and returns the composed version, so the emitter that
// broadcasts next stamps the version this exact mutation minted and never a later read.
func (buf *Buffer) write(fn func()) string {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	fn()
	buf.rev++
	return buf.versionLocked()
}

// read runs fn under mu and mints nothing.
func (buf *Buffer) read(fn func()) {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	fn()
}

// versionLocked composes the live_turn version. Must be called with mu held.
func (buf *Buffer) versionLocked() string {
	return strconv.FormatUint(buf.id, 10) + ":" + strconv.FormatUint(buf.rev, 10)
}

// Version is the buffer's current live_turn version, for the digest resolver; a reader,
// so it moves nothing.
func (buf *Buffer) Version() (version string) {
	buf.read(func() { version = buf.versionLocked() })
	return version
}

// MarkOverCap latches the over-cap state and reports whether THIS call was the
// one that set it, so exactly one caller announces the truncation.
func (buf *Buffer) MarkOverCap() (first bool, version string) {
	version = buf.write(func() {
		if buf.overCap {
			return
		}
		buf.overCap = true
		first = true
	})
	return first, version
}

// StartTurn latches the turn's message id and reports whether THIS call opened the turn, so
// exactly one caller announces it. Both fields move under ONE lock, which is what makes the
// pair atomic to a reader: SnapshotCapped keys on MessageID while the code-reference handler keys on
// Started, so setting them in sequence left a window where Started was true and MessageID empty.
func (buf *Buffer) StartTurn(messageID string) (opened bool, version string) {
	version = buf.write(func() {
		if buf.Started {
			return
		}
		buf.Started = true
		buf.MessageID = messageID
		opened = true
	})
	return opened, version
}

// BufferedBytes is the turn's accumulated content plus reasoning length, for a
// caller enforcing a byte cap on the pair.
func (buf *Buffer) BufferedBytes() (n int) {
	buf.read(func() { n = buf.Content.Len() + buf.Reasoning.Len() })
	return n
}

// TurnContent is everything a turn's closer needs from its buffer, read in ONE critical
// section. Closers run on the settling goroutine while the dispatch loop may still be folding,
// so field-by-field reads race a strings.Builder and three slices and can persist a torn
// Content as the turn's final text. Slices and maps are COPIES for the same reason.
type TurnContent struct {
	ChangedFiles map[string]*vibekit.FileChange
	Refusal      *vibekit.RefusalInfo
	// MessageID is the id the streamed message went out under, and the id the
	// persisted message keeps.
	MessageID      string
	KASMessageID   string
	Model          string
	Content        string
	Reasoning      string
	ToolCalls      []vibekit.ToolCall
	Blocks         []vibekit.Block
	CodeReferences []vibekit.CodeReference
	// Started is whether a message id was minted and a message_created went out. Not the same
	// question as EmittedNothing: a turn whose every delta was withheld still started, and
	// its empty message is the outcome's carrier.
	Started bool
	// EmittedNothing is whether the turn produced no content of any kind — all FOUR
	// accumulators, so a thinking-only turn counts and a future block kind in neither builder
	// still does. Deliberately NOT SnapshotCapped's early-return predicate, which omits Blocks: that
	// asks whether a message is worth sending, this asks whether the model produced anything.
	EmittedNothing bool
	// Segmented is whether this turn was split mid-flight, so an earlier segment is already
	// persisted as its own message. The closer reads it because a split turn can close with
	// nothing left to persist, leaving its credits and changed files no message to stamp.
	Segmented bool
}

// TakeTurn returns the turn's whole content as of one instant. The buffer is NOT reset: the
// turn record owns it and is dropped with the turn, so a second closer losing the claim must
// still read the same thing. ChangedFiles is copied DEEP, because a shallow clone shares its
// *FileChange values and TrackFileChanges mutates them in place; Refusal is shared
// deliberately, being written once and never mutated after.
func (buf *Buffer) TakeTurn() (snap TurnContent) {
	buf.read(func() { snap = buf.contentLocked() })
	return snap
}

// SplitSegment seals what the turn has produced so far into its own snapshot and readies the
// buffer for the rest, so a boundary INSIDE a turn can be a sibling message. Only the
// per-MESSAGE fields reset: ChangedFiles is cumulative, Model is latched for the whole turn,
// and chunkSeq must stay monotonic because a reconnecting client's watermark is compared to it.
// A turn that emitted nothing is left untouched, Segmented included.
func (buf *Buffer) SplitSegment() (snap TurnContent, version string) {
	version = buf.write(func() {
		snap = buf.contentLocked()
		// ABOVE the early return, unlike every reset below: the latch runs before the steer
		// filter's empty-text return, so a marker-only delta can latch record X's id in a
		// segment that emits nothing and carry it into a segment whose frames are record Y's
		// — measured different in 116 of 116 spans. MessageID and Started deliberately survive
		// that return because the streamed reply is still going out under that id and every
		// client holds it, while this one reaches no client and is read only at a seal — and
		// a seal that emitted nothing persists no row.
		buf.KASMessageID = ""
		// EmittedNothing rather than !Started: StartTurn mints the id before any delta
		// arrives, so a boundary in that window would seal an empty message and strand the
		// id the streamed reply is still going out under.
		if snap.EmittedNothing {
			return
		}
		buf.segmented = true
		snap.Segmented = true
		buf.Content.Reset()
		buf.Reasoning.Reset()
		buf.ToolCalls = nil
		buf.ToolCallIndex = nil
		buf.Blocks = nil
		buf.CodeReferences = nil
		// Refusal describes the segment that recorded it, not the rest of the turn.
		buf.Refusal = nil
		buf.Started = false
		buf.MessageID = ""
	})
	return snap, version
}

// ToolsSettled reports whether every buffered tool call reached a terminal status, and true for
// a buffer holding none. False is what makes a mid-turn split unsafe: an update resolves against
// the CURRENT buffer, so a call still in flight at the split can never be written back and its
// card stays a spinner in the message already on disk.
//
// MarkInFlightToolsAborted and HasToolInFlightSince spell a narrower TWO-value predicate this
// deliberately leaves alone: !Terminal() there would settle a status neither recognises, since
// the wire status is taken verbatim and KAS spells an unfinished call `executing`.
func (buf *Buffer) ToolsSettled() (settled bool) {
	buf.read(func() {
		for i := range buf.ToolCalls {
			if !buf.ToolCalls[i].Status.Terminal() {
				return
			}
		}
		settled = true
	})
	return settled
}

// contentLocked is the one capture of a buffer's content, shared by TakeTurn and SplitSegment
// so the two cannot report a turn differently. Must be called with mu held.
func (buf *Buffer) contentLocked() TurnContent {
	var changed map[string]*vibekit.FileChange
	if buf.ChangedFiles != nil {
		changed = make(map[string]*vibekit.FileChange, len(buf.ChangedFiles))
		for path, fc := range buf.ChangedFiles {
			copied := *fc
			changed[path] = &copied
		}
	}
	return TurnContent{
		ChangedFiles:   changed,
		Refusal:        buf.Refusal,
		MessageID:      buf.MessageID,
		KASMessageID:   buf.KASMessageID,
		Model:          buf.Model,
		Content:        buf.Content.String(),
		Reasoning:      buf.Reasoning.String(),
		ToolCalls:      slices.Clone(buf.ToolCalls),
		Blocks:         slices.Clone(buf.Blocks),
		CodeReferences: slices.Clone(buf.CodeReferences),
		Started:        buf.Started,
		EmittedNothing: buf.Content.Len() == 0 &&
			buf.Reasoning.Len() == 0 &&
			len(buf.ToolCalls) == 0 &&
			len(buf.Blocks) == 0,
		Segmented: buf.segmented,
	}
}

// AppendToolCall records a newly opened tool call and returns its index, keeping the id index
// in step with the slice under one lock. By pointer because the struct is 264 bytes; the append
// copies it, so the caller keeps ownership.
func (buf *Buffer) AppendToolCall(call *vibekit.ToolCall) (idx int, version string) {
	version = buf.write(func() {
		buf.ToolCalls = append(buf.ToolCalls, *call)
		if buf.ToolCallIndex == nil {
			buf.ToolCallIndex = make(map[string]int)
		}
		idx = len(buf.ToolCalls) - 1
		buf.ToolCallIndex[call.ID] = idx
	})
	return idx, version
}

// ToolCall returns a COPY of the buffered call with the given id, plus the index SetToolCall
// takes to write it back. A copy rather than a pointer into the slice because folding an update
// calls out to the terminal registry, the line tracker and the event bus, none of which can run
// under this mutex (two re-enter it). Read-modify-write is sound because the dispatch loop is
// the only writer: a concurrent SnapshotCapped sees the whole old call or the whole new one.
func (buf *Buffer) ToolCall(id string) (call vibekit.ToolCall, idx int, ok bool) {
	buf.read(func() {
		idx, ok = buf.ToolCallIndex[id]
		if !ok || idx < 0 || idx >= len(buf.ToolCalls) {
			idx, ok = 0, false
			return
		}
		call = buf.ToolCalls[idx]
	})
	return call, idx, ok
}

// SetToolCall writes a folded call back at idx. A stale index is dropped rather than panicking:
// nothing removes a tool call mid-turn, so an out-of-range idx means the caller's own bookkeeping
// is wrong and the turn should not die for it.
func (buf *Buffer) SetToolCall(idx int, call *vibekit.ToolCall) string {
	return buf.write(func() {
		if idx < 0 || idx >= len(buf.ToolCalls) {
			return
		}
		buf.ToolCalls[idx] = *call
	})
}

// ToolCallCount is how many tool calls the turn has opened, which the line
// tracker uses as the turn ordinal.
func (buf *Buffer) ToolCallCount() (n int) {
	buf.read(func() { n = len(buf.ToolCalls) })
	return n
}

// SteerCarry returns the withheld marker-candidate text and its attribution.
func (buf *Buffer) SteerCarry() (text, subtaskID string) {
	buf.read(func() { text, subtaskID = buf.steerCarry, buf.steerCarrySubtask })
	return text, subtaskID
}

// SetSteerCarry replaces the withheld text. An empty text clears the attribution too, so a
// released carry leaves nothing stale for the next delta to inherit.
func (buf *Buffer) SetSteerCarry(text, subtaskID string) string {
	return buf.write(func() {
		buf.steerCarry = text
		if text == "" {
			subtaskID = ""
		}
		buf.steerCarrySubtask = subtaskID
	})
}

// AppendCodeReferences merges refs into the turn's licensed-code attributions, deduping by
// (licenseName, repository, url) so neither the KAS fan-out nor a snippet reproduced twice
// yields duplicate chips. Returns the full deduped list, because the client REPLACES its own
// rather than appending. Empty entries are dropped by the caller before this point.
func (buf *Buffer) AppendCodeReferences(refs []vibekit.CodeReference) (all []vibekit.CodeReference, version string) {
	version = buf.write(func() {
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
		// A copy, so the caller's broadcast cannot race a later append mutating the backing
		// array.
		all = slices.Clone(buf.CodeReferences)
	})
	return all, version
}

// SetKASMessageID latches the id KAS records this segment's message under. First write wins,
// mirroring the projection's own adoptTurnIdentity: KAS records one message per say or tool, so
// the first id a segment sees names the record its frames belong to. An empty id is ignored, so a
// frame carrying none leaves the field absent rather than blanking a latched one.
func (buf *Buffer) SetKASMessageID(id string) string {
	return buf.write(func() {
		if id != "" && buf.KASMessageID == "" {
			buf.KASMessageID = id
		}
	})
}

// SetModel records which model is answering this turn. First write wins: a mid-turn switch must
// not rewrite the attribution of work already done under the previous model. An empty model is
// ignored, so an unknowable value leaves the field absent rather than stamping a blank.
func (buf *Buffer) SetModel(model string) string {
	return buf.write(func() {
		if model != "" && buf.Model == "" {
			buf.Model = model
		}
	})
}

// HasModel reports whether the turn's model is already latched, so a fallback caller can skip
// DERIVING one SetModel would refuse to store. Guarded because the latch and the fallback run on
// different goroutines: the prompt handler latches at dispatch while frames are still forwarding.
func (buf *Buffer) HasModel() (has bool) {
	buf.read(func() { has = buf.Model != "" })
	return has
}

// SetRefusal records the turn's model-refusal metadata. First write wins: KAS emits at most one
// refusal chunk per turn, so a duplicate is a replay and keeps the original.
func (buf *Buffer) SetRefusal(r *vibekit.RefusalInfo) string {
	return buf.write(func() {
		if r != nil && buf.Refusal == nil {
			buf.Refusal = r
		}
	})
}

// AppendTextDelta accumulates a text delta into the turn's content and extends this subtask's
// newest block, or starts a new text block when that one is not text. Returns the block index
// and the delta's sequence number, both broadcast on MessageChunkPayload, plus the version the
// write minted; the index is NOT monotonic across a turn, because a delta can address a block
// behind the tail.
//
// The builder write is IN here rather than beside every call because a snapshot needs the two
// consistent. Only a block of the SAME subtask is ever extended, and which one is
// lastBlockOfSubtask's answer rather than the array's tail.
func (buf *Buffer) AppendTextDelta(delta, subtaskID string) (idx int, seq int64, version string) {
	version = buf.write(func() {
		buf.Content.WriteString(delta)
		buf.chunkSeq++
		seq = buf.chunkSeq
		if i := buf.lastBlockOfSubtask(subtaskID); i >= 0 && buf.Blocks[i].Type == vibekit.BlockText {
			buf.Blocks[i].Text += delta
			idx = i
			return
		}
		buf.Blocks = append(buf.Blocks, vibekit.Block{Type: vibekit.BlockText, Text: delta, AgentSubtaskID: subtaskID})
		idx = len(buf.Blocks) - 1
	})
	return idx, seq, version
}

// lastBlockOfSubtask is the index of the newest block belonging to subtaskID, or -1 when the
// turn holds none. Must be called with mu held.
//
// A block of a DIFFERENT subtask is SKIPPED rather than read as a break: a delegate and its
// parent are two streams sharing one array, so with the tail as the only candidate a delegate's
// delta cut the parent's paragraph in half mid-word. A same-subtask block of the WRONG kind IS a
// barrier, because a tool call between two text runs of one stream really happened between them.
func (buf *Buffer) lastBlockOfSubtask(subtaskID string) int {
	for i, b := range slices.Backward(buf.Blocks) {
		if b.AgentSubtaskID == subtaskID {
			return i
		}
	}
	return -1
}

// AppendThinkingDelta is the BlockThinking analogue of AppendTextDelta, accumulating into
// Reasoning rather than Content. One subtask's reasoning chunks share a block until that
// subtask's own tool_use or text block breaks the run.
func (buf *Buffer) AppendThinkingDelta(delta, subtaskID string) (idx int, seq int64, version string) {
	version = buf.write(func() {
		buf.Reasoning.WriteString(delta)
		buf.chunkSeq++
		seq = buf.chunkSeq
		if i := buf.lastBlockOfSubtask(subtaskID); i >= 0 && buf.Blocks[i].Type == vibekit.BlockThinking {
			buf.Blocks[i].Thinking += delta
			idx = i
			return
		}
		buf.Blocks = append(buf.Blocks, vibekit.Block{Type: vibekit.BlockThinking, Thinking: delta, AgentSubtaskID: subtaskID})
		idx = len(buf.Blocks) - 1
	})
	return idx, seq, version
}

// AppendToolUseBlock records a new tool_use block for the given tool call id and returns its
// index. Always a NEW block: one per tool call, never coalesced the way a text run is.
func (buf *Buffer) AppendToolUseBlock(toolCallID, subtaskID string) (idx int, version string) {
	version = buf.write(func() {
		buf.Blocks = append(buf.Blocks, vibekit.Block{Type: vibekit.BlockToolUse, ToolCallID: toolCallID, AgentSubtaskID: subtaskID})
		idx = len(buf.Blocks) - 1
	})
	return idx, version
}

// TrackFileChanges accumulates per-file change stats from tool call diffs.
//
// The counts are a real line diff, not each side's newline count: KAS sends WHOLE-FILE
// OldText/NewText, so counting newlines reported the entire file as removed and re-added for a
// one-line change. Two edits to one file in one turn SUM, which is honest turn churn, and the
// path is recorded even at 0/0 because a no-op write still wrote the file. The deltas are
// computed BEFORE the lock, which also serves SnapshotCapped on the streaming goroutine. With
// no delta to record the buffer is not written and the CURRENT version is returned, never an
// empty string, so an emitter stamping from it stamps a real value.
func (buf *Buffer) TrackFileChanges(diffs []vibekit.ToolDiff, isNewFile bool) string {
	type delta struct {
		path    string
		added   int
		removed int
	}
	deltas := make([]delta, 0, len(diffs))
	for _, d := range diffs {
		if d.Path == "" {
			continue
		}
		added, removed := lineDelta(d.OldText, d.NewText)
		deltas = append(deltas, delta{path: d.Path, added: added, removed: removed})
	}
	if len(deltas) == 0 {
		return buf.Version()
	}
	return buf.write(func() {
		if buf.ChangedFiles == nil {
			buf.ChangedFiles = make(map[string]*vibekit.FileChange)
		}
		for _, d := range deltas {
			fc, ok := buf.ChangedFiles[d.path]
			if !ok {
				fc = &vibekit.FileChange{IsNewFile: isNewFile}
				buf.ChangedFiles[d.path] = fc
			}
			fc.LinesAdded += d.added
			fc.LinesRemoved += d.removed
		}
	})
}

// RecordToolStart records a tool call's start time so DurationMs is computable on completion.
func (buf *Buffer) RecordToolStart(toolCallID string) string {
	return buf.write(func() {
		if buf.ToolStartTimes == nil {
			buf.ToolStartTimes = make(map[string]int64)
		}
		buf.ToolStartTimes[toolCallID] = time.Now().UnixMilli()
	})
}

// ComputeDuration returns the elapsed time since the tool started, or 0 if it was not recorded.
func (buf *Buffer) ComputeDuration(toolCallID string) (ms int, version string) {
	version = buf.write(func() {
		start, ok := buf.ToolStartTimes[toolCallID]
		if !ok {
			return
		}
		delete(buf.ToolStartTimes, toolCallID)
		ms = int(time.Now().UnixMilli() - start)
	})
	return ms, version
}

// HasToolInFlightSince reports whether a tool call is in flight whose recorded
// start is at or after since.
//
// The cutoff is what makes this safe to extend a silence budget with. An
// unbounded "is anything in flight" answers true forever for a status the turn
// left behind — a tool whose result frame never arrived because the bridge
// wedged — so a budget consulting it never expires and the turn it bounds is
// never reaped. Asking whether the work BEGAN inside the window separates a
// tool that is genuinely running from one that is merely still marked running.
//
// A tool with no recorded start cannot be shown to have begun inside the window
// and so answers false. On the live path RecordToolStart runs on every create
// frame, so the only buffer that reaches this with a gap is the session/load
// replay projection's, which no budget watches.
func (buf *Buffer) HasToolInFlightSince(since time.Time) (inFlight bool) {
	cutoff := since.UnixMilli()
	buf.read(func() {
		for i := range buf.ToolCalls {
			if buf.ToolCalls[i].Status != vibekit.ToolInProgress && buf.ToolCalls[i].Status != vibekit.ToolPending {
				continue
			}
			if start, ok := buf.ToolStartTimes[buf.ToolCalls[i].ID]; ok && start >= cutoff {
				inFlight = true
				return
			}
		}
	})
	return inFlight
}

// MarkInFlightToolsAborted settles every in-progress tool call as aborted and returns the turn's
// message id alongside them, so a close leaves no stuck spinners. `aborted` rather than `failed`
// because nothing malfunctioned: the turn ended under the call. The id travels WITH the calls
// because the caller broadcasts each one keyed by it, and reading it separately would be an
// unguarded field read off the dispatch goroutine.
func (buf *Buffer) MarkInFlightToolsAborted() (messageID string, changed []vibekit.ToolCall, version string) {
	version = buf.write(func() {
		for i := range buf.ToolCalls {
			if buf.ToolCalls[i].Status == vibekit.ToolInProgress || buf.ToolCalls[i].Status == vibekit.ToolPending {
				buf.ToolCalls[i].Status = vibekit.ToolAborted
				changed = append(changed, buf.ToolCalls[i])
			}
		}
		messageID = buf.MessageID
	})
	return messageID, changed, version
}
