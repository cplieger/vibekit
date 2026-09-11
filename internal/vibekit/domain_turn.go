package vibekit

import "errors"

// ErrNoSuchTurn is what awaiting an epoch the chat has no record of reports. A
// caller holding that turn's completion handle can never receive it: the record is
// retained until the handle is released.
var ErrNoSuchTurn = errors.New("no such turn")

// TurnEpoch identifies one turn within its chat. Minted under the chat's lifecycle
// mutex and monotonic per chat, so a closer armed for turn N cannot touch turn
// N+1. Zero is never minted and means no turn.
type TurnEpoch uint64

// InterruptCause names why a turn was interrupted, in the words the transcript's
// divider renders. Empty means an ordinary end. Lives on the TURN, first-wins
// and epoch-scoped.
type InterruptCause string

// TurnResult is what a finalized turn reports. Immutable once the turn's
// completion handle fires.
type TurnResult struct {
	Interrupt InterruptCause
	Stop      StopReason
	Epoch     TurnEpoch
	// EmittedNothing is whether the turn produced content, measured AFTER the
	// steering filter's withheld text settled back in: a turn whose only text sits
	// in that carry reads as empty to any earlier measurement.
	EmittedNothing bool
	// WireEnded is whether a wire turn_end closed this turn rather than a local
	// closer, which is what makes the empty-turn recovery safe to arm: a local
	// close's end_turn says only that vibekit had nothing better to call it.
	WireEnded bool
}

// TurnOpenSource names what opened a turn.
type TurnOpenSource int

const (
	// TurnSourcePrompt is a user prompt vibekit is about to send.
	TurnSourcePrompt TurnOpenSource = iota
	// TurnSourceLocalShell is a `!cmd` turn vibekit runs itself, with no
	// session/prompt behind it. No model, and it REFUSES while a turn is open.
	TurnSourceLocalShell
	// TurnSourceWireTurnStart is a turn vibekit did not open: a turn_start with
	// nothing pending to bind, or a fold with no open turn, is the first it hears.
	TurnSourceWireTurnStart
	// TurnSourceEmptyRetry is the empty-turn recovery's second session/prompt: its
	// own turn, so the retry's reply does not extend a closed turn's.
	TurnSourceEmptyRetry
	// TurnSourceWorkflowStep is a turn opened only because a workflow STEP's frames
	// arrived on this chat's connection: it is the RUN's turn, not this chat's. The
	// step's own turn_end is dropped by the attribution gate, so nothing closes such
	// a turn through the bracket path — agent.Runs.observeComplete does, at the
	// run's terminal transition.
	TurnSourceWorkflowStep
	// turnSourceCount bounds the enum and is not a source: a member added above it
	// fails TestTurnSourcePredicates's completeness check.
	turnSourceCount
)

// PromptClass reports whether a turn opened by this source is a user prompt
// vibekit dispatched — the holders a second prompt can reach with a steer, which
// is what the admission refusal arm keys on and its ONLY reader. A workflow
// step's turn is deliberately not one: a steer aimed into it is read as that
// step's own input rather than as the reader's correction.
func (s TurnOpenSource) PromptClass() bool {
	switch s {
	case TurnSourcePrompt, TurnSourceEmptyRetry:
		return true
	default:
		return false
	}
}

// UserAnswered reports whether opening a turn from this source IS the user
// answering a question the agent left standing, which is what discharges the
// retained waiting_on_user status. A `!cmd` is not one: it reaches no agent.
// Separate from PromptClass because that predicate answers for admission, and
// widening one for admission reasons must not move a cache lifecycle.
func (s TurnOpenSource) UserAnswered() bool {
	switch s {
	case TurnSourcePrompt, TurnSourceEmptyRetry:
		return true
	default:
		return false
	}
}

// ClientVisibleTurn reports whether a RESERVATION held by this source is a turn in
// flight from a CLIENT's point of view: the user row is already persisted and
// broadcast, the client has already latched `thinking`, and the Turn record is one
// StartTurn away. Its own predicate for UserAnswered's reason — this one answers a
// liveness READ, and widening PromptClass for it would move the admission refusal arm
// with it.
//
// A shell turn IS one: command/shell.go reserves through TryReserveTurn and holds that
// reservation across the chat-file write that persists and broadcasts the `!cmd` user
// row, all before StartTurn mints the record. A wire-started turn holds no reservation
// at all.
func (s TurnOpenSource) ClientVisibleTurn() bool {
	switch s {
	case TurnSourcePrompt, TurnSourceEmptyRetry, TurnSourceLocalShell:
		return true
	default:
		return false
	}
}

// Acknowledgeable reports whether a wire turn_start may bind to this source. Only
// a source that sent a session/prompt qualifies: a localShell turn has no bracket
// coming and a wireTurnStart turn was created BY one. A binding is revisable, so
// nothing irreversible may rest on it.
func (s TurnOpenSource) Acknowledgeable() bool {
	switch s {
	case TurnSourcePrompt, TurnSourceEmptyRetry:
		return true
	default:
		return false
	}
}

// EngineOpened reports whether the ENGINE opened this turn rather than vibekit: a
// bracket or a fold arriving with nothing pending. Such a turn holds no admission
// reservation, so a prompt meeting one must DISPLACE it — closing it first, or
// content already broadcast to every client is lost. A predicate rather than a
// member list, so the displacement rule needs no widening for the next member.
func (s TurnOpenSource) EngineOpened() bool {
	switch s {
	case TurnSourceWireTurnStart, TurnSourceWorkflowStep:
		return true
	default:
		return false
	}
}
