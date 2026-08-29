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
// chat's TestTurnOutcomeContract and by turns.node.test.ts there. Change the rule in
// one language and the other language's test fails, which is the only thing
// keeping the two honest.
type TurnOutcome string

// TurnOutcomeRunning and the following constants are the valid TurnOutcome
// values. Every one but `running` is DURABLE — stamped on the finalized assistant
// message, or on an outcome marker when the turn emitted nothing — because a live
// stop reason is never stored and a reloaded transcript could otherwise only guess.
const (
	// TurnOutcomeRunning is the one value that is never persisted: it describes
	// the turn in flight, which only the client can know.
	TurnOutcomeRunning     TurnOutcome = "running"
	TurnOutcomeCompleted   TurnOutcome = "completed"
	TurnOutcomeCancelled   TurnOutcome = "cancelled"
	TurnOutcomeInterrupted TurnOutcome = "interrupted"
	TurnOutcomeFailed      TurnOutcome = "failed"
	// TurnOutcomeRefused is the model declining to continue — a refusal or a
	// content filter. Its own value rather than `failed`: nothing malfunctioned,
	// and the remedy is a different prompt or a different model.
	TurnOutcomeRefused TurnOutcome = "refused"
	// TurnOutcomeUnknown is a stop reason vibekit has not measured, or a turn
	// whose end never arrived. The raw string is kept beside it; no consumer may
	// read that string instead of this value.
	TurnOutcomeUnknown TurnOutcome = "unknown"
)

// TurnConclusion is one wire stop reason, read. A struct rather than four returns
// because it travels as a unit into the turn_ended payload and the three fields
// persisted on the message, where a transposed pair is silent in both directions.
type TurnConclusion struct {
	Outcome TurnOutcome
	// RawStop is the stop reason exactly as the wire sent it, kept whatever the
	// outcome, so a value vibekit has not measured is still recoverable from the
	// record rather than flattened into `unknown` and lost.
	RawStop StopReason
	// Truncated is a turn the model stopped short of finishing: it completed, and
	// its answer is cut off. Stored though derivable from RawStop, so the two
	// projections do not each re-implement the mapping.
	Truncated bool
	// Known is whether the mapping recognised RawStop. False is the caller's cue
	// to log once — never to treat the turn as failed, because an unmeasured stop
	// reason says nothing about whether the work succeeded.
	Known bool
}

// ConcludeStopReason maps a wire stop reason onto the turn's outcome.
//
// stopReason is an OPEN string — KAS exceeds ACP spec v1's closed five-value union
// — so an unrecognised value answers `unknown`, keeps its raw text and reports
// Known false. It never becomes `failed`, which would report a working turn as
// broken. An EMPTY reason is a response that carried none, which vibekit produces
// itself, so it resolves to `unknown` with nothing to log.
func ConcludeStopReason(stop StopReason) TurnConclusion {
	c := TurnConclusion{RawStop: stop, Known: true}
	switch stop {
	case StopReasonEndTurn:
		c.Outcome = TurnOutcomeCompleted
	case StopReasonCancelled:
		c.Outcome = TurnOutcomeCancelled
	case StopReasonInterrupted:
		c.Outcome = TurnOutcomeInterrupted
	case StopReasonError:
		c.Outcome = TurnOutcomeFailed
	case StopReasonRefusal, StopReasonContentFiltered:
		c.Outcome = TurnOutcomeRefused
	case StopReasonMaxTokens, StopReasonMaxTurnRequests:
		// The turn finished the work it was allowed to do, so it COMPLETED and its
		// answer is cut off. Grading it failed would report a bounded turn as broken.
		c.Outcome = TurnOutcomeCompleted
		c.Truncated = true
	case StopReasonUnknown, "":
		c.Outcome = TurnOutcomeUnknown
	default:
		c.Outcome = TurnOutcomeUnknown
		c.Known = false
	}
	return c
}

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
