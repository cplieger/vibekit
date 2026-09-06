package agent

// The one path every terminal step takes: each step is a CLOSER handed to
// finalizeTurn, which claims the turn first-wins, runs that closer's effects with
// no lock held, and publishes the result.

import (
	"cmp"
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/runesafe/v2"
	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/durable"
	"github.com/cplieger/vibekit/internal/sanitize"
	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// turnCloser names the local step ending a turn. The finalizer takes it rather
// than a stop reason because the steps resolve an in-flight partial in genuinely
// different directions.
type turnCloser int

const (
	// closerPromptResponse is the session/prompt response settling.
	closerPromptResponse turnCloser = iota
	// closerPromptFailure is the prompt call failing before the turn could end.
	closerPromptFailure
	// closerModelSwitch is a bridge restart DISCARDING the turn: the user asked
	// for a different answer, so the partial is moot.
	closerModelSwitch
	// closerLocalShell is a `!cmd` turn vibekit ran itself.
	closerLocalShell
	// closerBridgeDeath is the third actor: the agent process exited with a turn
	// still open, so nothing else is going to close it.
	closerBridgeDeath
	// closerWireEnd is the engine's own turn_end bracket. The ONLY closer that
	// sets WireEnded, which is what the empty-turn recovery gates on.
	closerWireEnd
	// closerWireDisplaced is a turn_start arriving with a turn still open and
	// nothing pending to bind: the previous turn's end never came, so it closes
	// `unknown` before the new one opens.
	closerWireDisplaced
	// closerRunComplete is the workflow RUN that owns this turn reaching a terminal
	// state, so nothing else is going to close it: a chat-parented run's steps fold
	// onto the launching chat and open a turn there, and the bracket path cannot
	// close that turn because the attribution gate drops a step's own turn_end.
	// APPENDED rather than inserted — no iota value is persisted anywhere, but
	// appending keeps every existing one stable for a reader diffing the block.
	closerRunComplete
)

// deathInterruptCause is what the transcript says when the agent process exited
// mid-turn. A constant because it is the divider's label and the only durable
// record of that cause.
const deathInterruptCause = "The agent process exited before the turn finished."

// displacedTurnCause is what a turn says when a new one started over it: its own
// end never arrived, so nothing else can account for it. Before this the
// displacement persisted `unknown` with no prose at all, which reaches a reader as
// a turn card with a neutral mark and an empty body.
const displacedTurnCause = "The agent started a new turn before this one ended."

// stepRunEndedCause is what a turn a workflow step's frames opened says when the
// run those steps belonged to finished: the step's own end never reached vibekit,
// so the run ending is all there is to account for it.
//
// PUBLIC PROSE. It lands on Message.TurnFailureReason, which the client renders
// into the turn notice for any non-clean severity, so it carries no internal
// machinery vocabulary — no closer, epoch, gate or source names.
const stepRunEndedCause = "The workflow run this turn belongs to finished, and the step's own end never arrived."

// maxReasonBytes bounds a persisted failure reason. The same 2 KiB rpcerr.Text
// gives its own prose, for the same reason: this string's usual source is upstream
// text, and the transcript row that renders it is not a place for a wall of it.
const maxReasonBytes = 2048

// reasonFor settles what a close SAYS, and it is the one place that decision is
// made: the cause whoever closed the turn supplied, else the outcome's own default
// sentence, else nothing at all for a turn that ended cleanly.
//
// Sanitized and capped here rather than at each call site, because every source
// but the local constants above is untrusted upstream text — the wire's
// stopDetails, an RPC error's rendered prose — and a single-line cap is what keeps
// a persisted reason renderable in a one-line footer face.
func reasonFor(o vibekit.TurnOutcome, supplied string) string {
	reason := supplied
	if reason == "" {
		reason = vibekit.DefaultFailureReason(o)
	}
	if reason == "" {
		return ""
	}
	capped, _ := runesafe.SanitizeSingleLineCapped(sanitize.Output(reason), maxReasonBytes, "...")
	return capped
}

// turnClose is what a closer knows about the stop it is reporting.
type turnClose struct {
	// Resp is the session/prompt response, on closerPromptResponse only.
	Resp *vibekit.RPCResponse
	// Reason is the user-facing account of the stop: the rendered prose on
	// closerPromptFailure, the wire's own stopDetails on closerWireEnd. Empty
	// leaves the outcome's default sentence to speak, so a closer with nothing
	// to add supplies nothing rather than inventing wording.
	Reason string
	// Stop is the wire's own stop reason, on closerWireEnd only.
	Stop vibekit.StopReason
	// Epoch names the ONE turn an epoch-scoped closer may end. Left zero only by a
	// closer that sets AnyOpen; see it.
	Epoch vibekit.TurnEpoch
	// Seq is the read loop position the response arrived at, on
	// closerPromptResponse only: the settle waits for the folder to reach it before
	// deciding the wire never closed this turn. Zero skips the wait.
	Seq uint64
	// AnyOpen is the EXPLICIT spelling of "close whatever is open", for a closer
	// that describes the CHAT rather than one turn. Explicit rather than a zero
	// Epoch, because zero is ALSO what StartTurn returns when ctx died while the chat
	// was finalizing. Never set alongside Epoch.
	AnyOpen bool
	Closer  turnCloser
}

// turnStats is a finished turn's two measurements, both DERIVED from the record.
// A struct rather than an adjacent `creditsDelta, elapsedMs float64` pair: a
// transposition compiles, is silent in both directions, and both values are
// persisted on the message.
type turnStats struct {
	CreditsDelta float64
	ElapsedMs    float64
}

// StartTurn opens chatID's turn, run at bridge-ready immediately before the ACP
// call so everything true of the turn is stamped with the bridge live: the
// answering model, and the credit baseline its spend is measured against
// (spawn, prime and MCP wait excluded). Admission is NOT here — the
// reservation was taken synchronously (turn_admission.go), and the priming
// turn's own open/finalize runs between the two untouched. Returns the epoch,
// on which the caller holds a completion handle until ReleaseTurn; zero means
// ctx died while the chat was finalizing. WAITS out a finalize in progress,
// and a prompt-shaped source finding a turn the ENGINE started CLOSES it
// first — no closer can claim that turn through the BRACKET path, so opening
// over it would drop content already streamed to clients. That covers a
// workflow step's turn as well as an agent-initiated one; both are the
// engine's, and a step turn's own closer (the run finishing) may not have
// run yet.
func (bc *BridgeCoordinator) StartTurn(ctx context.Context, chatID vibekit.ChatID, source vibekit.TurnOpenSource) vibekit.TurnEpoch {
	if source.Acknowledgeable() {
		if displaced, ok := bc.displaceEngineTurn(ctx, chatID); ok {
			slog.Info("a prompt displaced a live engine-opened turn",
				"chat_id", chatID, "displaced_epoch", displaced, "source", source)
		}
	}
	model, credits := bc.turnOpenFacts(ctx, chatID, source)
	t := bc.turns.open(ctx, chatID, source, model, credits)
	if t == nil {
		return 0
	}
	if model != "" {
		// Latch the model here: the streaming handler stamps it on the first frame,
		// which can be seconds later and so can pick up a fast in-session switch.
		t.Buf.SetModel(model)
	}
	return t.Epoch
}

// displaceEngineTurn closes an open turn the ENGINE started, so a local step
// taking the chat next neither folds into it nor writes a row into its body.
// Reports the epoch it displaced.
//
// closerWireDisplaced rather than the model-switch closer, which DISCARDS the
// partial: that is right for the user's own turn (they asked for a different
// answer) and wrong for someone else's, whose content is already on every
// client's screen.
func (bc *BridgeCoordinator) displaceEngineTurn(ctx context.Context, chatID vibekit.ChatID) (vibekit.TurnEpoch, bool) {
	displaced, ok := bc.turns.displaceableEngineTurn(chatID)
	if !ok {
		return 0, false
	}
	bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerWireDisplaced, Epoch: displaced})
	return displaced, true
}

