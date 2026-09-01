package translate

// Stripping the steering acknowledgement marker out of assistant text.
//
// When a steer is injected, KAS instructs the agent to close its response
// with `[STEERING steer-<id>: what I did about it]`. That marker is
// machinery: KAS reads it back with `recordSteeringAcks` to mark the
// steer handled, and does not remove it itself, so it arrives here
// verbatim inside ordinary text deltas.
//
// The stripping happens mid-stream, and a marker can be split across any
// number of chunk boundaries. Hence a carry: the trailing bytes of a
// chunk that could still grow into a marker are withheld rather than
// emitted, and released once the next chunk proves they were not one.
// The carry lives on the per-chat buffer.Buffer and is flushed if a turn
// ends while something is still held.
//
// Text only, matching KAS's own scoping: `getLastAssistantText` collects
// only text entries, so KAS itself never looks for the marker in
// reasoning.

import (
	"regexp"
	"strings"

	"github.com/cplieger/vibekit/internal/buffer"
)

// steerAckPrefix is the shortest span that commits a `[` to being a marker.
// The literal `[STEERING ` alone is not enough: prose can legitimately open that
// way ("[STEERING is the new feature]"), and treating it as a marker start would
// hold a real sentence hostage until the carry bound released it.
const steerAckPrefix = "[STEERING steer-"

// maxSteerCarry bounds what may be withheld. A marker the model opens and never
// closes would otherwise swallow the rest of the turn; past this the carry is
// released as ordinary text on the reasoning that a marker this long is not one.
const maxSteerCarry = 8 << 10

// steerAckRe matches a COMPLETE marker. Deliberately the same shape as KAS's
// STEERING_RESPONSE_PATTERN — `(?s)` for its `s` flag and a lazy body that ends
// at the first `]` — because what vibekit hides must be exactly what KAS treats
// as an acknowledgement. A looser pattern here would eat real text; a stricter
// one would leave machinery on screen.
//
// The two groups do not change what matches; they name the two halves the strip
// used to throw away. The BODY is the agent's own statement of what it did about
// the steer, which is strictly better information than a check glyph, so it is
// extracted rather than discarded (see steerAck).
var steerAckRe = regexp.MustCompile(`(?s)\[STEERING (steer-[^\s:]+): (.+?)\]`)

// steerAck is one acknowledgement lifted out of the text: which steer, and what
// the agent says it did about it.
//
// Hidden from the transcript and surfaced on the steer's own chip instead. The
// marker is machinery in the prose and a fact about the steer, so its home is
// the row that already tracks that steer rather than the reply.
type steerAck struct {
	SteerID string
	Text    string
}

// stripSteerAcks removes complete acknowledgement markers from
// carry+incoming and returns the text safe to emit, the bytes still
// withheld, and the acknowledgements it lifted out, in order.
//
// The returned carry is always a suffix of the input: every byte is either
// emitted, withheld for the next call, or part of a marker matched in
// full. A `[` sitting before a complete marker can never grow into one
// itself, since a complete marker sits between it and every later byte —
// withholding it would splice unrelated text across a marker that is
// already gone.
func stripSteerAcks(carry, incoming string) (emit, newCarry string, acks []steerAck) {
	joined := carry + incoming
	buf := joined
	// settled is where the scan for a still-open candidate may begin. Everything
	// before it is followed by a complete marker, so it is finished text.
	settled := 0
	if matches := steerAckRe.FindAllStringSubmatchIndex(joined, -1); matches != nil {
		var b strings.Builder
		last := 0
		for _, m := range matches {
			b.WriteString(joined[last:m[0]])
			acks = append(acks, steerAck{
				SteerID: joined[m[2]:m[3]],
				// Trimmed because the body is rendered as a label, and the
				// marker's own separator leaves it with leading space in the
				// shapes KAS produces.
				Text: strings.TrimSpace(joined[m[4]:m[5]]),
			})
			last = m[1]
		}
		settled = b.Len()
		b.WriteString(joined[last:])
		buf = b.String()
	}
	// Every complete marker is gone, so any candidate left in the tail is still
	// open. Hold from the first one that could still become a marker; a `[` that
	// cannot is ordinary text and must not delay the rest of the sentence behind
	// it.
	for i := settled; i < len(buf); {
		j := strings.IndexByte(buf[i:], '[')
		if j < 0 {
			break
		}
		at := i + j
		if couldBecomeSteerAck(buf[at:]) {
			if len(buf)-at > maxSteerCarry {
				return buf, "", acks
			}
			// `[` is ASCII, so this is a rune boundary and neither side can be a
			// torn code point.
			return buf[:at], buf[at:], acks
		}
		i = at + 1
	}
	return buf, "", acks
}

// couldBecomeSteerAck reports whether s, which starts at a `[`, might still turn
// into a complete marker once more text arrives.
//
// Two cases. Shorter than the committing prefix: it qualifies only while it is
// still a prefix OF that prefix, so `[STE` waits and `[doc` does not. At or past
// that length: it must actually carry the prefix, and it stays open until a `]`
// shows up — a `]` already present means the full pattern did not match it back
// in stripSteerAcks, so it never was a marker.
func couldBecomeSteerAck(s string) bool {
	if len(s) < len(steerAckPrefix) {
		return strings.HasPrefix(steerAckPrefix, s)
	}
	if !strings.HasPrefix(s, steerAckPrefix) {
		return false
	}
	return !strings.Contains(s, "]")
}

// FlushSteerCarry settles whatever the filter was still withholding when a turn
// ended, and clears it.
//
// The decision is the carry's own shape, and the two outcomes are both correct
// rather than one being a fallback. A carry that already carries the committing
// prefix is a marker the model opened and never closed — machinery, and dropping
// it is the same answer the filter would have given had the `]` arrived. A
// shorter carry is a partial bracket sequence that turned out to be ordinary
// prose (a response ending in `[`), so it goes back into the turn.
//
// Released text needs no chunk broadcast: the turn's final message is appended
// to the chat store whole, and that append broadcasts the complete content. One
// call reaches both readers of the turn — AppendTextDelta accumulates into the
// content builder as well as the block array — which is what keeps the persisted
// message and the client's block renderer agreeing.
func FlushSteerCarry(buf *buffer.Buffer) {
	carry, subtask := buf.SteerCarry()
	if carry == "" {
		return
	}
	buf.SetSteerCarry("", "")
	if strings.HasPrefix(carry, steerAckPrefix) {
		return
	}
	buf.AppendTextDelta(carry, subtask)
}
