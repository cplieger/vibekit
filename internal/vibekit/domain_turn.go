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
// divider renders. Empty means the turn ended for one of the ordinary causes. It
// lives on the TURN, first-wins and epoch-scoped.
type InterruptCause string

// TurnResult is what a finalized turn reports. Immutable once the turn's
// completion handle fires.
type TurnResult struct {
	Interrupt InterruptCause
	Stop      StopReason
	Epoch     TurnEpoch
	// EmittedNothing is whether the turn produced any content at all, measured
	// AFTER the steering filter's withheld text was settled back in — a turn whose
	// only final text looks like the start of a steering acknowledgement sits in
	// that carry, so a measurement taken earlier reads it as empty.
	EmittedNothing bool
	// WireEnded is whether a wire turn_end closed this turn rather than a local
	// closer. It is what makes the empty-turn recovery safe to arm: a local close
	// can only report end_turn or cancelled, so its end_turn says only that vibekit
	// had nothing better to call it.
	WireEnded bool
}

// TurnOpenSource names what opened a turn.
type TurnOpenSource int

const (
	// TurnSourcePrompt is a user prompt vibekit is about to send.
	TurnSourcePrompt TurnOpenSource = iota
	// TurnSourceLocalShell is a `!cmd` turn vibekit runs itself, with no
	// session/prompt behind it. It records no model, and REFUSES while another turn
	// is open: a shell turn cannot begin during an agent turn.
	TurnSourceLocalShell
	// TurnSourceWireTurnStart is a turn vibekit did not open: the engine started
	// one, and either a turn_start with nothing pending to bind or a fold with no
	// open turn is the first vibekit hears of it.
	TurnSourceWireTurnStart
	// TurnSourcePrime is the transcript-priming session/prompt sent on a switch, a
	// reload or a refused fork. It awaits its own epoch before returning, which is
	// what keeps the unacknowledged set from ever holding two.
	TurnSourcePrime
	// TurnSourceEmptyRetry is the empty-turn recovery's second session/prompt: its
	// own turn, so the retry's reply is its own message rather than extending a
	// closed turn's.
	TurnSourceEmptyRetry
)

// HasUserTrigger reports whether a turn opened by this source has a user message
// of its own in the transcript, which is what decides whether an EMPTY turn needs
// an outcome marker at all: a wireTurnStart turn has no trigger and no assistant
// message, so a marker is the only record that it happened. emptyRetry counts
// because it reuses the first prompt's user message, and both turns project into
// that message's segment on reload.
func (s TurnOpenSource) HasUserTrigger() bool {
	switch s {
	case TurnSourcePrompt, TurnSourceEmptyRetry, TurnSourceLocalShell:
		return true
	default:
		return false
	}
}

// Acknowledgeable reports whether a wire turn_start may bind to this source. Only
// a source that sent a session/prompt qualifies: a localShell turn has no bracket
// coming, and a wireTurnStart turn was created BY one. A binding is revisable and
// nothing irreversible may rest on it, since vibekit cannot tell a prompted
// turn_start from an agent-initiated one.
func (s TurnOpenSource) Acknowledgeable() bool {
	switch s {
	case TurnSourcePrompt, TurnSourcePrime, TurnSourceEmptyRetry:
		return true
	default:
		return false
	}
}
