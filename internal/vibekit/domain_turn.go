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
	// TurnSourceWorkflowStep is a turn opened only because a workflow STEP's
	// frames arrived on this chat's connection: it is the RUN's turn, not this
	// chat's. A chat-parented run executes on the launching chat's session, so the
	// step's content folds here — but the step's own turn_end is dropped by the
	// workflow attribution gate, so nothing closes such a turn through the bracket
	// path and a client that read it as the chat working would say so for the
	// whole run. The RUN's own tab dot carries that liveness instead.
	//
	// What DOES close it is the RUN's own terminal transition (agent.Runs.observeComplete
	// calls agent.BridgeCoordinator.CloseStepTurn). Whichever closer gets there, the
	// content is persisted ahead of the trailing user rows: that choice reads EngineOpened.
	TurnSourceWorkflowStep
)

// PromptClass reports whether a turn opened by this source is a user prompt
// vibekit dispatched — the holders a second prompt can reach with a steer once
// the bridge is live, which is what the admission refusal arm keys on. A prime
// is deliberately not one: a steer aimed into the prime window is consumed by
// a throwaway turn, so its holder answers the "starting" refusal instead.
func (s TurnOpenSource) PromptClass() bool {
	switch s {
	case TurnSourcePrompt, TurnSourceEmptyRetry:
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

// EngineOpened reports whether the ENGINE opened this turn rather than vibekit:
// a bracket or a fold arriving with nothing pending. Such a turn holds no
// admission reservation, so a prompt can meet one and must DISPLACE it — closing
// it first, or content already broadcast to every client is lost. Both members
// are the same kind of turn and differ only in what the client may conclude from
// them, so a predicate is what keeps the displacement rule from having to be
// widened again for the next one.
func (s TurnOpenSource) EngineOpened() bool {
	switch s {
	case TurnSourceWireTurnStart, TurnSourceWorkflowStep:
		return true
	default:
		return false
	}
}
