package buffer

import (
	"slices"
	"time"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// SnapshotCaps bounds every dimension of a turn snapshot. A ZERO in any field leaves that
// dimension unbounded, which makes SnapshotCaps{} the unbounded snapshot and lets Snapshot
// be one call into SnapshotCapped rather than a second implementation. A struct rather
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
	// own 64 KiB bound AND have a checkable ceiling. Zero is UNBOUNDED, which is what
	// connectSnapshotCaps leaves it at.
	ToolOutputTotalBytes int
	// Blocks bounds how many blocks are carried, keeping the NEWEST.
	Blocks int
}

// MaxTextBytes is the worst-case TEXT the caps admit: the two flat fields, the block
// array's share, and every carried tool call's output, so a budget and the tests read one
// arithmetic. Zero means UNBOUNDED, reported whenever any contributing dimension is
// itself unbounded — a partial sum would read as a real ceiling and understate the payload.
//
// The unbounded guard covers the five ORIGINAL dimensions only, deliberately.
// ToolOutputTotalBytes is an OPTIONAL tightening rather than a sixth requirement: a zero
// there means the tool-output share is the product, which is what connectSnapshotCaps
// answers today and is why its own MaxTextBytes is unmoved by this field existing.
func (c SnapshotCaps) MaxTextBytes() int {
	if c.ReasoningBytes <= 0 || c.ContentBytes <= 0 || c.BlockTextBytes <= 0 || c.ToolCalls <= 0 || c.ToolOutputBytes <= 0 {
		return 0
	}
	tools := c.ToolCalls * c.ToolOutputBytes
	if c.ToolOutputTotalBytes > 0 {
		tools = min(tools, c.ToolOutputTotalBytes)
	}
	return c.ReasoningBytes + c.ContentBytes + c.BlockTextBytes + tools
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

// SnapshotCapped returns the in-flight turn as a vibekit.Message bounded by caps, plus the
// chunk-sequence watermark, whether anything was withheld, and whether there is a snapshot
// at all. Snapshot is this call with no caps, so there is ONE implementation of the read.
//
// Every text dimension keeps its TAIL, because a mid-turn reconnect wants the reply being
// written now. `truncated` is true when ANY dimension cut anything, and it rides the wire
// so no client can read a bounded payload as a complete one.
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
	tools, toolsCut := capToolCalls(buf.ToolCalls, caps.ToolCalls, caps.ToolOutputBytes, caps.ToolOutputTotalBytes)
	// Field-for-field the shape assembled at turn end, so a mid-turn snapshot renders
	// byte-equivalently to the turn that follows it. Slices are copied: the caller reads
	// them off this goroutine while the dispatch loop keeps appending.
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
