package buffer

import (
	"slices"
	"time"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// SnapshotCaps bounds every dimension of a turn snapshot. A ZERO in any field leaves that
// dimension unbounded, which is what makes SnapshotCaps{} the unbounded snapshot and lets
// Snapshot be one call into SnapshotCapped rather than a second implementation.
//
// A struct rather than six positional ints: six adjacent ints are an undetectable
// transposition at the call site, and the values are policy numbers a reader has to be able
// to name (#go-rulebook C16).
type SnapshotCaps struct {
	// ReasoningBytes bounds Message.Reasoning, keeping its TAIL.
	ReasoningBytes int
	// ContentBytes bounds Message.Content, keeping its TAIL.
	ContentBytes int
	// BlockTextBytes bounds the SUM of Text+Thinking across Message.Blocks. Reasoning and
	// text are stored TWICE in a Buffer — AppendThinkingDelta writes buf.Reasoning and
	// Blocks[i].Thinking, AppendTextDelta does the same for Content and Blocks[i].Text —
	// and both carriers are consumed client-side, so a cap that reaches only the flat
	// fields halves the payload where it should divide it.
	BlockTextBytes int
	// ToolCalls bounds how many tool calls are carried, keeping the NEWEST.
	ToolCalls int
	// ToolOutputBytes bounds each carried tool call's Output, keeping its TAIL.
	ToolOutputBytes int
	// Blocks bounds how many blocks are carried, keeping the NEWEST.
	Blocks int
}

// MaxTextBytes is the worst-case TEXT the caps admit: the two flat fields, the block array's
// share, and every carried tool call's output. It exists so a per-connect budget and the
// tests read one arithmetic rather than restating it.
//
// Zero means UNBOUNDED, reported whenever any contributing dimension is itself unbounded —
// a partial sum would read as a real ceiling and understate the payload.
func (c SnapshotCaps) MaxTextBytes() int {
	if c.ReasoningBytes <= 0 || c.ContentBytes <= 0 || c.BlockTextBytes <= 0 || c.ToolCalls <= 0 || c.ToolOutputBytes <= 0 {
		return 0
	}
	return c.ReasoningBytes + c.ContentBytes + c.BlockTextBytes + c.ToolCalls*c.ToolOutputBytes
}

// tailBytes keeps the last n bytes of s, advancing the cut forward to the next rune
// boundary, and reports whether anything was dropped. A non-positive n is unbounded.
//
// The advance is not cosmetic: a byte-boundary cut splits a multi-byte rune, and
// encoding/json substitutes U+FFFD for the invalid leading fragment on marshal — so the
// client renders a replacement character at the top of every capped field.
//
// runesafe/v2 is the fleet's home for this rule and has no tail sibling: CapBytes caps the
// HEAD. Adding one there needs a release plus a pin bump, so the gap is recorded rather
// than filled here.
func tailBytes(s string, n int) (string, bool) {
	if n <= 0 || len(s) <= n {
		return s, false
	}
	cut := len(s) - n
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[cut:], true
}

// capBlocks keeps the newest blocks that fit textCap bytes of Text+Thinking, tail-truncating
// the boundary block, then keeps at most countCap of them. Reports whether anything was cut.
func capBlocks(blocks []vibekit.Block, textCap, countCap int) ([]vibekit.Block, bool) {
	out := slices.Clone(blocks)
	truncated := false
	if textCap > 0 {
		remaining := textCap
		keepFrom := len(out)
		for i := len(out) - 1; i >= 0; i-- {
			size := len(out[i].Text) + len(out[i].Thinking)
			if size <= remaining {
				remaining -= size
				keepFrom = i
				continue
			}
			// The boundary block: keep the tail of whatever budget is left, and drop it
			// outright when there is none. Everything older goes either way.
			if remaining > 0 {
				b := out[i]
				b.Text, _ = tailBytes(b.Text, remaining)
				remaining -= len(b.Text)
				b.Thinking, _ = tailBytes(b.Thinking, remaining)
				out[i] = b
				keepFrom = i
			}
			truncated = true
			break
		}
		out = out[keepFrom:]
	}
	if countCap > 0 && len(out) > countCap {
		out = out[len(out)-countCap:]
		truncated = true
	}
	return out, truncated
}

// capToolCalls keeps the newest countCap tool calls with each Output tail-capped to
// outputCap bytes. Reports whether anything was cut.
func capToolCalls(calls []vibekit.ToolCall, countCap, outputCap int) ([]vibekit.ToolCall, bool) {
	out := slices.Clone(calls)
	truncated := false
	if countCap > 0 && len(out) > countCap {
		out = out[len(out)-countCap:]
		truncated = true
	}
	for i := range out {
		capped, cut := tailBytes(out[i].Output, outputCap)
		if cut {
			out[i].Output = capped
			truncated = true
		}
	}
	return out, truncated
}

// SnapshotCapped returns the in-flight turn as a vibekit.Message bounded by caps, plus the
// chunk-sequence watermark, whether anything was withheld, and whether there is a snapshot
// at all. Snapshot is this call with no caps, so there is ONE implementation of the read.
//
// Every text dimension keeps its TAIL, because a mid-turn reconnect wants the reply being
// written now rather than its opening. `truncated` is true when ANY dimension cut anything,
// and it rides the wire so no client can read a bounded payload as a complete one.
func (buf *Buffer) SnapshotCapped(caps SnapshotCaps) (msg vibekit.Message, seq int64, truncated, ok bool) {
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if buf.MessageID == "" {
		return vibekit.Message{}, 0, false, false
	}
	if buf.Content.Len() == 0 && buf.Reasoning.Len() == 0 && len(buf.ToolCalls) == 0 {
		return vibekit.Message{}, buf.chunkSeq, false, false
	}
	content, contentCut := tailBytes(buf.Content.String(), caps.ContentBytes)
	reasoning, reasoningCut := tailBytes(buf.Reasoning.String(), caps.ReasoningBytes)
	blocks, blocksCut := capBlocks(buf.Blocks, caps.BlockTextBytes, caps.Blocks)
	tools, toolsCut := capToolCalls(buf.ToolCalls, caps.ToolCalls, caps.ToolOutputBytes)
	// Field-for-field the shape bridge_coord assembles at turn end, so a mid-turn snapshot
	// renders byte-equivalently to the turn that follows it. Slices are copied: the caller
	// reads them off this goroutine while the dispatch loop keeps appending.
	msg = vibekit.Message{
		ID:             buf.MessageID,
		Role:           vibekit.RoleAssistant,
		Ts:             time.Now().UnixMilli(),
		Content:        content,
		Reasoning:      reasoning,
		ToolCalls:      tools,
		Blocks:         blocks,
		CodeReferences: slices.Clone(buf.CodeReferences),
		Refusal:        buf.Refusal,
	}
	return msg, buf.chunkSeq, contentCut || reasoningCut || blocksCut || toolsCut, true
}
