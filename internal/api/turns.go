package api

import (
	"strings"
	"unicode"
)

// TurnOutcome is a turn's result, rendered as scannable colour on the timeline
// rail and as the turn footer's tint.
//
// CROSS-LANGUAGE CONTRACT. The identical rule is implemented client-side in
// `static-src/turns.ts` (deriveOutcome), and it has to be: the server owns
// numbering and outcome for the whole session, but the client still derives the
// IN-FLIGHT turn's outcome, which no fetched summary can know. Neither side can
// be deleted, so both are pinned against one shared fixture —
// `testdata/turn_outcomes.json`, read by TestTurnOutcomeContract here and by
// turns.test.ts there. Change the rule in one language and the other language's
// test fails, which is the only thing keeping the two honest.
type TurnOutcome string

// TurnOutcomeRunning and the following constants are the valid TurnOutcome
// values.
const (
	TurnOutcomeRunning     TurnOutcome = "running"
	TurnOutcomeCompleted   TurnOutcome = "completed"
	TurnOutcomeInterrupted TurnOutcome = "interrupted"
	TurnOutcomeFailed      TurnOutcome = "failed"
)

// TurnSummary is one row of a chat's session-wide turn index: enough to draw a
// rail marker and label it, and nothing else.
//
// Deliberately NOT the turn's content. The rail spans the whole session while
// the transcript store holds a paginated window, so a rail built from resident
// turns would grow markers as the reader scrolled up — which is precisely the
// "progress read-out" the rail claims to be. This is the cheap projection that
// makes the claim true, and it is why the route exists at all rather than the
// client walking `?before_id=` to the beginning of history.
type TurnSummary struct {
	// ID is the turn's opening message id. The client joins on it to map its
	// own resident turns onto these absolute numbers, so the server stays the
	// single source of truth for what "turn 14" means.
	ID string `json:"id"`
	// FirstLine is the request's first line, whitespace-collapsed, for the
	// marker's hover label. Empty for a turn the user did not trigger.
	FirstLine string      `json:"first_line,omitempty"`
	Outcome   TurnOutcome `json:"outcome"`
	// N is the 1-based, session-absolute turn ordinal.
	N int `json:"n"`
	// Ts is the turn's start: its trigger's timestamp, else its first body
	// message's.
	Ts int64 `json:"ts"`
	// AgentInitiated marks a turn with no user trigger, so the rail can render
	// it as a non-user marker instead of implying the user asked for it.
	AgentInitiated bool `json:"agent_initiated,omitempty"`
}

// turnFirstLineMax caps the hover label. Long enough to recognise a request,
// short enough that the response stays small on a 400-turn session.
const turnFirstLineMax = 120

// ProjectTurnSummaries groups a chat's flat message list into the session-wide
// turn index the timeline rail draws from.
//
// A user message opens a turn; everything else joins the open turn, or opens a
// headerless one when there is none. That last case is not an edge: an
// agent-initiated turn (a run-completion wake) legitimately has no user row to
// promote, and fabricating one would put words in the user's mouth.
//
// `thinking` marks the LAST turn as running, and is the caller's knowledge — a
// persisted chat file cannot tell whether a bridge is mid-turn right now.
func ProjectTurnSummaries(msgs []Message, thinking bool) []TurnSummary {
	if len(msgs) == 0 {
		return []TurnSummary{}
	}
	// bodies[i] holds turn i's non-trigger messages, which is what the outcome
	// derivation reads. Kept beside the summaries rather than on them because
	// the wire shape must not carry a turn's content.
	out := make([]TurnSummary, 0, 8)
	bodies := make([][]Message, 0, 8)
	for i := range msgs {
		m := &msgs[i]
		if m.Role == RoleUser || len(out) == 0 {
			var body []Message
			if m.Role != RoleUser {
				body = append(body, *m)
			}
			summary := TurnSummary{
				ID:             m.ID,
				N:              len(out) + 1,
				Ts:             m.Ts,
				AgentInitiated: m.Role != RoleUser,
			}
			if m.Role == RoleUser {
				summary.FirstLine = firstLine(m.Content, turnFirstLineMax)
			}
			out = append(out, summary)
			bodies = append(bodies, body)
			continue
		}
		bodies[len(bodies)-1] = append(bodies[len(bodies)-1], *m)
	}
	for i := range out {
		out[i].Outcome = DeriveTurnOutcome(bodies[i], thinking && i == len(out)-1)
	}
	return out
}

// DeriveTurnOutcome reads a turn's outcome off its persisted body.
//
// Persisted data only: a turn's stop reason rides the live turn_ended SSE and is
// never stored, so a reloaded transcript cannot see it. The refusal marker and
// the inline event messages are what survive, and they are what this keys on.
//
// A terminal marker beats isLive deliberately. `thinking` can legitimately still
// be true for the last turn when the next turn's stream has already opened, so
// trusting it over a marker would repaint a finished failure as in-progress.
func DeriveTurnOutcome(body []Message, isLive bool) TurnOutcome {
	interrupted := false
	for i := range body {
		m := &body[i]
		if m.Refusal != nil {
			return TurnOutcomeFailed
		}
		switch m.EventKind {
		case EventCompactFailed, EventInfraSafetyBlocked:
			return TurnOutcomeFailed
		case EventCancelled, EventInterrupted:
			interrupted = true
		}
	}
	if interrupted {
		return TurnOutcomeInterrupted
	}
	if isLive {
		return TurnOutcomeRunning
	}
	return TurnOutcomeCompleted
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