// AwaitTurn blocks until the turn named by epoch has finalized and reports what it
// did, so a caller deciding something about its OWN turn reads the turn's account
// rather than state the finalize has consumed. It runs on the caller's goroutine:
// moving the decision into the finalizer deadlocks against the close it awaits.
func (bc *BridgeCoordinator) AwaitTurn(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch) (vibekit.TurnResult, error) {
	return bc.turns.await(ctx, chatID, epoch)
}

// ReleaseTurn gives up the completion handle StartTurn issued. The finalized
// record is dropped when its last handle goes, which is what bounds retention.
func (bc *BridgeCoordinator) ReleaseTurn(chatID vibekit.ChatID, epoch vibekit.TurnEpoch) {
	bc.turns.release(chatID, epoch)
}

// turnOpenFacts reads the two facts a turn records at open. The model comes from
// the chat record rather than from the bridge: the record IS the bridge's reported
// model, and a resumed session's own accessors answer the zero value for whatever
// session/load omitted, which routinely includes the model.
func (bc *BridgeCoordinator) turnOpenFacts(ctx context.Context, chatID vibekit.ChatID, source vibekit.TurnOpenSource) (string, CreditBaseline) {
	ch, ok := bc.chatStore.Get(ctx, chatID)
	if !ok {
		return "", 0
	}
	credits := CreditBaseline(ch.Usage.Credits)
	if source == vibekit.TurnSourceLocalShell {
		return "", credits
	}
	return ch.Model, credits
}

// finalizeTurn claims chatID's turn and runs one closer's effects.
//
// Claim first, effects second, publish third — and the mutex is held for none of
// the effects. Finalizing an epoch twice therefore broadcasts once: the second
// caller loses the claim and returns.
func (bc *BridgeCoordinator) finalizeTurn(ctx context.Context, chatID vibekit.ChatID, tc turnClose) {
	// The wait comes BEFORE the claim: claiming first puts the chat in
	// turnFinalizing, where a fold waits, so the settle would be waiting for a
	// folder it had blocked itself. False means the position can no longer be
	// reached, so the bridge-death closer owns whatever is still open.
	if tc.Seq > 0 && !bc.turns.awaitPosition(ctx, chatID, tc.Epoch, tc.Seq) {
		return
	}
	t, won := bc.claimForCloser(ctx, chatID, tc)
	if !won {
		return
	}
	// Below here the effects are DURABILITY, plus one fan-out that rides them (see
	// closeWithOutcome's push): the closers persist a message already assembled
	// from the live buffer, and both doors into this function are handed a
	// shutdown-cancelled context by construction. Detached HERE and not at the
	// top, because awaitPosition above has ctx.Done() as its only timed escape.
	ctx = durable.Context(ctx)
	var result vibekit.TurnResult
	switch tc.Closer {
	case closerPromptResponse:
		result = bc.closeOnPromptResponse(ctx, t, tc.Resp)
	case closerWireEnd:
		result = bc.closeOnWireEnd(ctx, t, tc.Stop, tc.Reason)
	case closerPromptFailure:
		result = bc.closeAsInterrupted(ctx, t, tc.Reason)
	case closerBridgeDeath:
		result = bc.closeAsInterrupted(ctx, t, deathInterruptCause)
	case closerWireDisplaced:
		result = bc.closeWithOutcome(ctx, t, vibekit.StopReasonUnknown, closerWireDisplaced, displacedTurnCause)
	case closerRunComplete:
		// `unknown` is what vibekit actually knows: the RUN ended, and how the step's
		// own turn ended never arrived. It is also closerWireDisplaced's precedent for
		// the same content — a step turn a prompt displaced already persists this way —
		// and it is what keeps the push silent, since SeverityOf(unknown) is `stopped`
		// and closeWithOutcome's push switch declines that arm.
		//
		// Deriving a stop reason from the RUN's status is DECLINED: a run can complete
		// while a step failed, one turn can hold several steps' content, and `end_turn`
		// would be a claim about a bracket vibekit never saw.
		result = bc.closeWithOutcome(ctx, t, vibekit.StopReasonUnknown, closerRunComplete, stepRunEndedCause)
	case closerModelSwitch:
		result = bc.closeAsDiscarded(ctx, t)
	case closerLocalShell:
		result = bc.closeOnLocalShell(ctx, t)
	}
	bc.turns.finish(t, result)
	// Published by the closer that WON: an agent-initiated turn has no prompt
	// wrapper to advance anything, and a loser would advance a boundary it did not
	// cross.
	if bc.onTurnClosed != nil {
		bc.onTurnClosed(t.Chat, t.Epoch)
	}
}

