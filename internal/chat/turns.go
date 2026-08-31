package chat

import (
	"strings"
	"unicode"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// turnFirstLineMax caps the hover label. Long enough to recognise a request,
// short enough that the response stays small on a 400-turn session.
const turnFirstLineMax = 120

// projectTurnSummaries groups a chat's flat message list into the session-wide
// turn index the timeline rail draws from.
//
// A user message opens a turn, and so does the first message of a turn with no
// user trigger — see opensHeaderlessTurn, which owns that predicate. Without it an
// agent-initiated turn's reply landed in the PREVIOUS turn's body and took its
// outcome with it.
//
// `thinking` marks the LAST turn as running, and is the caller's knowledge — a
// persisted chat file cannot tell whether a bridge is mid-turn right now.
func projectTurnSummaries(msgs []vibekit.Message, thinking bool) []vibekit.TurnSummary {
	if len(msgs) == 0 {
		return []vibekit.TurnSummary{}
	}
	// bodies[i] holds turn i's non-trigger messages, which is what the outcome
	// derivation reads. Kept beside the summaries rather than on them because
	// the wire shape must not carry a turn's content.
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
// does; "unknown" does not, because it marks a fragment whose end never arrived
// — the displaced-turn persist (agent/turn_finalize.go closerWireDisplaced), a
// bracket that never closed — and every transcript persisted before the
// internal-tool suppression carries one such fragment per fresh session, right
// between the user's message and the real reply. Treating the fragment as a
// terminator split that turn in two: the reply opened a phantom
// "Agent-initiated turn", the rail counted one turn too many, and the user's
// input and output landed in different segments. A fragment JOINS the segment
// it interrupted; deriveTurnOutcome lets the reply's settled outcome supersede
// its "unknown".
func closesTurn(outcome vibekit.TurnOutcome) bool {
	return outcome != "" && outcome != vibekit.TurnOutcomeUnknown
}

// opensHeaderlessTurn reports whether m is the first persisted message of a turn
// with no user trigger. Derivable from a flat list because a turn's
// outcome-bearing message closes it (Message.TurnOutcome, one settled outcome
// per turn — closesTurn owns the fragment carve-out).
//
// Both clauses are load-bearing, and reviewers got the rule wrong in both
// directions: without the close test a prompted empty turn's marker is split off
// its own prompt, and without the assistant-or-marker test an interrupted divider
// is split off the turn it describes.
func opensHeaderlessTurn(m *vibekit.Message, prevClosed bool) bool {
	if !prevClosed {
		return false
	}
	return m.Role == vibekit.RoleAssistant || m.TurnOutcome != ""
}

// deriveTurnOutcome reads a turn's outcome off its persisted body.
//
// The DURABLE outcome first; the marker derivation below is the fallback for every
// turn persisted before that field existed, where the refusal marker and the
// inline event messages are all that survived.
//
// A terminal answer beats isLive deliberately: `thinking` can still be true for
// the last turn when the next turn's stream has opened, so trusting it would
// repaint a finished failure as in-progress.
func deriveTurnOutcome(body []vibekit.Message, isLive bool) vibekit.TurnOutcome {
	interrupted := false
	sawUnknown := false
	for i := range body {
		m := &body[i]
		if m.TurnOutcome == vibekit.TurnOutcomeUnknown {
			// A fragment's non-verdict (see closesTurn). Remembered as the
			// fallback rather than returned: the segment usually continues into
			// the real reply, whose settled outcome is the turn's.
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
	if sawUnknown {
		return vibekit.TurnOutcomeUnknown
	}
	return vibekit.TurnOutcomeCompleted
}

// firstLine collapses every whitespace run to one space and truncates on a rune
// boundary, so a pasted block yields one readable line and a multi-byte
// character is never cut in half.
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
