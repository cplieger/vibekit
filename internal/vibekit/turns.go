package vibekit

// TurnOutcome is a turn's result, rendered as scannable colour on the timeline
// rail and as the turn footer's tint.
//
// CROSS-LANGUAGE CONTRACT: `static-src/turns.ts` deriveOutcome implements the same
// rule for the IN-FLIGHT turn, which no fetched summary can know. Both are pinned
// against `internal/chat/testdata/turn_outcomes.json`, so changing the rule in one
// language fails the other language's test.
type TurnOutcome string

// TurnOutcomeRunning and the following constants are the valid TurnOutcome values.
// Every one but `running` is DURABLE, because a live stop reason is never stored
// and a reloaded transcript could otherwise only guess.
const (
	// TurnOutcomeRunning is the one value never persisted: the turn in flight,
	// which only the client can know.
	TurnOutcomeRunning     TurnOutcome = "running"
	TurnOutcomeCompleted   TurnOutcome = "completed"
	TurnOutcomeCancelled   TurnOutcome = "cancelled"
	TurnOutcomeInterrupted TurnOutcome = "interrupted"
	TurnOutcomeFailed      TurnOutcome = "failed"
	// TurnOutcomeRefused is the model declining to continue. Its own value rather
	// than `failed`: nothing malfunctioned, so the remedy is a different prompt.
	TurnOutcomeRefused TurnOutcome = "refused"
	// TurnOutcomeUnknown is an unmeasured stop reason, or a turn whose end never
	// arrived. The raw string is kept beside it; no consumer may read it instead.
	TurnOutcomeUnknown TurnOutcome = "unknown"
)

// TurnSeverity is HOW BADLY a turn ended, derived from its TurnOutcome by
// SeverityOf and by nothing else, because five surfaces each carried a partial
// answer to "is this turn broken" and disagreed. CROSS-LANGUAGE like TurnOutcome:
// `static-src/turn-severity.ts` holds the same table, pinned against
// `internal/vibekit/testdata/turn_severity.json`.
type TurnSeverity string

// TurnSeverityRunning and the following constants are the valid TurnSeverity
// values. Four rather than three because a turn the user cancelled ended without
// an answer, which is not the same as one that answered.
const (
	// TurnSeverityRunning is a turn in flight: no settled mark on any surface.
	TurnSeverityRunning TurnSeverity = "running"
	// TurnSeverityClean is a turn that answered. The ABSENCE of a mark is its
	// treatment, so no surface paints anything for it.
	TurnSeverityClean TurnSeverity = "clean"
	// TurnSeverityStopped is a turn that produced no answer through no fault: the
	// user cancelled it, or vibekit could not read how it ended.
	TurnSeverityStopped TurnSeverity = "stopped"
	// TurnSeverityBroken is a turn that failed: the one value that earns a red
	// mark, an inline reason and a refusal to auto-fold.
	TurnSeverityBroken TurnSeverity = "broken"
)

// SeverityOf grades a turn outcome. Total and MECE over the seven TurnOutcome
// values with NO default arm, so an eighth outcome is a compile error here rather
// than a value that reads clean at five surfaces.
//
// `interrupted` is BROKEN: a fault nobody chose stopped the turn. `unknown` is
// STOPPED, never broken and never clean — an unmeasured stop reason says nothing
// about success, and a mark falls back to ambiguous rather than to reassuring.
func SeverityOf(o TurnOutcome) TurnSeverity {
	switch o {
	case TurnOutcomeRunning:
		return TurnSeverityRunning
	case TurnOutcomeCompleted:
		return TurnSeverityClean
	case TurnOutcomeCancelled, TurnOutcomeUnknown:
		return TurnSeverityStopped
	case TurnOutcomeInterrupted, TurnOutcomeFailed, TurnOutcomeRefused:
		return TurnSeverityBroken
	}
	// Unreachable for every declared TurnOutcome; an added outcome fails the
	// exhaustiveness fixture. An empty or hand-edited value grades STOPPED.
	return TurnSeverityStopped
}

// DefaultFailureReason is what a turn says when nothing upstream said anything: no
// stopDetails on the wire, or a record predating TurnFailureReason. One sentence
// per OUTCOME rather than per severity, because a refusal and a network
// interruption are both broken and want different words; empty where no account is
// needed. Pinned BYTE-IDENTICAL against the fixture the client reads too.
func DefaultFailureReason(o TurnOutcome) string {
	switch o {
	case TurnOutcomeFailed:
		return "The agent reported an error and the turn stopped."
	case TurnOutcomeInterrupted:
		return "The turn was interrupted before the agent finished."
	case TurnOutcomeRefused:
		return "The model declined to continue."
	case TurnOutcomeUnknown:
		return "The turn ended for a reason vibekit could not read."
	// `cancelled` says nothing: the footer's own outcome word already reads
	// "Cancelled" a row away, so a sentence here is one fact rendered twice.
	case TurnOutcomeCancelled, TurnOutcomeCompleted, TurnOutcomeRunning:
		return ""
	}
	return ""
}

// StopMarkerKind is the transcript marker recording a turn that stopped without the
// engine answering it. Two consumers in two packages — agent's closeAsInterrupted and
// command's no-turn carrier — so the two buckets live here rather than twice.
//
// EventTurnOutcome is deliberately out of range: that is the CLEAN-close marker
// recordTurnCarrier mints for a turn which emitted nothing, and it answers a different
// question.
func StopMarkerKind(o TurnOutcome) EventKind {
	if o == TurnOutcomeCancelled {
		return EventCancelled
	}
	return EventInterrupted
}

