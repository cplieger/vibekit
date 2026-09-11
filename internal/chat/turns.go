package chat

import (
	"strings"
	"unicode"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// turnFirstLineMax caps the hover label: long enough to recognise a request, short
// enough that the response stays small on a 400-turn session.
const turnFirstLineMax = 120

// projectTurnSummaries groups a chat's flat message list into the session-wide turn
// index the timeline rail draws from. A PROMPT opens a turn, and so does the
// first message of a turn with no user trigger (opensHeaderlessTurn), or an
// agent-initiated reply lands in the PREVIOUS turn's body. `thinking` marks the LAST
// turn as running and is the caller's: a chat file cannot see a live bridge.
func projectTurnSummaries(msgs []vibekit.Message, thinking bool) []vibekit.TurnSummary {
	if len(msgs) == 0 {
		return []vibekit.TurnSummary{}
	}
	// bodies[i] holds turn i's non-trigger messages, kept beside the summaries
	// rather than on them because the wire shape must not carry a turn's content.
	out := make([]vibekit.TurnSummary, 0, 8)
	bodies := make([][]vibekit.Message, 0, 8)
	closed := false
	for i := range msgs {
		m := &msgs[i]
		if carriesNothing(m) {
			continue
		}
		// A prompt opens a turn; a steer joins the one already running.
		if opensTurn(m, len(out) == 0, closed) {
			var body []vibekit.Message
			if !isPrompt(m) {
				body = append(body, *m)
			}
			summary := vibekit.TurnSummary{
				ID:             m.ID,
				N:              len(out) + 1,
				Ts:             m.Ts,
				AgentInitiated: !isPrompt(m),
			}
			if isPrompt(m) {
				summary.FirstLine = firstLine(m.Content, turnFirstLineMax)
			}
			out = append(out, summary)
			bodies = append(bodies, body)
			closed = closesTurn(m.TurnOutcome)
			continue
		}
		bodies[len(bodies)-1] = append(bodies[len(bodies)-1], *m)
		closed = closed || closesTurn(m.TurnOutcome)
	}
	for i := range out {
		out[i].Outcome = deriveTurnOutcome(bodies[i], thinking && i == len(out)-1)
	}
	return out
}

// opensTurn reports whether m OPENS a turn rather than joining the one already
// running. `first` marks the scan's first message, which opens a turn whatever it
// carries; `prevClosed` is the segmentation state as of the message before it.
//
// One predicate with two callers — projectTurnSummaries and turnWindowBase — so the
// boundary rule has exactly one home and a window's ordinals cannot disagree with
// the summaries the rail draws from.
func opensTurn(m *vibekit.Message, first, prevClosed bool) bool {
	return isPrompt(m) || first || opensHeaderlessTurn(m, prevClosed)
}

// turnWindowBase reports the segmentation state at a WINDOW'S LEFT EDGE: how many
// turns precede the turn CONTAINING msgs[start], and whether the segment before
// msgs[start] had already closed. `offset + 1` is that first turn's session-absolute
// ordinal, and `segmentClosed` seeds the client projection's carried state.
//
// The offset counts to the CONTAINING turn rather than to the window, because those
// differ by one exactly when the window opens mid-turn — the common case, since the
// cut falls at a message boundary. A `start` past len(msgs) is an empty window.
func turnWindowBase(msgs []vibekit.Message, start int) (offset int, segmentClosed bool) {
	start = max(start, 0)
	count := 0
	closed := false
	for i := range min(start, len(msgs)) {
		m := &msgs[i]
		if carriesNothing(m) {
			continue
		}
		if opensTurn(m, count == 0, closed) {
			count++
			closed = closesTurn(m.TurnOutcome)
			continue
		}
		closed = closed || closesTurn(m.TurnOutcome)
	}
	// A skipped message is in no turn, so the boundary question below is asked of the
	// first message the window actually renders. Skipping changes no carried state:
	// such a message carries no outcome.
	for start < len(msgs) && carriesNothing(&msgs[start]) {
		start++
	}
	if start >= len(msgs) {
		return count, closed
	}
	if opensTurn(&msgs[start], count == 0, closed) {
		return count, closed
	}
	// msgs[start] continues the turn before it, so the window's FIRST turn is that
	// turn and one fewer turn precedes it. Reached only with count > 0, because
	// opensTurn answers true through its `first` clause otherwise, so the
	// subtraction cannot go negative.
	return count - 1, closed
}

// closesTurn reports whether an outcome value ENDS a segment. A settled outcome
// does; "unknown" does not, since it marks a fragment whose end never arrived, and
// treating that as a terminator split the turn in two. A fragment JOINS the segment
// it interrupted, and deriveTurnOutcome lets the reply's outcome supersede it.
func closesTurn(outcome vibekit.TurnOutcome) bool {
	return outcome != "" && outcome != vibekit.TurnOutcomeUnknown
}

// isPrompt reports whether m is a user PROMPT rather than a steer. Unexported; the
// TypeScript twin is static-src/turns.ts.
func isPrompt(m *vibekit.Message) bool {
	return m.Role == vibekit.RoleUser && m.UserKind != vibekit.UserKindSteer
}

// isStepMessage reports whether every one of m's blocks is workflow-step content.
func isStepMessage(m *vibekit.Message) bool {
	// "every block parses" is vacuously true of a message with NO blocks, so without
	// this an empty assistant or event message would lose the turn it opens.
	if len(m.Blocks) == 0 {
		return false
	}
	for i := range m.Blocks {
		if _, ok := vibekit.ParseStepSubtask(m.Blocks[i].AgentSubtaskID); !ok {
			return false
		}
	}
	return true
}

// carriesNothing reports whether nothing about this assistant message reaches the
// transcript yet. Such a message neither opens a turn nor JOINS one: joining would
// set deriveTurnOutcome's sawAssistant and flip a carrier-less turn from "unknown"
// to "completed". Assistant-only, because an event row renders a badge and may
// carry the turn's outcome, and a user row is a trigger.
func carriesNothing(m *vibekit.Message) bool {
	return m.Role == vibekit.RoleAssistant &&
		m.Content == "" &&
		m.Reasoning == "" &&
		len(m.Blocks) == 0 &&
		len(m.ToolCalls) == 0 &&
		len(m.Plan) == 0 &&
		m.Refusal == nil &&
		m.TurnOutcome == "" &&
		m.EventKind == ""
}

// opensHeaderlessTurn reports whether m is the first persisted message of a turn
// with no user trigger. All three clauses are load-bearing; the shared fixture's
// _segmentation_comment owns the reasoning, and closesTurn the fragment carve-out.
func opensHeaderlessTurn(m *vibekit.Message, prevClosed bool) bool {
	if !prevClosed || isStepMessage(m) {
		return false
	}
	return m.Role == vibekit.RoleAssistant || m.TurnOutcome != ""
}

// deriveTurnOutcome reads a turn's outcome off its persisted body: the DURABLE
// outcome first, with the marker derivation below as the fallback for turns
// persisted before that field existed.
//
// A terminal answer beats isLive deliberately — `thinking` can still be true once
// the next turn's stream has opened. The TAIL clause is the honest answer for a turn
// NOTHING closed, and its predicate is "no ASSISTANT message" rather than "empty
// body", which is what keeps a legacy transcript reading `completed`.
func deriveTurnOutcome(body []vibekit.Message, isLive bool) vibekit.TurnOutcome {
	interrupted := false
	sawUnknown := false
	sawAssistant := false
	for i := range body {
		m := &body[i]
		if m.Role == vibekit.RoleAssistant {
			sawAssistant = true
		}
		if m.TurnOutcome == vibekit.TurnOutcomeUnknown {
			// A fragment's non-verdict (see closesTurn), remembered as the fallback
			// since the segment usually continues into the reply that settles it.
			sawUnknown = true
			continue
		}
		if m.TurnOutcome != "" {
			return m.TurnOutcome
		}
		if m.Refusal != nil {
			return vibekit.TurnOutcomeFailed
		}
		switch m.EventKind {
		case vibekit.EventCompactFailed, vibekit.EventInfraSafetyBlocked:
			return vibekit.TurnOutcomeFailed
		case vibekit.EventCancelled, vibekit.EventInterrupted:
			interrupted = true
		}
	}
	if interrupted {
		return vibekit.TurnOutcomeInterrupted
	}
	if isLive {
		return vibekit.TurnOutcomeRunning
	}
	if sawUnknown || !sawAssistant {
		return vibekit.TurnOutcomeUnknown
	}
	return vibekit.TurnOutcomeCompleted
}

// firstLine collapses every whitespace run to one space and truncates on a rune
// boundary, so a pasted block yields one readable line and a multi-byte character
// is never cut in half.
func firstLine(s string, maxRunes int) string {
	var b strings.Builder
	b.Grow(min(len(s), maxRunes+4))
	space := false
	n := 0
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && n > 0 {
			if n >= maxRunes {
				return b.String() + "\u2026"
			}
			b.WriteRune(' ')
			n++
		}
		space = false
		if n >= maxRunes {
			return b.String() + "\u2026"
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