// claimForCloser claims the turn a closer is ending. An EPOCH claims exactly that
// turn; AnyOpen takes whatever is open, because it describes the chat. Neither
// OPENS a turn to close it, or a bracket for an already-closed turn makes a
// phantom. A ZERO epoch closes nothing — that is what StartTurn returns when ctx
// died, so falling through would let a prompt failure claim a turn it never opened.
func (bc *BridgeCoordinator) claimForCloser(ctx context.Context, chatID vibekit.ChatID, tc turnClose) (*Turn, bool) {
	if tc.AnyOpen {
		// NO AMEND ON THIS BRANCH, and the narrowing is EXPLICIT rather than by
		// omission: the reason rule below applies to epoch-bearing closers only.
		// Three facts settle it, and each one independently forecloses routing an
		// AnyOpen loss through amendLostReason.
		//
		//  1. claimOpen CANNOT TELL A LOSS FROM AN ABSENCE. It returns (nil, false)
		//     both when another closer just finalized the turn and when the chat never
		//     had one — and a bridge death on an idle chat is the common case. Routing
		//     it would need claimOpen to report which, plus an epoch to look the
		//     carrier up by, which an AnyOpen closer does not carry (tc.Epoch is zero
		//     for all four of them; see turnClose.AnyOpen).
		//  2. THE REASON IS NOT ON tc ANYWAY. closerBridgeDeath is dispatched as
		//     turnClose{Closer: closerBridgeDeath, AnyOpen: true}, so tc.Reason is
		//     EMPTY — deathInterruptCause is applied inside finalizeTurn's switch, on
		//     the WINNING path. closerWireDisplaced is the same shape with
		//     displacedTurnCause. So even a routed loss would decline on the
		//     `tc.Reason != ""` conjunct, and making it fire would mean moving two
		//     constants to their dispatch sites purely to feed a rule we are declining.
		//  3. IN THE ONE RACE THAT COSTS SOMETHING, THE WINNER'S ACCOUNT IS TRUER. A
		//     bridge death losing to a wire turn_end means the wire DID deliver the
		//     bracket and the process exited afterwards, so stamping "The agent process
		//     exited before the turn finished." would assert something false about a
		//     turn that finished on the wire. An AnyOpen closer describes the CHAT's
		//     condition; an epoch-bearing closer describes one turn, and only the
		//     second is an account of the turn whose carrier is being amended.
		//
		// Deliberately NO TEST: an "an AnyOpen loss does not amend" assertion cannot
		// fail today, because fact 2 means the existing tc.Reason conjunct already
		// declines for every AnyOpen closer. It would stay green with this narrowing
		// deleted, which pins the wrong mechanism.
		return bc.turns.claimOpen(ctx, chatID)
	}
	if tc.Epoch == 0 {
		slog.Warn("a turn closer named no epoch, so it closed nothing",
			"chat_id", chatID, "closer", tc.Closer)
		return nil, false
	}
	t, won := bc.turns.claimEpoch(ctx, chatID, tc.Epoch)
	if !won {
		// A LOST CLAIM. It is the ordinary outcome of two closers racing one fault —
		// the wire's turn_end and the prompt call's own error both fire for a dropped
		// connection, and whichever arrives second finds the epoch already finalized —
		// so this is not an error and does not log as one. What it IS is the one moment
		// a closer's account of the stop can go nowhere: the winner stamped ITS reason,
		// and the loser is usually the more specific one, since the prompt failure
		// knows the transport error while the wire closer arrives with `stopReason:
		// "error"` and no stopDetails.
		bc.amendLostReason(ctx, chatID, tc)
	}
	return t, won
}

