package buffer

import (
	"slices"
	"time"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// SnapshotCaps bounds every dimension of a turn snapshot. A ZERO in any field leaves that
// dimension unbounded, which makes SnapshotCaps{} the unbounded read and lets an uncapped
// caller go through SnapshotCapped rather than a second implementation. A struct rather
// than six positional ints, which would be an undetectable transposition at the call site.
type SnapshotCaps struct {
	// ReasoningBytes bounds Message.Reasoning, keeping its TAIL.
	ReasoningBytes int
	// ContentBytes bounds Message.Content, keeping its TAIL.
	ContentBytes int
	// BlockTextBytes bounds the SUM of Text+Thinking across Message.Blocks. Reasoning and
	// text are each stored TWICE in a Buffer, once flat and once in a block, and both
	// carriers are consumed client-side — so a cap reaching only the flat fields halves
	// the payload where it should divide it.
	BlockTextBytes int
	// ToolCalls bounds how many tool calls are carried, keeping the NEWEST.
	ToolCalls int
	// ToolOutputBytes bounds each carried tool call's Output, keeping its TAIL.
	ToolOutputBytes int
	// ToolOutputTotalBytes bounds the SUM of the carried outputs, keeping the NEWEST
	// calls. It exists because the per-call cap times the call count is an arithmetic
	// ceiling nobody would accept as a real bound: a 64 KiB tail on 4096 calls is 268 MiB.
	// This is the only dimension a caller can raise the per-call cap under while still
	// stating a total, which is what lets liveTurnGETCaps keep the terminal ring buffer's
	// own 64 KiB bound AND have a checkable ceiling. Zero is UNBOUNDED.
	ToolOutputTotalBytes int
	// Blocks bounds how many blocks are carried, keeping the NEWEST.
	Blocks int
}

// tailBytes keeps the last n bytes of s, advancing the cut forward to the next rune
// boundary, and reports whether anything was dropped. A non-positive n is unbounded.
//
// The advance is not cosmetic: a byte-boundary cut splits a multi-byte rune, and
// encoding/json substitutes U+FFFD for the invalid leading fragment on marshal — so the
// client renders a replacement character at the top of every capped field.
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
// the boundary block, then keeps at most countCap of them. Reports the base — the index in
// `blocks` that out[0] came from, summed across BOTH cap dimensions — and whether anything
// was cut.
//
// The base is what lets a reader address an ABSOLUTE block index against the window: both
// slices below re-index from zero, so without it a caller holding out[] and a chunk naming
// block N have no way to meet.
func capBlocks(blocks []vibekit.Block, textCap, countCap int) ([]vibekit.Block, int, bool) {
	out := slices.Clone(blocks)
	base := 0
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
		// keepFrom IS the base for this dimension, on both arms of the boundary: a block
		// kept tail-truncated leaves keepFrom at its own index, and one dropped outright
		// leaves it at the index above.
		base += keepFrom
	}
	if countCap > 0 && len(out) > countCap {
		// Read BEFORE the slice, and ADDED to whatever the text cap already dropped:
		// out is already re-indexed from zero at this point, so the two dimensions sum.
		base += len(out) - countCap
		out = out[len(out)-countCap:]
		truncated = true
	}
	return out, base, truncated
}

