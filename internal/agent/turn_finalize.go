package agent

// The one path every terminal step takes: each step is a CLOSER handed to
// finalizeTurn, which claims the turn first-wins, runs that closer's effects with
// no lock held, and publishes the result.

import (
	"cmp"
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/vibekit/internal/buffer"
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
)

// deathInterruptCause is what the transcript says when the agent process exited
// mid-turn. A constant because it is the divider's label and the only durable
// record of that cause.
const deathInterruptCause = "The agent process exited before the turn finished."

// turnClose is what a closer knows about the stop it is reporting.
type turnClose struct {
	// Resp is the session/prompt response, on closerPromptResponse only.
	Resp *vibekit.RPCResponse
	// Reason is the user-facing account of the stop, on closerPromptFailure only.
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
// and a prompt-shaped source finding a turn the WIRE started CLOSES it
// first — no closer can claim that turn, so opening over it would drop
// content already streamed to clients.
func (bc *BridgeCoordinator) StartTurn(ctx context.Context, chatID vibekit.ChatID, source vibekit.TurnOpenSource) vibekit.TurnEpoch {
	if source.Acknowledgeable() {
		if displaced, ok := bc.turns.displaceableWireTurn(chatID); ok {
			slog.Info("a prompt displaced a live agent-initiated turn",
				"chat_id", chatID, "displaced_epoch", displaced, "source", source)
			bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerWireDisplaced, Epoch: displaced})
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
	var result vibekit.TurnResult
	switch tc.Closer {
	case closerPromptResponse:
		result = bc.closeOnPromptResponse(ctx, t, tc.Resp)
	case closerWireEnd:
		result = bc.closeOnWireEnd(ctx, t, tc.Stop)
	case closerPromptFailure:
		result = bc.closeAsInterrupted(ctx, t, tc.Reason, true)
	case closerBridgeDeath:
		result = bc.closeAsInterrupted(ctx, t, deathInterruptCause, true)
	case closerWireDisplaced:
		result = bc.closeOnWireEnd(ctx, t, vibekit.StopReasonUnknown)
	case closerModelSwitch:
		result = bc.closeAsInterrupted(ctx, t, "", false)
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
		return bc.turns.claimOpen(ctx, chatID)
	}
	if tc.Epoch == 0 {
		slog.Warn("a turn closer named no epoch, so it closed nothing",
			"chat_id", chatID, "closer", tc.Closer)
		return nil, false
	}
	return bc.turns.claimEpoch(ctx, chatID, tc.Epoch)
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

// closeOnPromptResponse finalizes a turn on the response that settled it — the
// LOCAL fallback, running only when the wire's own turn_end never arrived, so its
// outcome can be nothing richer than end_turn or cancelled.
func (bc *BridgeCoordinator) closeOnPromptResponse(ctx context.Context, t *Turn, resp *vibekit.RPCResponse) vibekit.TurnResult {
	return bc.closeWithOutcome(ctx, t, extractStopReason(resp), false)
}

// closeOnWireEnd finalizes a turn the ENGINE closed: same effects as the local
// fallback, but the outcome came off the wire, so WireEnded is set.
func (bc *BridgeCoordinator) closeOnWireEnd(ctx context.Context, t *Turn, stop vibekit.StopReason) vibekit.TurnResult {
	return bc.closeWithOutcome(ctx, t, stop, true)
}

// closeWithOutcome persists the turn's assistant message with its DURABLE outcome,
// announces the end with the turn's stats, and pushes. A PRIME turn does none of
// it: its frames are vibekit's own transcript replay, so persisting them would put
// the priming preamble in the conversation and broadcasting them shows text that
// vanishes on the next reload.
func (bc *BridgeCoordinator) closeWithOutcome(ctx context.Context, t *Turn, stopReason vibekit.StopReason, wireEnded bool) vibekit.TurnResult {
	chatID := t.Chat
	c := bc.concludeStop(chatID, stopReason)
	// Read BEFORE the turn_ended broadcast below: emit() clears the chat's status as
	// that event goes out, so a read at the push site finds nothing.
	statusDesc := bc.statusDescription(chatID)
	stats := bc.turnStatsFor(ctx, t)

	var changedFiles map[string]*vibekit.FileChange
	var refusal *vibekit.RefusalInfo
	model := t.Model
	silent := t.Source == vibekit.TurnSourcePrime
	// Whether an assistant message CARRIES the outcome. Not `!EmittedNothing`:
	// Started means a message id went out, so a fully-withheld turn still persists
	// an empty message, and that message is the carrier.
	carried := false

	// Fail the tools BEFORE the content is taken, so the persisted message carries
	// their final statuses rather than the spinners a reload would render. A prime's
	// are left alone: this broadcast bypasses the fold-time mute.
	if buf := t.Buf; buf != nil && !silent && stopReason == stopReasonCancelled {
		bc.failInFlightTools(ctx, chatID, buf)
	}
	snap := settleBuffer(t.Buf)
	if snap.Started && !silent {
		changedFiles = snap.ChangedFiles
		refusal = snap.Refusal
		model = cmp.Or(snap.Model, t.Model)
		msg := assistantTurnMessage(&snap, stats, model, c)
		bc.persistTurn(ctx, chatID, &msg)
		carried = true
	}
	if silent {
		return vibekit.TurnResult{Stop: stopReason, EmittedNothing: snap.EmittedNothing, WireEnded: wireEnded}
	}

	if stopReason == stopReasonCancelled {
		bc.appendEventMessage(ctx, chatID, vibekit.EventCancelled, "", carrierFor(&c, carried))
	} else if !carried {
		// No assistant message to stamp, so a marker carries the outcome. Skipped
		// above for a cancel, whose own event message is already this turn's marker.
		bc.persistOutcomeMarker(ctx, t, c)
	}

	if _, stillExists := bc.chatStore.Get(ctx, chatID); stillExists {
		bc.broadcast(ctx, vibekit.NewEvent(vibekit.EventTurnEnded, chatID, vibekit.TurnEndedPayload{
			Outcome:      c.Outcome,
			StopReason:   stopReason,
			Truncated:    c.Truncated,
			Refusal:      refusal,
			Model:        model,
			CreditsDelta: stats.CreditsDelta,
			ElapsedMs:    stats.ElapsedMs,
			ChangedFiles: changedFiles,
		}))
	}

	if stopReason != stopReasonCancelled {
		bc.NotifyPush(ctx, agentFinishedBodyFrom(statusDesc), vibekit.PushKindAgentFinished, chatID)
	}
	return vibekit.TurnResult{Stop: stopReason, EmittedNothing: snap.EmittedNothing, WireEnded: wireEnded}
}

// concludeStop reads a stop reason and logs an unmapped one ONCE per distinct
// value, so a wire vibekit has not seen is discoverable without a line per turn.
// The mapping itself computes and stays silent; the finalizer is the supervisor.
func (bc *BridgeCoordinator) concludeStop(chatID vibekit.ChatID, stop vibekit.StopReason) vibekit.TurnConclusion {
	c := vibekit.ConcludeStopReason(stop)
	if !c.Known {
		if _, seen := bc.unknownStops.LoadOrStore(stop, struct{}{}); !seen {
			slog.Warn("a turn ended on a stop reason vibekit does not map",
				"chat_id", chatID, "stop_reason", stop)
		}
	}
	return c
}

// persistOutcomeMarker records how a turn that emitted NOTHING ended: with no
// assistant message this is the outcome's only carrier, and a turn with no user
// message is its only persisted trace at all. Skipped only when it adds nothing — a
// completed, untruncated outcome on a turn the transcript already represents
// through its own user message.
func (bc *BridgeCoordinator) persistOutcomeMarker(ctx context.Context, t *Turn, c vibekit.TurnConclusion) {
	if c.Outcome == vibekit.TurnOutcomeCompleted && !c.Truncated && t.Source.HasUserTrigger() {
		return
	}
	bc.appendEventMessage(ctx, t.Chat, vibekit.EventTurnOutcome, "", &c)
}

// carrierFor answers which marker carries the turn's outcome: none when an
// assistant message already did.
func carrierFor(c *vibekit.TurnConclusion, alreadyCarried bool) *vibekit.TurnConclusion {
	if alreadyCarried {
		return nil
	}
	return c
}

// closeAsInterrupted finalizes a turn that stopped without the engine answering
// it. `reason` becomes the divider's label, and a cause claimed on the turn beats
// it. `persist` is the direction the caller resolves the partial in: a failed
// prompt and a dead bridge KEEP it (invariant 1), a model switch DISCARDS it and
// writes no divider, and a PRIME persists and broadcasts nothing at all.
func (bc *BridgeCoordinator) closeAsInterrupted(ctx context.Context, t *Turn, reason string, persist bool) vibekit.TurnResult {
	chatID := t.Chat
	cause := bc.turns.interruptCause(t)
	if cause != "" {
		reason = string(cause)
	} else {
		cause = vibekit.InterruptCause(reason)
	}
	c := vibekit.ConcludeStopReason(vibekit.StopReasonInterrupted)
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
	if !persist {
		// A switch with nothing in flight announces nothing: the chat was idle and
		// the restart is invisible.
		if snap.Started {
			bc.announceInterrupted(ctx, chatID, c)
		}
		return result
	}
	if !snap.Started {
		// Nothing streamed, so there is no partial to persist -- but the divider still
		// lands AND the turn still ends: without a marker the client's deriveOutcome
		// reads the turn as `completed` and suppresses its footer, so a rate-limited
		// turn renders indistinguishably from a clean short answer.
		bc.appendEventMessage(ctx, chatID, vibekit.EventInterrupted, reason, &c)
		bc.announceInterrupted(ctx, chatID, c)
		return result
	}
	// Fail the in-flight tool calls and RE-READ the content, or the persisted turn
	// carries running tool cards that a reload renders as permanent spinners.
	bc.failInFlightTools(ctx, chatID, buf)
	snap = buf.TakeTurn()

	// No stats: an interrupted turn has no credit delta to attribute, so the footer
	// is deliberately empty rather than carrying the failed call's consumption.
	msg := assistantTurnMessage(&snap, turnStats{}, cmp.Or(snap.Model, t.Model), c)
	bc.persistTurn(ctx, chatID, &msg)

	// The divider does NOT re-carry the outcome: the message above already did, and
	// two carriers in one turn open a spurious segment.
	bc.appendEventMessage(ctx, chatID, vibekit.EventInterrupted, reason, nil)
	bc.announceInterrupted(ctx, chatID, c)
	return result
}

// announceInterrupted tells every client the turn is over. It is the ONE place the
// interrupted end is broadcast, and the client has no second door: an `error` frame
// deliberately touches no turn state, so a path that skips this leaves `thinking`,
// Cancel, the transient banners, the snapshot watermark and the rail live
// indefinitely.
func (bc *BridgeCoordinator) announceInterrupted(ctx context.Context, chatID vibekit.ChatID, c vibekit.TurnConclusion) {
	bc.broadcast(ctx, vibekit.NewEvent(vibekit.EventTurnEnded, chatID,
		vibekit.TurnEndedPayload{Outcome: c.Outcome, StopReason: vibekit.StopReasonInterrupted}))
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
// reload.
//
// EXACTLY ONE persisted message per turn may carry TurnOutcome: its presence closes
// the turn for both projections, so a second one opens a spurious segment.
func (bc *BridgeCoordinator) appendEventMessage(
	ctx context.Context,
	chatID vibekit.ChatID,
	kind vibekit.EventKind,
	content string,
	carries *vibekit.TurnConclusion,
) {
	evt := vibekit.Message{
		ID:        newMessageID(),
		Role:      vibekit.RoleEvent,
		Ts:        time.Now().UnixMilli(),
		EventKind: kind,
		Content:   content,
	}
	if carries != nil {
		evt.TurnOutcome = carries.Outcome
		evt.TurnStopReasonRaw = carries.RawStop
		evt.TurnTruncated = carries.Truncated
	}
	if err := bc.chatStore.AppendMessage(ctx, chatID, &evt); err != nil {
		slog.Error("persist turn boundary event", "chat_id", chatID, "kind", kind, "error", err)
	}
}