// amendLostReason UPGRADES the persisted reason on a turn whose loser knew more
// than its winner, and never anything else. It is the whole handling of a lost
// claim, the Warn included, so a reader finds one account of that path rather than
// two.
//
// THE SPECIFICITY RULE: a reason is more specific when a CLOSER SUPPLIED it, not
// when it is longer or matches a keyword. The amend fires only when the loser
// supplied a non-empty reason AND the winner's carrier was DEFAULTED from the
// outcome, so it cannot downgrade (a supplied reason never yields to a default) and
// it cannot fight (two supplied reasons: first wins, the discipline
// turnRegistry.interrupt already uses for the interrupt cause). No per-closer
// priority order is invented, which would be a private ranking beside the shared
// severity table.
//
// EPOCH-BEARING CLOSERS ONLY. An AnyOpen closer's lost claim never reaches here,
// and claimForCloser's own AnyOpen branch carries the three reasons why.
//
// IT NEVER STAMPS A REASON ONTO A CARRIER WITH NOTHING TO SAY. reasonFor returns
// the loser's prose whatever the carrier's outcome, and ReasonSupplied is false for
// a `completed` winner precisely because DefaultFailureReason(completed) is "" — so
// without a gate a wire end_turn racing a prompt failure writes turn_failure_reason
// onto a completed message. Reachable: closerWireEnd is AnyOpen and wins,
// closerPromptFailure carries the epoch and loses. Invisible on screen today
// (turnFailureText returns "" for severity `clean` before it reads any row, and no
// Go reader consumes the field), so this is record integrity.
//
// The gate is `vibekit.DefaultFailureReason(m.TurnOutcome) != ""`, not a severity
// comparison: it is the same predicate reasonFor already uses to decide whether the
// outcome HAS a fallback, and DefaultFailureReason's own doc states that contract
// ("a caller can use the empty string as 'there is nothing to say here' rather than
// testing the severity again"). A SeverityOf(...) == clean test would be a second
// spelling of one fact. It can only be evaluated INSIDE the UpdateMessage mutate
// closure, because that is where the carrier's outcome is known, so the closure
// reports back whether it wrote.
//
// ONLY THE REASON MOVES. The outcome, the stop reason, the truncation flag and the
// stats stay the winner's: the winner settled how the turn ENDED, and changing that
// would change every surface's verdict rather than what it says.
//
// It cannot resurrect a deleted or truncated turn BY CONSTRUCTION rather than by a
// guard, and the guarantee is UpdateMessage's own callback rather than Mutate's:
// Mutate's !exists branch CONSTRUCTS an in-memory chat and runs the mutator, so what
// refuses is the callback returning false — for an absent chat, and for an absent
// message id, which is what a truncation leaves behind. A false callback skips the
// save, so no write, no broadcast, no phantom. Mutate's tombstone refuses auto-create
// on top of that, for the window it covers.
//
// The write is DETACHED: it carries the turn's own account of why it failed, which
// nothing re-derives, and the two doors into finalizeTurn are handed a
// shutdown-cancelled context by construction. It runs above finalizeTurn's own seam
// because the claim does, so it owns the decision here.
func (bc *BridgeCoordinator) amendLostReason(ctx context.Context, chatID vibekit.ChatID, tc turnClose) {
	carrier, found := bc.turns.carrierOf(chatID, tc.Epoch)
	// FOUR DECLINE MESSAGES, one per cause, rather than one line with conditional
	// fields. Each is the ordinary case rather than a defect, and each has a
	// different remedy, so the frequency of each has to stay separately measurable.
	// EVERY ATTR IS A FACT SOMETHING READ: `winner_reason_supplied` appears only on
	// the arms where a carrier was found (with `found` false it was being read off a
	// zero-value turnCarrier{} and always reported false, which reads as a fact about
	// a winner that recorded nothing), and `outcome` only on the arm where the mutate
	// closure ran to read one. Literal key-value calls rather than a variadic attr
	// slice, because sloglint runs kv-only.
	if !found {
		slog.Warn("a turn closer lost its claim and the winner recorded no carrier, so its reason was not used",
			"chat_id", chatID, "closer", tc.Closer, "epoch", tc.Epoch,
			"loser_had_reason", tc.Reason != "")
		return
	}
	if tc.Reason == "" || carrier.ReasonSupplied {
		slog.Warn("a turn closer lost its claim and its reason was not used",
			"chat_id", chatID, "closer", tc.Closer, "epoch", tc.Epoch,
			"loser_had_reason", tc.Reason != "",
			"winner_reason_supplied", carrier.ReasonSupplied)
		return
	}
	var outcome vibekit.TurnOutcome
	var ran, wrote bool
	err := bc.chatStore.UpdateMessage(durable.Context(ctx), chatID, carrier.MessageID, func(m *vibekit.Message) {
		// THE CLOSURE REPORTS THAT IT RAN, and that is what separates the two
		// declines below. UpdateMessage returns nil when the chat does not exist or
		// the message id is absent — its own loop simply finds no row — so without
		// this flag a row-not-found outcome is indistinguishable from the gate
		// declining, and the gate's message would name a predicate that was never
		// evaluated. Reachable: a rewind truncation leaves the carrier's row absent,
		// and Rewind is two clicks from the turn footer.
		ran = true
		outcome = m.TurnOutcome
		// THE GATE. Evaluated here because this is the only place the carrier's own
		// outcome is known; see the doc comment for why the predicate is
		// DefaultFailureReason rather than a severity test.
		if vibekit.DefaultFailureReason(m.TurnOutcome) == "" {
			return
		}
		wrote = true
		m.TurnFailureReason = reasonFor(m.TurnOutcome, tc.Reason)
	})
	if err != nil {
		slog.Error("amend a lost closer's reason onto the turn's carrier",
			"chat_id", chatID, "closer", tc.Closer, "epoch", tc.Epoch, "error", err)
		return
	}
	if !ran {
		// No outcome to report, because nothing read one. The remedy differs from
		// every other arm's: this is the record having moved under the amend rather
		// than a rule declining, so there is nothing to reconsider about the rule.
		slog.Warn("a turn closer lost its claim and the winner's carrier is no longer in the record, so its reason was not used",
			"chat_id", chatID, "closer", tc.Closer, "epoch", tc.Epoch,
			"loser_had_reason", tc.Reason != "",
			"winner_reason_supplied", carrier.ReasonSupplied)
		return
	}
	if !wrote {
		slog.Warn("a turn closer lost its claim and the winner's outcome has nothing to say, so its reason was not used",
			"chat_id", chatID, "closer", tc.Closer, "epoch", tc.Epoch,
			"loser_had_reason", tc.Reason != "",
			"winner_reason_supplied", carrier.ReasonSupplied, "outcome", outcome)
		return
	}
	slog.Warn("a turn closer lost its claim, so its reason was upgraded onto the carrier",
		"chat_id", chatID, "closer", tc.Closer, "epoch", tc.Epoch, "outcome", outcome)
}

// turnStatsFor derives the turn's spend and duration from its own record.
func (bc *BridgeCoordinator) turnStatsFor(ctx context.Context, t *Turn) turnStats {
	st := turnStats{ElapsedMs: float64(time.Since(t.Opened).Milliseconds())}
	if ch, ok := bc.chatStore.Get(ctx, t.Chat); ok {
		st.CreditsDelta = ch.Usage.Credits - float64(t.Credits)
	}
	return st
}

// settleBuffer flushes what the steering filter was withholding and THEN takes the
// turn's content in one guarded read. The ORDER is the invariant: the carry can
// hold a turn's only final text — a reply ending in `[` looks like the start of a
// steering acknowledgement — so an emptiness check taken before the flush reads an
// ordinary turn as empty and the empty-turn recovery re-prompts an answered
// question. A nil buffer means no frame ever arrived.
func settleBuffer(buf *buffer.Buffer) buffer.TurnContent {
	if buf == nil {
		return buffer.TurnContent{EmittedNothing: true}
	}
	translate.FlushSteerCarry(buf)
	return buf.TakeTurn()
}

// persistTurnReply commits a finalized turn's assistant message where every client
// already places it, keyed on the turn's SOURCE. An ENGINE-opened turn's content
// interleaves with the reader's own prompts, so its reply goes AHEAD of the trailing
// user rows already on disk; a turn vibekit opened must NOT, because its own trigger
// row IS that tail. Keyed on the closer the rule was widened per closer and left
// incomplete twice; both routes broadcast message_appended, so only FILE order moves.
func (bc *BridgeCoordinator) persistTurnReply(ctx context.Context, t *Turn, msg *vibekit.Message) {
	if t.Source.EngineOpened() {
		bc.persistDisplacedTurn(ctx, t.Chat, msg)
		return
	}
	bc.persistTurn(ctx, t.Chat, msg)
}

// closeOnPromptResponse finalizes a turn on the response that settled it — the
// LOCAL fallback, running only when the wire's own turn_end never arrived, so its
// outcome can be nothing richer than end_turn or cancelled.
func (bc *BridgeCoordinator) closeOnPromptResponse(ctx context.Context, t *Turn, resp *vibekit.RPCResponse) vibekit.TurnResult {
	// No reason of its own: the response carries a stop reason and no prose, so
	// reasonFor falls through to the outcome's default sentence.
	return bc.closeWithOutcome(ctx, t, extractStopReason(resp), closerPromptResponse, "")
}

