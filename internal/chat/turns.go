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
// index the timeline rail draws from. A user message opens a turn, and so does the
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
		if m.Role == vibekit.RoleUser || len(out) == 0 || opensHeaderlessTurn(m, closed) {
			var body []vibekit.Message
			if m.Role != vibekit.RoleUser {
				body = append(body, *m)
			}
			summary := vibekit.TurnSummary{
				ID:             m.ID,
				N:              len(out) + 1,
				Ts:             m.Ts,
				AgentInitiated: m.Role != vibekit.RoleUser,
			}
			if m.Role == vibekit.RoleUser {
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

// closesTurn reports whether an outcome value ENDS a segment. A settled outcome
// does; "unknown" does not, since it marks a fragment whose end never arrived, and
// treating that as a terminator split the turn in two. A fragment JOINS the segment
// it interrupted, and deriveTurnOutcome lets the reply's outcome supersede it.
func closesTurn(outcome vibekit.TurnOutcome) bool {
	return outcome != "" && outcome != vibekit.TurnOutcomeUnknown
}

// opensHeaderlessTurn reports whether m is the first persisted message of a turn
// with no user trigger. Derivable from a flat list because a turn's outcome-bearing
// message closes it; closesTurn owns the fragment carve-out.
//
// Both clauses are load-bearing: without the close test a prompted empty turn's
// marker splits off its own prompt, without the other an interrupted divider does.
func opensHeaderlessTurn(m *vibekit.Message, prevClosed bool) bool {
	if !prevClosed {
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
