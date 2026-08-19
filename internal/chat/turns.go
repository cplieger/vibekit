package chat

import (
	"strings"
	"unicode"

	"github.com/cplieger/vibekit/internal/api"
)

// turnFirstLineMax caps the hover label. Long enough to recognise a request,
// short enough that the response stays small on a 400-turn session.
const turnFirstLineMax = 120

// projectTurnSummaries groups a chat's flat message list into the session-wide
// turn index the timeline rail draws from.
//
// A user message opens a turn; everything else joins the open turn, or opens a
// headerless one when there is none. That last case is not an edge: an
// agent-initiated turn (a run-completion wake) legitimately has no user row to
// promote, and fabricating one would put words in the user's mouth.
//
// `thinking` marks the LAST turn as running, and is the caller's knowledge — a
// persisted chat file cannot tell whether a bridge is mid-turn right now.
func projectTurnSummaries(msgs []api.Message, thinking bool) []api.TurnSummary {
	if len(msgs) == 0 {
		return []api.TurnSummary{}
	}
	// bodies[i] holds turn i's non-trigger messages, which is what the outcome
	// derivation reads. Kept beside the summaries rather than on them because
	// the wire shape must not carry a turn's content.
	out := make([]api.TurnSummary, 0, 8)
	bodies := make([][]api.Message, 0, 8)
	for i := range msgs {
		m := &msgs[i]
		if m.Role == api.RoleUser || len(out) == 0 {
			var body []api.Message
			if m.Role != api.RoleUser {
				body = append(body, *m)
			}
			summary := api.TurnSummary{
				ID:             m.ID,
				N:              len(out) + 1,
				Ts:             m.Ts,
				AgentInitiated: m.Role != api.RoleUser,
			}
			if m.Role == api.RoleUser {
				summary.FirstLine = firstLine(m.Content, turnFirstLineMax)
			}
			out = append(out, summary)
			bodies = append(bodies, body)
			continue
		}
		bodies[len(bodies)-1] = append(bodies[len(bodies)-1], *m)
	}
	for i := range out {
		out[i].Outcome = deriveTurnOutcome(bodies[i], thinking && i == len(out)-1)
	}
	return out
}

// deriveTurnOutcome reads a turn's outcome off its persisted body.
//
// Persisted data only: a turn's stop reason rides the live turn_ended SSE and is
// never stored, so a reloaded transcript cannot see it. The refusal marker and
// the inline event messages are what survive, and they are what this keys on.
//
// A terminal marker beats isLive deliberately. `thinking` can legitimately still
// be true for the last turn when the next turn's stream has already opened, so
// trusting it over a marker would repaint a finished failure as in-progress.
func deriveTurnOutcome(body []api.Message, isLive bool) api.TurnOutcome {
	interrupted := false
	for i := range body {
		m := &body[i]
		if m.Refusal != nil {
			return api.TurnOutcomeFailed
		}
		switch m.EventKind {
		case api.EventCompactFailed, api.EventInfraSafetyBlocked:
			return api.TurnOutcomeFailed
		case api.EventCancelled, api.EventInterrupted:
			interrupted = true
		}
	}
	if interrupted {
		return api.TurnOutcomeInterrupted
	}
	if isLive {
		return api.TurnOutcomeRunning
	}
	return api.TurnOutcomeCompleted
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