// closeOnWireEnd finalizes a turn the ENGINE closed: same effects as the local
// fallback, but the outcome came off the wire, so WireEnded is set. `details` is
// the wire's own account of the stop (turn_end's stopDetails), empty on every
// build that sends none.
func (bc *BridgeCoordinator) closeOnWireEnd(ctx context.Context, t *Turn, stop vibekit.StopReason, details string) vibekit.TurnResult {
	return bc.closeWithOutcome(ctx, t, stop, closerWireEnd, details)
}

// closeWithOutcome persists the turn's assistant message with its DURABLE outcome,
// announces the end with the turn's stats, and pushes. It reads as one sequence of
// named phases, each owning the facts its own decision rests on. It takes the CLOSER
// rather than a bare wireEnded flag because two of those phases still read which
// local step is ending the turn.
func (bc *BridgeCoordinator) closeWithOutcome(
	ctx context.Context,
	t *Turn,
	stopReason vibekit.StopReason,
	closer turnCloser,
	reason string,
) vibekit.TurnResult {
	// WireEnded is deliberately NOT widened for closerRunComplete: this closer keys
	// on a RUN-level frame, not on the turn's own bracket, and WireEnded's only
	// reader is the empty-turn recovery's arming gate, which is about a prompt this
	// closer never touches.
	wireEnded := closer == closerWireEnd || closer == closerWireDisplaced
	chatID := t.Chat
	c := bc.concludeStop(chatID, stopReason, reason)
	if t.Source == vibekit.TurnSourcePrime {
		// A PRIME persists and broadcasts NOTHING: its frames are vibekit's own transcript
		// replay, so a row would put the priming preamble in the conversation, a turn_ended
		// shows text the next reload deletes, and the tool-fail broadcast bypasses the
		// fold-time mute. The content is still TAKEN, or the next turn extends these blocks.
		// Below concludeStop, whose warns are about a stop reason off the same wire.
		snap := settleBuffer(t.Buf)
		return vibekit.TurnResult{Stop: stopReason, EmittedNothing: snap.EmittedNothing, WireEnded: wireEnded}
	}
	// Read BEFORE the turn_ended broadcast below: emit() clears the chat's status as
	// that event goes out, so a read at the push site finds nothing.
	statusDesc := bc.statusDescription(chatID)
	stats := bc.turnStatsFor(ctx, t)

	snap := bc.settleTurnContent(ctx, t, stopReason, closer)
	p := bc.persistTurnContent(ctx, t, &snap, c, stats)
	// One fact set, whichever event carries it: the turn_ended payload below reports
	// the same numbers, so a carrier that omitted them showed the footer live and
	// lost it on reload.
	facts := turnOutcomeFacts{
		ChangedFiles: p.ChangedFiles,
		Conclusion:   c,
		Model:        p.Model,
		Stats:        stats,
	}
	persisted := bc.recordTurnCarrier(ctx, t, p, &facts, stopReason, reason != "")

	// GATED on a carrier: a close that deliberately persists nothing announces nothing,
	// because turn_ended means "this chat's turn ended" and every effect of one lands on
	// the launching chat's OWN last turn, which did not end. COST, recorded not fixed:
	// hasOpenTurn counts a step turn, so a client holding turn_open from an earlier fetch
	// keeps rendering its own newest turn as running until its next one.
	if persisted {
		if _, stillExists := bc.chatStore.Get(ctx, chatID); stillExists {
			bc.broadcast(ctx, vibekit.NewEvent(vibekit.EventTurnEnded, chatID, vibekit.TurnEndedPayload{
				Outcome:      c.Outcome,
				StopReason:   stopReason,
				Truncated:    c.Truncated,
				Refusal:      p.Refusal,
				Model:        p.Model,
				CreditsDelta: stats.CreditsDelta,
				ElapsedMs:    stats.ElapsedMs,
				ChangedFiles: p.ChangedFiles,
			}))
		}
	}
	bc.pushTurnOutcome(ctx, chatID, c, statusDesc)
	return vibekit.TurnResult{Stop: stopReason, EmittedNothing: snap.EmittedNothing, WireEnded: wireEnded}
}

// concludeStop grades the stop and settles what the close SAYS: the reason travels ON
// the conclusion, so every carrier stamps it from one field rather than each
// remembering to, and a clean turn resolves to "". It also complains when the grading
// is unusable — an unmapped stop logs ONCE per distinct value, so a wire vibekit has
// not seen is discoverable without a line per turn.
func (bc *BridgeCoordinator) concludeStop(
	chatID vibekit.ChatID,
	stop vibekit.StopReason,
	reason string,
) vibekit.TurnConclusion {
	c := vibekit.ConcludeStopReason(stop)
	if !c.Known {
		if _, seen := bc.unknownStops.LoadOrStore(stop, struct{}{}); !seen {
			slog.Warn("a turn ended on a stop reason vibekit does not map",
				"chat_id", chatID, "stop_reason", stop)
		}
	}
	c.Reason = reasonFor(c.Outcome, reason)
	if c.Reason == "" && vibekit.SeverityOf(c.Outcome) == vibekit.TurnSeverityBroken {
		// Unreachable while DefaultFailureReason covers every broken outcome, and
		// logged rather than asserted because the cost of being wrong is a turn card
		// with a red mark and an empty body — the exact defect this field exists to
		// remove, and one nothing else would report.
		slog.Warn("a broken turn closed with no reason to show",
			"chat_id", chatID, "outcome", c.Outcome, "stop_reason", stop)
	}
	return c
}

// settleTurnContent fails the tool calls nothing can still settle and THEN takes the
// turn's content. The order is the invariant: a card persisted `in_progress` renders as
// a permanent spinner on every later reload. closerRunComplete joins the cancel because
// the run's terminal transition ends the only thing that could still send a
// tool_call_update for a step's call; closerWireDisplaced's latent twin is out of scope.
func (bc *BridgeCoordinator) settleTurnContent(
	ctx context.Context,
	t *Turn,
	stopReason vibekit.StopReason,
	closer turnCloser,
) buffer.TurnContent {
	if buf := t.Buf; buf != nil && (stopReason == stopReasonCancelled || closer == closerRunComplete) {
		bc.failInFlightTools(ctx, t.Chat, buf)
	}
	return settleBuffer(t.Buf)
}

