package vibekit

// TurnOutcome is a turn's result, rendered as scannable colour on the timeline
// rail and as the turn footer's tint.
//
// CROSS-LANGUAGE CONTRACT. The identical rule is implemented client-side in
// `static-src/turns.ts` (deriveOutcome), and it has to be: the server owns
// numbering and outcome for the whole session, but the client still derives the
// IN-FLIGHT turn's outcome, which no fetched summary can know. Neither side can
// be deleted, so both are pinned against one shared fixture —
// `internal/chat/testdata/turn_outcomes.json`, read beside the projection by
// chat's TestTurnOutcomeContract and by turns.test.ts there. Change the rule in
// one language and the other language's test fails, which is the only thing
// keeping the two honest.
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