// capToolCalls keeps the newest countCap tool calls with each Output tail-capped to
// outputCap bytes, then bounds the SUM of what survives to totalCap. Reports whether
// anything was cut.
//
// Order is count, then per-call, then the aggregate, and the aggregate walks NEWEST-first
// over the already-capped outputs — keep while it fits, tail-cap the boundary call to the
// remainder, DROP anything older. That mirrors capBlocks' BlockTextBytes handling exactly,
// so there is ONE shape for a text aggregate in this package.
func capToolCalls(calls []vibekit.ToolCall, countCap, outputCap, totalCap int) ([]vibekit.ToolCall, bool) {
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
	if totalCap <= 0 {
		return out, truncated
	}
	remaining := totalCap
	keepFrom := len(out)
	for i := len(out) - 1; i >= 0; i-- {
		size := len(out[i].Output)
		if size <= remaining {
			remaining -= size
			keepFrom = i
			continue
		}
		// The boundary call: keep the tail of whatever budget is left, and drop it
		// outright when there is none. Everything older goes either way.
		if remaining > 0 {
			c := out[i]
			c.Output, _ = tailBytes(c.Output, remaining)
			out[i] = c
			keepFrom = i
		}
		truncated = true
		break
	}
	out = out[keepFrom:]
	return out, truncated
}

// Snapshot is one bounded read of the in-flight turn: the message, plus the three facts a
// reader needs about the READ rather than about the turn.
//
// A STRUCT rather than four positional returns, two of which are adjacent same-kind values:
// a transposed pair compiles and is silent in both directions, and this codebase already
// refuses that shape twice — SnapshotCaps over six positional ints, and vibekit.LiveTurn
// over the four values this call used to answer with.
type Snapshot struct {
	// Message is the turn as accumulated so far, bounded by the caps.
	Message vibekit.Message
	// ChunkSeq is the last delta folded into Message (see MessageChunkPayload.Seq): a
	// client's dedup watermark, so a chunk at or below it is already in here.
	ChunkSeq int64
	// BlockBase is the ABSOLUTE index of Message.Blocks[0] in the buffer's own array. The
	// block caps keep the TAIL and re-index it from zero while a live message_chunk keeps
	// naming the absolute index, so a reader holding this window subtracts the base to
	// place one. Zero is a POSITIVE statement that the window starts at 0, not filler.
	BlockBase int
	// Truncated is whether ANY dimension cut anything, so no reader can take a bounded
	// payload for a complete one.
	Truncated bool
}

// SnapshotCapped returns the in-flight turn bounded by caps, plus whether there is a
// snapshot at all. SnapshotCaps{} is this call with every dimension unbounded, so there is
// ONE implementation of the read.
//
// `ok` stays a separate return rather than a field: it answers "is there a snapshot at
// all", which is Go's comma-ok idiom and the shape Runtime.LiveTurn already answers in.
//
// Every text dimension keeps its TAIL, because a mid-turn reconnect wants the reply being
// written now. Truncated is true when ANY dimension cut anything, and it rides the wire
// so no client can read a bounded payload as a complete one.
func (buf *Buffer) SnapshotCapped(caps SnapshotCaps) (snap Snapshot, ok bool) {
	buf.read(func() {
		if buf.MessageID == "" {
			return
		}
		// A bare busy signal still carries the seq, which is a fact about the TURN rather
		// than about the message this read found nothing to describe.
		snap.ChunkSeq = buf.chunkSeq
		if buf.Content.Len() == 0 && buf.Reasoning.Len() == 0 && len(buf.ToolCalls) == 0 {
			return
		}
		content, contentCut := tailBytes(buf.Content.String(), caps.ContentBytes)
		reasoning, reasoningCut := tailBytes(buf.Reasoning.String(), caps.ReasoningBytes)
		blocks, base, blocksCut := capBlocks(buf.Blocks, caps.BlockTextBytes, caps.Blocks)
		tools, toolsCut := capToolCalls(buf.ToolCalls, caps.ToolCalls, caps.ToolOutputBytes, caps.ToolOutputTotalBytes)
		// Field-for-field the shape assembled at turn end, so a mid-turn snapshot renders
		// byte-equivalently to the turn that follows it. Slices are copied: the caller reads
		// them off this goroutine while the dispatch loop keeps appending.
		snap.Message = vibekit.Message{
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
		snap.BlockBase = base
		snap.Truncated = contentCut || reasoningCut || blocksCut || toolsCut
		ok = true
	})
	return snap, ok
}