// persistedTurn is what a close committed, plus the facts read from the same
// snapshot that the turn_ended payload reports.
type persistedTurn struct {
	// ChangedFiles is the TURN's map, cumulative across its segments, nil when the
	// turn touched nothing.
	ChangedFiles map[string]*vibekit.FileChange
	// Refusal is the model's own refusal metadata, which only a carried turn has.
	Refusal *vibekit.RefusalInfo
	// MessageID names the assistant row this persisted, empty when it persisted none.
	MessageID string
	// Model is the model that ANSWERED, falling back to the record's value at open.
	Model string
	// Carried is whether an assistant message holds the outcome. Not
	// `!EmittedNothing`: Started means a message id went out, so a fully-withheld
	// turn still persists an empty message, and that message is the carrier.
	Carried bool
}

// persistTurnContent commits whatever the turn produced and reports the facts the
// payload needs from the same read. The segmented arm exists because a turn SPLIT at a
// compaction point can end with nothing after the split while its changed-file map
// spans every segment; both arms read the buffer's latched model, which is the model
// that ANSWERED where t.Model is only the record's value at open.
func (bc *BridgeCoordinator) persistTurnContent(
	ctx context.Context,
	t *Turn,
	snap *buffer.TurnContent,
	c vibekit.TurnConclusion,
	stats turnStats,
) persistedTurn {
	p := persistedTurn{Model: t.Model}
	switch {
	case snap.Started:
		p.ChangedFiles = snap.ChangedFiles
		p.Refusal = snap.Refusal
		p.Model = cmp.Or(snap.Model, t.Model)
		p.MessageID = snap.MessageID
		msg := assistantTurnMessage(snap, stats, p.Model, c)
		bc.persistTurnReply(ctx, t, &msg)
		p.Carried = true
	case snap.Segmented:
		p.ChangedFiles = snap.ChangedFiles
		p.Model = cmp.Or(snap.Model, t.Model)
	}
	return p
}

// recordTurnCarrier records WHICH persisted row carries this turn's outcome, minting a
// marker when no row does, and reports whether one was persisted at all.
//
// The carrier lets a closer that LOST the claim here amend its reason (amendLostReason);
// `reasonSupplied` is that rule's specificity test. The bool is false ONLY where
// persistsEmptyCarrier declines, never for an append that failed — the client is told.
func (bc *BridgeCoordinator) recordTurnCarrier(
	ctx context.Context,
	t *Turn,
	p persistedTurn,
	facts *turnOutcomeFacts,
	stopReason vibekit.StopReason,
	reasonSupplied bool,
) bool {
	carrier := turnCarrier{MessageID: p.MessageID, ReasonSupplied: reasonSupplied}
	persisted := p.Carried
	switch {
	case stopReason == stopReasonCancelled:
		cancelID := bc.appendEventMessage(ctx, t.Chat, vibekit.EventCancelled, "", carrierFor(facts, p.Carried))
		persisted = true
		if !p.Carried {
			carrier.MessageID = cancelID
		}
	case !p.Carried && persistsEmptyCarrier(t):
		// No assistant message to stamp, so a marker carries the outcome. Skipped for a
		// cancel, whose own event message is already this turn's marker, and declined
		// for a turn whose source leaves no row at all — see persistsEmptyCarrier.
		carrier.MessageID = bc.persistOutcomeMarker(ctx, t, facts)
		persisted = true
	}
	bc.turns.recordCarrier(t, carrier)
	return persisted
}

// pushTurnOutcome sends the off-screen notification a finished turn earns, reading the
// SEVERITY so it cannot claim success over a failure. The client half
// (static-src/handlers/turn.ts) makes the same switch and the two agree BY
// CONSTRUCTION: both read one table the shared severity fixture pins, so no string is
// authored here. On finalizeTurn's DETACHED context deliberately — the push fans out on
// its own goroutine, and the caller's context is one runPromptTurn defers a cancel of.
func (bc *BridgeCoordinator) pushTurnOutcome(
	ctx context.Context,
	chatID vibekit.ChatID,
	c vibekit.TurnConclusion,
	statusDesc string,
) {
	switch vibekit.SeverityOf(c.Outcome) {
	case vibekit.TurnSeverityClean:
		bc.NotifyPush(ctx, agentFinishedBodyFrom(statusDesc), vibekit.PushKindAgentFinished, chatID)
	case vibekit.TurnSeverityBroken:
		bc.NotifyPush(ctx, vibekit.DefaultFailureReason(c.Outcome), vibekit.PushKindAgentFinished, chatID)
	case vibekit.TurnSeverityStopped, vibekit.TurnSeverityRunning:
		// A cancel is what the reader asked for and an unreadable end reports nothing,
		// so neither earns an off-screen notification. `running` cannot reach a close.
	}
}

// turnOutcomeFacts is what a turn-boundary event stamps when THAT event is the
// turn's carrier: how the turn ended, plus the footer numbers no assistant message
// is left to hold.
//
// One type rather than four parameters, because the set travels together and the
// rule over it is a single one — exactly one persisted message per turn may carry
// it, since its presence closes the turn for both projections.
type turnOutcomeFacts struct {
	// ChangedFiles is the turn's cumulative map, nil when an assistant message
	// carries it or the turn touched nothing.
	ChangedFiles map[string]*vibekit.FileChange
	// Model is which model answered. A footer fact like the two below, because
	// the client's turn ledger reads it off every row in the turn's body — so a
	// turn whose only carrier is an event showed its numbers with no model.
	Model      string
	Conclusion vibekit.TurnConclusion
	// Stats are the turn's credits and duration. Left zero by a caller whose
	// footer is deliberately empty — an interrupted turn has no spend to
	// attribute.
	Stats turnStats
}

// persistsEmptyCarrier reports whether a turn that carried NOTHING may leave a row
// saying how it ended. Both empty-turn sites read it, because the ruling is a
// property of the TURN rather than of the closer: for a WORKFLOW STEP nothing carried
// means no message_created went out, so no transcript divergence exists for a row to
// close, and what a row WOULD do is open a headless card in the wrong conversation.
func persistsEmptyCarrier(t *Turn) bool {
	return t.Source != vibekit.TurnSourceWorkflowStep
}