// TurnConclusion is one wire stop reason, read. A struct rather than four returns
// because it travels as a unit into the turn_ended payload and the three persisted
// fields, where a transposed pair is silent in both directions.
type TurnConclusion struct {
	Outcome TurnOutcome
	// Reason is the user-facing account of an abnormal stop, in the words of
	// whoever knows the cause. Persisted beside the outcome so a reloaded
	// transcript can SAY why a turn failed rather than show an empty red card.
	Reason string
	// RawStop is the stop reason exactly as the wire sent it, kept whatever the
	// outcome, so an unmeasured value is recoverable rather than flattened away.
	RawStop StopReason
	// Truncated is a turn the model stopped short of finishing. Stored though
	// derivable from RawStop, so two projections do not re-implement the mapping.
	Truncated bool
	// Known is whether the mapping recognised RawStop. False is a cue to log once,
	// never to treat the turn as failed.
	Known bool
}

// ConcludeStopReason maps a wire stop reason onto the turn's outcome.
//
// stopReason is an OPEN string — KAS exceeds ACP spec v1's closed union — so an
// unrecognised value answers `unknown`, keeps its raw text and reports Known
// false. It never becomes `failed`, which would report a working turn as broken.
// An EMPTY reason is one vibekit produced itself, so it resolves silently.
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
		// The turn finished the work it was allowed to do, so it COMPLETED with its
		// answer cut off. Grading it failed would report a bounded turn as broken.
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
// rail marker and label it, and nothing else. Deliberately NOT the turn's content
// — the rail spans the whole session while the transcript store holds a paginated
// window, so a rail built from resident turns would grow markers as the reader
// scrolled up.
type TurnSummary struct {
	// ID is the turn's opening message id. The client joins on it, so the server
	// stays the single source of truth for what "turn 14" means.
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
	// AgentInitiated marks a turn with no user trigger, so the rail does not imply
	// the user asked for it.
	AgentInitiated bool `json:"agent_initiated,omitempty"`
}

// LiveTurn is the in-flight turn's accumulated assistant message, as the single-chat
// GET carries it beside `messages`.
//
// It exists because the in-flight reply reaches the chat file only at turn end, so the
// window that response serves has no carrier for it: `turn_open` states that a turn is
// running and the transcript that describes it was, until this field, reachable through
// the SSE connect replay alone — a channel gated on a declaration the client makes
// before it knows which chat it will show. A SIBLING field rather than an extra element
// in `messages`, so `has_more`, `turn_offset`, `turn_segment_closed` and `message_count`
// all keep meaning "what the file holds".
//
// A STRUCT rather than the four values SnapshotCapped answers with: two of those are
// adjacent bools, and a transposed pair at the one call site compiles and is silent in
// both directions.
type LiveTurn struct {
	// Message is the turn as accumulated so far, field-for-field the shape assembled at
	// turn end so it renders byte-equivalently to the turn that replaces it.
	Message Message `json:"message"`
	// ChunkSeq is the last delta folded into Message (see MessageChunkPayload.Seq). The
	// client's dedup watermark: a chunk at or below it is already in here.
	ChunkSeq int64 `json:"chunk_seq"`
	// Truncated reports that the cap withheld part of Message, so the payload carries
	// the TAIL of the turn and the rest arrives with message_appended.
	//
	// A `true` is the EXCEPTIONAL case on this channel, unlike on TurnStatePayload. The
	// caps this field reports on (internal/agent's liveTurnGETCaps) are sized above the
	// measured per-dimension maxima precisely so an ordinary turn is not cut, so what a
	// `true` names is a turn past a ~10.1 MiB runaway ceiling rather than a routine tail.
	//
	// A `false` is therefore load-bearing rather than merely an absence of withholding: it
	// is a positive statement that this MESSAGE is whole, and it RETRACTS a truncation
	// marker the connect channel set for the same message id. The two channels disagree by
	// design — a connect frame's turn_state runs on connectSnapshotCaps at 52 KiB and
	// truncates routinely — and this one is the fresher and wider answer, so a reader
	// holding a marker for this message id must drop it (static-src/store-load.ts
	// adoptLiveTurn).
	//
	// NEVER `omitempty`, for TurnStatePayload.Truncated's reason: an absent marker must
	// not be readable as "complete", which is what makes a capped payload admissible.
	Truncated bool `json:"truncated"`
}

// TurnOpenState is the `turn_open` / `turn_workflow_step` pair on the single-chat GET:
// whether a turn is open at all, and WHOSE it is. One value because the two must be read
// under a single acquisition (openTurnFacts records that hazard) and because two adjacent
// bools transpose silently. Zero value: no turn, nothing to own.
type TurnOpenState struct {
	// Open counts a workflow STEP's turn and a prompt-class admission reservation with
	// no Turn minted yet. Openness alone, so its true licenses a reader to keep its
	// per-turn markers rather than to render the chat as busy.
	Open bool
	// WorkflowStep means a run's step opened it, not the reader, so the chat's own agent
	// is idle. Absent-means-this-chat's-own, matching TurnEndedPayload.WorkflowStep.
	// Meaningless unless Open — "no turn" and "a run's turn" are not one axis.
	WorkflowStep bool
}

// OwnTurn reports whether a turn of the CHAT'S OWN is in flight — the one spelling the
// connect handshake's busy set also answers, so the two channels cannot disagree.
func (t TurnOpenState) OwnTurn() bool { return t.Open && !t.WorkflowStep }