// persistOutcomeMarker records how a turn that emitted NOTHING ended: with no
// assistant message this is the outcome's only carrier, and a turn with no user
// message is its only persisted trace at all.
//
// It skips NOTHING, and that is the load-bearing half. It used to omit the marker
// for a clean prompted empty turn, on the reasoning that `completed` is only what
// the derivation answers by default and the turn's own user message already
// represents it — which made the writer and the reader ONE mechanism: the writer
// left the fact out because the reader guessed it. A legitimately clean empty turn
// and one a restart killed were then byte-identical on disk, so the reader could
// not tell them apart and answered `completed` for both. Now an absent carrier
// MEANS nothing closed the turn, which is what lets deriveTurnOutcome say `unknown`
// honestly instead of guessing. Cost: one invisible EventTurnOutcome row per clean
// empty prompted turn, which is the fact.
func (bc *BridgeCoordinator) persistOutcomeMarker(ctx context.Context, t *Turn, f *turnOutcomeFacts) string {
	return bc.appendEventMessage(ctx, t.Chat, vibekit.EventTurnOutcome, "", f)
}

// carrierFor answers which marker carries the turn's outcome: none when an
// assistant message already did.
func carrierFor(f *turnOutcomeFacts, alreadyCarried bool) *turnOutcomeFacts {
	if alreadyCarried {
		return nil
	}
	return f
}

// closeAsInterrupted finalizes a turn that stopped without the engine answering
// it, KEEPING the partial (invariant 1). `reason` becomes the divider's label, and
// a cause claimed on the turn beats it. A PRIME persists and broadcasts nothing at
// all.
//
// A model switch takes closeAsDiscarded instead, which is what removed this
// function's `persist` parameter: the third mode resolved the partial in the
// opposite direction AND concluded a different outcome, so it was never a flag on
// this one.
func (bc *BridgeCoordinator) closeAsInterrupted(ctx context.Context, t *Turn, reason string) vibekit.TurnResult {
	chatID := t.Chat
	cause := bc.turns.interruptCause(t)
	if cause != "" {
		reason = string(cause)
	} else {
		cause = vibekit.InterruptCause(reason)
	}
	c := vibekit.ConcludeStopReason(vibekit.StopReasonInterrupted)
	// The same prose the divider carries, ALSO stamped on the carrier: a divider is
	// skipped whenever the turn already carried its outcome, and it is the client's
	// collapsed-face lookup that reads it, so a reason living only there is
	// unreachable from an OPEN turn's body. Stamping both is what makes the account
	// survive independently of which row happens to be rendered.
	c.Reason = reasonFor(c.Outcome, reason)
	result := vibekit.TurnResult{
		Stop:           vibekit.StopReasonInterrupted,
		Interrupt:      cause,
		EmittedNothing: true,
	}

	buf := t.Buf
	// Flush before measuring, and before the message below is built from the same
	// buffer; see settleBuffer.
	snap := settleBuffer(buf)
	result.EmittedNothing = snap.EmittedNothing
	if t.Source == vibekit.TurnSourcePrime {
		return result
	}
	if !snap.Started {
		if !persistsEmptyCarrier(t) {
			// A STEP turn that carried nothing persists and announces nothing, the shape
			// closeAsDiscarded's empty branch already has. The footer argument below does not
			// transfer: it turns on this chat having a turn card whose outcome deriveOutcome
			// reads, and an empty step turn persists no row, so projectTurns opens none.
			return result
		}
		// Nothing streamed, so there is no partial to persist -- but the divider still
		// lands AND the turn still ends: without a marker the client's deriveOutcome
		// reads the turn as `completed` and suppresses its footer, so a rate-limited
		// turn renders indistinguishably from a clean short answer.
		//
		// No stats, for the same reason the persisted partial below carries none: an
		// interrupted turn has no spend to attribute. The changed files and the model
		// ARE carried — a SPLIT turn always takes this branch, so nothing else can.
		dividerID := bc.appendEventMessage(ctx, chatID, vibekit.EventInterrupted, reason, &turnOutcomeFacts{
			ChangedFiles: snap.ChangedFiles,
			Conclusion:   c,
			Model:        cmp.Or(snap.Model, t.Model),
		})
		bc.turns.recordCarrier(t, turnCarrier{MessageID: dividerID, ReasonSupplied: reason != ""})
		bc.announceConclusion(ctx, chatID, c)
		return result
	}
	// Fail the in-flight tool calls and RE-READ the content, or the persisted turn
	// carries running tool cards that a reload renders as permanent spinners.
	bc.failInFlightTools(ctx, chatID, buf)
	snap = buf.TakeTurn()

	// No stats: an interrupted turn has no credit delta to attribute, so the footer
	// is deliberately empty rather than carrying the failed call's consumption.
	msg := assistantTurnMessage(&snap, turnStats{}, cmp.Or(snap.Model, t.Model), c)
	bc.persistTurnReply(ctx, t, &msg)
	bc.turns.recordCarrier(t, turnCarrier{MessageID: msg.ID, ReasonSupplied: reason != ""})

	// The divider does NOT re-carry the outcome: the message above already did, and
	// two carriers in one turn open a spurious segment.
	bc.appendEventMessage(ctx, chatID, vibekit.EventInterrupted, reason, nil)
	bc.announceConclusion(ctx, chatID, c)
	return result
}

// announceConclusion tells every client a LOCALLY closed turn is over. It is the
// ONE place the interrupted and discarded ends are broadcast, and the client has no
// second door: an `error` frame deliberately touches no turn state, so a path that
// skips this leaves `thinking`, Cancel, the transient banners, the snapshot
// watermark and the rail live indefinitely.
//
// The stop reason comes off the CONCLUSION rather than being a literal here, which
// is what lets one site serve both closers: the interrupt path concludes
// `interrupted` and the discard path `cancelled`, and each has to announce its own
// or the live channel disagrees with what the reload derives.
func (bc *BridgeCoordinator) announceConclusion(ctx context.Context, chatID vibekit.ChatID, c vibekit.TurnConclusion) {
	bc.broadcast(ctx, vibekit.NewEvent(vibekit.EventTurnEnded, chatID,
		vibekit.TurnEndedPayload{Outcome: c.Outcome, StopReason: c.RawStop}))
}

// closeAsDiscarded finalizes a turn a MODEL SWITCH threw away: the reader asked for
// a different answer, so the partial is moot and none of it is persisted.
//
// It concludes `cancelled` rather than `interrupted`, and that is the whole reason
// it is a closer of its own. A discard must read neither alarming nor clean, and the
// two channels have to agree: `interrupted` grades BROKEN, so live the tab dot went
// red for a switch the reader asked for, while on reload the derivation found no
// carrier at all and answered `completed`, which hides the turn's footer glyph.
// `cancelled` is already graded `stopped` by the shared severity table and already
// carries its own default sentence, so this needs no new outcome, no new severity
// case, no new prose and no wire change.
//
// A marker is PERSISTED so the reload reads the verdict the live client was told.
// `EventCancelled` renders as a skip (static-src/messages-events.ts), so it adds no
// visible row; `turnFailureText` supplies the notice from the severity table. No
// stats, matching the rule the interrupted path already follows: a turn with no
// answer has no spend to attribute.
func (bc *BridgeCoordinator) closeAsDiscarded(ctx context.Context, t *Turn) vibekit.TurnResult {
	c := vibekit.ConcludeStopReason(vibekit.StopReasonCancelled)
	c.Reason = reasonFor(c.Outcome, "")
	// TurnResult.Stop stays `interrupted`: its one reader is recoverEmptyTurn's
	// `== StopReasonEndTurn` gate, false either way, so moving it would change a
	// field no consumer reads.
	result := vibekit.TurnResult{Stop: vibekit.StopReasonInterrupted, EmittedNothing: true}
	// Flush before measuring; see settleBuffer. The content is TAKEN and dropped,
	// which is what discarding means — the next turn must not extend these blocks.
	snap := settleBuffer(t.Buf)
	result.EmittedNothing = snap.EmittedNothing
	if t.Source == vibekit.TurnSourcePrime {
		return result
	}
	if !snap.Started {
		// A switch with nothing in flight persists and announces nothing: the chat was
		// idle and the restart is invisible.
		return result
	}
	markerID := bc.appendEventMessage(ctx, t.Chat, vibekit.EventCancelled, "", &turnOutcomeFacts{
		ChangedFiles: snap.ChangedFiles,
		Conclusion:   c,
		Model:        cmp.Or(snap.Model, t.Model),
	})
	// THE CARRIER IS RECORDED, as closeAsInterrupted does at both of its own persist
	// points. Without it t.carrier.MessageID stays empty, so every amend against a
	// model-switch winner declines with `winner_carrier=false` and a losing closer's
	// real transport error goes nowhere.
	//
	// ReasonSupplied is FALSE because it is: `c.Reason` above is
	// `reasonFor(cancelled, "")`, i.e. DEFAULTED from the outcome, which is exactly
	// the shape the amend rule upgrades. A discard deliberately keeping the generic
	// sentence is the alternative and it is wrong — the reason a discard has nothing
	// to say is that the reader asked for a different answer, not that a closer
	// holding a real transport error should be silenced.
	//
	// The race is real rather than synthetic: closerModelSwitch claims with AnyOpen
	// and closerPromptFailure carries an epoch, so a model switch during a failing
	// prompt has the switch WIN and the prompt failure lose — and the loser is the
	// one holding the transport error's own prose.
	bc.turns.recordCarrier(t, turnCarrier{MessageID: markerID, ReasonSupplied: false})
	bc.announceConclusion(ctx, t.Chat, c)
	return result
}

// closeOnLocalShell finalizes a `!cmd` turn. The output is already persisted by
// the interception itself, so the end is all that is left to announce.
func (bc *BridgeCoordinator) closeOnLocalShell(ctx context.Context, t *Turn) vibekit.TurnResult {
	bc.broadcast(ctx, vibekit.NewEvent(vibekit.EventTurnEnded, t.Chat,
		vibekit.TurnEndedPayload{
			Outcome:    vibekit.TurnOutcomeCompleted,
			StopReason: vibekit.StopReasonEndTurn,
		}))
	return vibekit.TurnResult{Stop: vibekit.StopReasonEndTurn}
}

// failInFlightTools marks the buffer's running tool calls failed and tells every
// client, so a reload does not render permanent spinners for work that stopped. The
// message id comes back WITH the changed calls rather than being read off the
// buffer: this runs on the settling goroutine, not the dispatch loop.
func (bc *BridgeCoordinator) failInFlightTools(ctx context.Context, chatID vibekit.ChatID, buf *buffer.Buffer) {
	messageID, changed := buf.MarkCancelledToolsFailed()
	for i := range changed {
		bc.broadcast(ctx, vibekit.NewEvent(vibekit.EventToolCallUpdate, chatID,
			vibekit.ToolCallUpdatePayload{MessageID: messageID, ToolCall: changed[i]}))
	}
}

// appendEventMessage records a turn-boundary event on the transcript, carrying the
// turn's outcome when carries is non-nil. For EventInterrupted the content is the
// divider's label, the transcript's only account of the stop that survives a
// reload. It reports the id it minted, so a caller writing the turn's CARRIER can
// record which message that is.
//
// EXACTLY ONE persisted message per turn may carry TurnOutcome: its presence closes
// the turn for both projections, so a second one opens a spurious segment.
func (bc *BridgeCoordinator) appendEventMessage(
	ctx context.Context,
	chatID vibekit.ChatID,
	kind vibekit.EventKind,
	content string,
	carries *turnOutcomeFacts,
) string {
	evt := vibekit.Message{
		ID:        newMessageID(),
		Role:      vibekit.RoleEvent,
		Ts:        time.Now().UnixMilli(),
		EventKind: kind,
		Content:   content,
	}
	if carries != nil {
		evt.TurnOutcome = carries.Conclusion.Outcome
		evt.TurnStopReasonRaw = carries.Conclusion.RawStop
		evt.TurnTruncated = carries.Conclusion.Truncated
		evt.TurnFailureReason = carries.Conclusion.Reason
		evt.TurnCredits = carries.Stats.CreditsDelta
		evt.TurnElapsedMs = carries.Stats.ElapsedMs
		evt.ChangedFiles = carries.ChangedFiles
		evt.TurnModel = carries.Model
	}
	if err := bc.chatStore.AppendMessage(ctx, chatID, &evt); err != nil {
		slog.Error("persist turn boundary event", "chat_id", chatID, "kind", kind, "error", err)
		// The row did not land, so no id on disk names it and an amend keyed on this
		// would rewrite nothing (or worse, a row some later append happens to mint the
		// same way). "" is the honest answer.
		return ""
	}
	return evt.ID
}
