package agent

// The REASON, durable: what a finalized turn records about WHY it ended badly. The outcome
// was durable and the account of it was not, and that asymmetry is the whole defect —
// `closeWithOutcome` passed an empty content string and stamped no marker once the turn had
// streamed something, so a turn that failed on the wire's own `stopReason: "error"` persisted
// a settled `failed`, a red footer mark, three changed files, and no sentence anywhere.
// Measured on the live chat file: 26 blocks, zero prose; the cause reached the reader through
// a 12-second toast and nothing else.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// carrierOf returns the ONE persisted message carrying this chat's turn outcome —
// the assistant message that finalized the turn, or the marker written when the
// turn emitted nothing. Exactly one message per turn may carry it, which is what
// makes "the carrier" a single thing to assert against.
func carrierOf(t *testing.T, cs *fakeChatStore, chatID vibekit.ChatID) *vibekit.Message {
	t.Helper()
	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		t.Fatalf("chat %q vanished", chatID)
	}
	var carrier *vibekit.Message
	for i := range c.Messages {
		if c.Messages[i].TurnOutcome != "" {
			carrier = &c.Messages[i]
		}
	}
	if carrier == nil {
		t.Fatal("no persisted message carries a turn outcome")
	}
	return carrier
}

// eventRecorder is a chat.Store broadcaster that counts what the STORE emitted.
//
// The SSE replay ring cannot answer this: the store's own broadcaster is wired by
// the composition root, and hubOnDisk builds a store with none — so a store-level
// broadcast is invisible there whether or not it happened, which is the shape a
// no-broadcast assertion must not be built on.
type eventRecorder struct {
	mu     sync.Mutex
	counts map[vibekit.EventType]int
}

func (r *eventRecorder) Broadcast(_ context.Context, evt vibekit.ServerEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts[evt.Type]++
}

func (r *eventRecorder) count(want vibekit.EventType) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[want]
}

// hubOnDiskWatchingBroadcasts is hubOnDisk with the store's own broadcaster wired,
// so a test can assert on what the STORE announced rather than only on what landed.
func hubOnDiskWatchingBroadcasts(t *testing.T, chatID vibekit.ChatID) (*Runtime, *chat.Store, *eventRecorder) {
	t.Helper()
	rec := &eventRecorder{counts: map[vibekit.EventType]int{}}
	cs, err := chat.NewStore(t.TempDir(), chat.WithBroadcaster(rec))
	if err != nil {
		t.Fatalf("chat.NewStore: %v", err)
	}
	h := New(t.Context(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, cs)
	if err := cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	return h, cs, rec
}

// startedTurnReturningEpoch is startedTurnOn plus the epoch, which the
// prompt-failure closer needs in order to name the turn it is ending.
func startedTurnReturningEpoch(
	t *testing.T,
	h *Runtime,
	cs *fakeChatStore,
	chatID vibekit.ChatID,
	text string,
) vibekit.TurnEpoch {
	t.Helper()
	seedChat(t, cs, chatID)
	epoch := h.coord.StartTurn(t.Context(), chatID, vibekit.TurnSourcePrompt)
	buf := h.stageTurnBuffer(t, chatID)
	buf.Started = true
	buf.MessageID = newMessageID()
	buf.Content.WriteString(text)
	return epoch
}

// TestCloseOnWireEnd_PersistsAReasonWithNothingUpstreamToSayIt is the direct pin on
// symptom 1's shape: a wire `error` close on a turn that HAS streamed content, which
// writes no divider and no marker, so the assistant carrier is the only place a
// reason can live.
func TestCloseOnWireEnd_PersistsAReasonWithNothingUpstreamToSayIt(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "here is half an answer")

	// No stopDetails, which is every build measured so far.
	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonError, "")

	carrier := carrierOf(t, cs, "c1")
	if carrier.Role != vibekit.RoleAssistant {
		t.Errorf("carrier role = %q, want assistant — a streamed turn carries its own outcome", carrier.Role)
	}
	if carrier.TurnFailureReason == "" {
		t.Fatal("the carrier records no reason; a failed turn with no reason renders " +
			"as a red mark over an empty body, which is the reported defect")
	}
	if want := vibekit.DefaultFailureReason(vibekit.TurnOutcomeFailed); carrier.TurnFailureReason != want {
		t.Errorf("reason = %q, want the outcome's default %q", carrier.TurnFailureReason, want)
	}
}

// TestCloseOnWireEnd_PrefersTheWiresOwnAccountOfTheStop pins that stopDetails, when
// a build sends it, beats the default sentence. It is the only channel on this path
// that can say anything specific, and it was decoded by nobody.
func TestCloseOnWireEnd_PrefersTheWiresOwnAccountOfTheStop(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "half an answer")

	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonError, "The upstream model dropped the stream.")

	if got := carrierOf(t, cs, "c1").TurnFailureReason; got != "The upstream model dropped the stream." {
		t.Errorf("reason = %q, want the wire's own stopDetails", got)
	}
}

// TestCloseOnWireEnd_SanitizesAndBoundsTheReason keeps the reason on the same
// footing as every other untrusted upstream string that reaches a persisted record:
// its usual source is the agent's own text.
func TestCloseOnWireEnd_SanitizesAndBoundsTheReason(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "half an answer")

	// An ANSI escape, a zero-width space and far more bytes than a transcript row
	// should ever render.
	hostile := "\x1b[31mred\x1b[0m\u200bzero" + strings.Repeat("x", maxReasonBytes*2)
	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonError, hostile)

	got := carrierOf(t, cs, "c1").TurnFailureReason
	if len(got) > maxReasonBytes {
		t.Errorf("reason is %d bytes, want at most %d", len(got), maxReasonBytes)
	}
	if strings.Contains(got, "\x1b") {
		t.Error("reason kept an ANSI escape")
	}
	if strings.Contains(got, "\u200b") {
		t.Error("reason kept a zero-width space, which is a prompt-injection carrier")
	}
}

// TestCloseOnWireEnd_AnEmptyFailedTurnsMarkerCarriesTheReason is turn 2 of the
// reported chat: a turn that emitted nothing, whose only persisted trace is the
// outcome marker. `EVENT_RENDER_MAP` skips a `turn_outcome` row, so before the
// reason the body rendered literally nothing and the footer's lead word was the
// entire report.
func TestCloseOnWireEnd_AnEmptyFailedTurnsMarkerCarriesTheReason(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	epoch := h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)
	t.Cleanup(func() { h.coord.ReleaseTurn("c1", epoch) })

	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonError, "")

	carrier := carrierOf(t, cs, "c1")
	if carrier.Role != vibekit.RoleEvent || carrier.EventKind != vibekit.EventTurnOutcome {
		t.Fatalf("carrier is %q/%q, want an event/turn_outcome marker", carrier.Role, carrier.EventKind)
	}
	if carrier.TurnFailureReason == "" {
		t.Error("the marker records no reason, so the turn renders an empty body and " +
			"a lead word with nothing explaining it")
	}
}

// TestCloseAsInterrupted_StampsTheReasonOnTheCarrierAsWellAsTheDivider is the other
// half of the durability story, and the divider is NOT sufficient on its own: it is
// skipped whenever the turn already carried its outcome, and the client's face
// lookup that read it only ran for a FOLDED card — while a broken turn is precisely
// the turn that never auto-folds. Stamping both is what makes the account reachable
// whichever row is rendered.
func TestCloseAsInterrupted_StampsTheReasonOnTheCarrierAsWellAsTheDivider(t *testing.T) {
	h, cs, _ := newTestHub()
	epoch := startedTurnReturningEpoch(t, h, cs, "c1", "half an answer")

	const reason = "A network error occurred. Please check your connection and try again."
	h.AbandonInFlightTurn(t.Context(), "c1", epoch, reason)

	if got := carrierOf(t, cs, "c1").TurnFailureReason; got != reason {
		t.Errorf("carrier reason = %q, want the prompt failure's own prose %q", got, reason)
	}
	c, _ := cs.Get(t.Context(), "c1")
	var divider *vibekit.Message
	for i := range c.Messages {
		if c.Messages[i].EventKind == vibekit.EventInterrupted {
			divider = &c.Messages[i]
		}
	}
	if divider == nil || divider.Content != reason {
		t.Error("the interrupted divider lost its prose; the two surfaces must agree")
	}
}

// --- THE LOST CLAIM ---
// Two closers race one fault, the winner stamps its reason, and the loser is usually the one
// that knows more, so the account reaching the record was the LESS specific of the two. The
// measured sequence: the wire's turn_end wins with `stopReason: "error"` and no stopDetails,
// so reasonFor falls through to the outcome's default sentence while the prompt-failure closer
// arriving second holds the transport error's own prose and drops it.

// TestLostClaim_UpgradesTheCarriersReasonToTheLosersProse is chat 1's sequence.
func TestLostClaim_UpgradesTheCarriersReasonToTheLosersProse(t *testing.T) {
	h, cs, _ := newTestHub()
	epoch := startedTurnReturningEpoch(t, h, cs, "c1", "half an answer")

	// The WIRE wins, with nothing of its own to say.
	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonError, "")
	if got := carrierOf(t, cs, "c1").TurnFailureReason; got != vibekit.DefaultFailureReason(vibekit.TurnOutcomeFailed) {
		t.Fatalf("the fixture did not reproduce the defect: carrier reason = %q, want the default", got)
	}

	// The prompt failure arrives second and loses the claim.
	const prose = "A network error occurred. Please check your connection and try again."
	h.AbandonInFlightTurn(t.Context(), "c1", epoch, prose)

	carrier := carrierOf(t, cs, "c1")
	if carrier.TurnFailureReason != prose {
		t.Errorf("carrier reason = %q, want the loser's own prose %q", carrier.TurnFailureReason, prose)
	}
	// ONLY the reason moves: the winner settled how the turn ended, and changing
	// that would change every surface's verdict rather than what it says.
	if carrier.TurnOutcome != vibekit.TurnOutcomeFailed {
		t.Errorf("carrier outcome = %q, want the winner's failed", carrier.TurnOutcome)
	}
	if carrier.TurnStopReasonRaw != vibekit.StopReasonError {
		t.Errorf("carrier raw stop = %q, want the winner's %q", carrier.TurnStopReasonRaw, vibekit.StopReasonError)
	}
}

// TestLostClaim_DoesNotDowngradeASuppliedReason is the direction that makes this an
// UPGRADE rather than last-writer-wins. Reverse the order and the wire's EMPTY
// reason must not replace the interrupt path's prose.
func TestLostClaim_DoesNotDowngradeASuppliedReason(t *testing.T) {
	h, cs, _ := newTestHub()
	epoch := startedTurnReturningEpoch(t, h, cs, "c1", "half an answer")

	const prose = "A network error occurred. Please check your connection and try again."
	h.AbandonInFlightTurn(t.Context(), "c1", epoch, prose)

	// The wire's turn_end arrives second, carrying no stopDetails.
	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonError, "")

	carrier := carrierOf(t, cs, "c1")
	if carrier.TurnFailureReason != prose {
		t.Errorf("carrier reason = %q, want the interrupt path's own prose %q kept; a default "+
			"overwriting a supplied reason is the rule inverted", carrier.TurnFailureReason, prose)
	}
	if carrier.TurnOutcome != vibekit.TurnOutcomeInterrupted {
		t.Errorf("carrier outcome = %q, want the winner's interrupted", carrier.TurnOutcome)
	}
}

// TestLostClaim_TwoSuppliedReasonsKeepTheFirst pins the no-fight half: the winner
// already said something specific, so the loser's equally-specific prose does not
// relabel it. Same first-wins discipline turnRegistry.interrupt uses for the cause.
func TestLostClaim_TwoSuppliedReasonsKeepTheFirst(t *testing.T) {
	h, cs, _ := newTestHub()
	epoch := startedTurnReturningEpoch(t, h, cs, "c1", "half an answer")

	const first = "The upstream model dropped the stream."
	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonError, first)
	h.AbandonInFlightTurn(t.Context(), "c1", epoch, "A network error occurred.")

	if got := carrierOf(t, cs, "c1").TurnFailureReason; got != first {
		t.Errorf("carrier reason = %q, want the winner's own %q", got, first)
	}
}

// TestLostClaim_ADeletedCarrierIsNotResurrected is the no-phantom half, and it holds by
// construction rather than by a guard: UpdateMessage's own loop reports no match for an absent
// id, which is what a truncation leaves behind, and Mutate's !exists branch covers a deleted
// chat. It runs against the REAL store because the broadcast is half the property — a
// recording fake performs the same no-op write and emits nothing either way.
func TestLostClaim_ADeletedCarrierIsNotResurrected(t *testing.T) {
	const chatID vibekit.ChatID = "c1"
	h, cs, rec := hubOnDiskWatchingBroadcasts(t, chatID)
	epoch, buf := h.stagePromptTurn(t, chatID)
	buf.StartTurn(newMessageID())
	buf.AppendTextDelta("half an answer", "")
	h.coord.WireTurnEnd(t.Context(), chatID, vibekit.StopReasonError, "")

	// A rewind between the two closers: the carrier is gone from the record.
	if err := cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool {
		c.Messages = nil
		return true
	}); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	before := rec.count(vibekit.EventMessageUpdated)

	h.AbandonInFlightTurn(t.Context(), chatID, epoch, "A network error occurred.")

	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		t.Fatalf("chat %q vanished", chatID)
	}
	if len(c.Messages) != 0 {
		t.Errorf("the amend re-created %d messages on a truncated turn: %+v", len(c.Messages), c.Messages)
	}
	if got := rec.count(vibekit.EventMessageUpdated); got != before {
		t.Errorf("message_updated broadcasts = %d, want %d: an amend that matched nothing "+
			"still told every client something had changed", got, before)
	}
}

// TestLostClaim_AnAbsentCarrierRowIsNotReportedAsTheGateDeclining is the other half of the
// truncation case above, and it pins a DECISION rather than prose: which of the amend's four
// declines ran, read off which facts the line carries. `Store.UpdateMessage` returns nil when
// the chat or the message id is absent, so `wrote` stays false and `outcome` stays the ZERO
// VALUE — a single decline arm then reports "the winner's outcome has nothing to say" for a
// case where the outcome was never read. The discriminator is the ABSENCE of the `outcome`
// attribute, not the wording: an arm may only report a field something read.
func TestLostClaim_AnAbsentCarrierRowIsNotReportedAsTheGateDeclining(t *testing.T) {
	const chatID vibekit.ChatID = "c1"
	h, cs, _ := hubOnDiskWatchingBroadcasts(t, chatID)
	epoch, buf := h.stagePromptTurn(t, chatID)
	buf.StartTurn(newMessageID())
	buf.AppendTextDelta("half an answer", "")
	h.coord.WireTurnEnd(t.Context(), chatID, vibekit.StopReasonError, "")

	// A rewind between the two closers: the carrier's row is gone, so the mutate
	// closure never runs.
	if err := cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool {
		c.Messages = nil
		return true
	}); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	logs := captureLogs(t)
	h.AbandonInFlightTurn(t.Context(), chatID, epoch, "A network error occurred.")
	line := logs.String()

	if !strings.Contains(line, `"loser_had_reason":true`) {
		t.Fatalf("the amend did not reach a decline arm at all; log was:\n%s", line)
	}
	if strings.Contains(line, `"outcome"`) {
		t.Errorf("the decline reports an `outcome` attribute, but no closure ran to read one, "+
			"so the value is a zero-value TurnOutcome presented as a fact; log was:\n%s", line)
	}
}

// TestLostClaim_ACleanCarriersDeclineDoesReportTheOutcome is the positive direction
// of the case above, and it is what makes that assertion mean something: with the row
// PRESENT the closure runs, so the outcome IS a fact and the arm must say which one
// it read. Without this the absence assertion is satisfied by an amend that never
// reports an outcome anywhere.
func TestLostClaim_ACleanCarriersDeclineDoesReportTheOutcome(t *testing.T) {
	h, cs, _ := newTestHub()
	epoch := startedTurnReturningEpoch(t, h, cs, "c1", "the whole answer")
	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonEndTurn, "")

	logs := captureLogs(t)
	h.AbandonInFlightTurn(t.Context(), "c1", epoch, "A network error occurred.")
	line := logs.String()

	if !strings.Contains(line, `"outcome":"completed"`) {
		t.Errorf("the gate's decline does not name the outcome it read, which is the one fact "+
			"that says why the amend declined; log was:\n%s", line)
	}
}

// TestLostClaim_AmendsThroughTheRealStoreAndSaysSo is the same upgrade against the
// real store, which is what proves the write LANDS on disk and is ANNOUNCED. The
// recording fake performs no broadcast for an UpdateMessage, so the fake-backed
// cases above pin the value and this one pins the durability and the fan-out —
// without which the amended reason is reachable only by a reload.
func TestLostClaim_AmendsThroughTheRealStoreAndSaysSo(t *testing.T) {
	const chatID vibekit.ChatID = "c1"
	h, cs, rec := hubOnDiskWatchingBroadcasts(t, chatID)
	epoch, buf := h.stagePromptTurn(t, chatID)
	buf.StartTurn(newMessageID())
	buf.AppendTextDelta("half an answer", "")
	h.coord.WireTurnEnd(t.Context(), chatID, vibekit.StopReasonError, "")
	before := rec.count(vibekit.EventMessageUpdated)

	const prose = "A network error occurred. Please check your connection and try again."
	h.AbandonInFlightTurn(t.Context(), chatID, epoch, prose)

	c, _ := cs.Get(t.Context(), chatID)
	var carrier *vibekit.Message
	for i := range c.Messages {
		if c.Messages[i].TurnOutcome != "" {
			carrier = &c.Messages[i]
		}
	}
	if carrier == nil {
		t.Fatalf("no carrier on disk: %+v", c.Messages)
	}
	if carrier.TurnFailureReason != prose {
		t.Errorf("persisted reason = %q, want the loser's prose %q", carrier.TurnFailureReason, prose)
	}
	if got := rec.count(vibekit.EventMessageUpdated); got != before+1 {
		t.Errorf("message_updated broadcasts = %d, want %d: a landed amend the clients are "+
			"never told about is a reason only a reload can reach", got, before+1)
	}
}

// TestSeverityOfEveryClose_RecordsSomethingToShow is the property behind the cases
// above, over the closers rather than over the outcome table: whatever path ends a
// turn, a reader must not be left with a mark and no words. `internal/vibekit`'s own
// tests pin the table; this pins that the FINALIZER actually reaches it.
func TestSeverityOfEveryClose_RecordsSomethingToShow(t *testing.T) {
	stops := []vibekit.StopReason{
		vibekit.StopReasonError,
		vibekit.StopReasonRefusal,
		vibekit.StopReasonContentFiltered,
		vibekit.StopReasonInterrupted,
		vibekit.StopReasonCancelled,
		vibekit.StopReasonUnknown,
	}
	for _, stop := range stops {
		t.Run(string(stop), func(t *testing.T) {
			h, cs, _ := newTestHub()
			startedTurnOn(t, h, cs, "c1", "half an answer")
			h.coord.WireTurnEnd(t.Context(), "c1", stop, "")

			carrier := carrierOf(t, cs, "c1")
			if vibekit.SeverityOf(carrier.TurnOutcome) == vibekit.TurnSeverityClean {
				t.Skipf("%q grades clean, so there is nothing to report", stop)
			}
			if carrier.TurnFailureReason == "" {
				t.Errorf("a %q close (outcome %q) recorded no reason",
					stop, carrier.TurnOutcome)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The amend's two remaining edges: the discard path's carrier, and a carrier with
// nothing to say.
// ---------------------------------------------------------------------------

// TestLostClaim_ADiscardedTurnsMarkerCanStillBeAmended: `closeAsDiscarded` throwing away
// appendEventMessage's returned id leaves `t.carrier.MessageID` empty, so every amend against
// a model-switch winner declines with `winner_carrier=false` — including this one, where the
// loser holds a real transport error and the winner's sentence is defaulted. The order is not
// synthetic: closerModelSwitch claims with AnyOpen and closerPromptFailure carries an epoch,
// so a model switch during a failing prompt has the switch WIN.
func TestLostClaim_ADiscardedTurnsMarkerCanStillBeAmended(t *testing.T) {
	h, cs, _ := newTestHub()
	epoch := startedTurnReturningEpoch(t, h, cs, "c1", "half an answer")

	// The model switch wins, discarding the partial.
	h.coord.FlushInFlightTurnOnSwitch(t.Context(), "c1")
	carrier := carrierOf(t, cs, "c1")
	if carrier.Role != vibekit.RoleEvent || carrier.EventKind != vibekit.EventCancelled {
		t.Fatalf("carrier is %q/%q, want an event/cancelled marker", carrier.Role, carrier.EventKind)
	}
	if want := vibekit.DefaultFailureReason(vibekit.TurnOutcomeCancelled); carrier.TurnFailureReason != want {
		t.Fatalf("the fixture did not reproduce the shape: carrier reason = %q, want the default %q",
			carrier.TurnFailureReason, want)
	}

	// The prompt failure arrives second and loses the claim.
	const prose = "A network error occurred. Please check your connection and try again."
	h.AbandonInFlightTurn(t.Context(), "c1", epoch, prose)

	got := carrierOf(t, cs, "c1")
	if got.TurnFailureReason != prose {
		t.Errorf("carrier reason = %q, want the loser's own prose %q; a discard that records no "+
			"carrier silences a closer holding a real transport error", got.TurnFailureReason, prose)
	}
	// Only the reason moves — a discard is still a discard.
	if got.TurnOutcome != vibekit.TurnOutcomeCancelled {
		t.Errorf("carrier outcome = %q, want the winner's cancelled", got.TurnOutcome)
	}
}

// TestLostClaim_NeverStampsAReasonOnACleanCarrier: `reasonFor` returns the loser's prose
// whatever the carrier's outcome, and `ReasonSupplied` is false for a `completed` winner
// because `DefaultFailureReason(completed)` is "" — so without the gate a wire `end_turn`
// racing a prompt failure writes `turn_failure_reason` onto a turn that SUCCEEDED. Invisible
// on screen today, so what this pins is record integrity rather than a rendered defect.
func TestLostClaim_NeverStampsAReasonOnACleanCarrier(t *testing.T) {
	h, cs, _ := newTestHub()
	epoch := startedTurnReturningEpoch(t, h, cs, "c1", "the whole answer")

	// The wire's end_turn wins: outcome `completed`, carried on the assistant message.
	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonEndTurn, "")
	carrier := carrierOf(t, cs, "c1")
	if carrier.TurnOutcome != vibekit.TurnOutcomeCompleted {
		t.Fatalf("the fixture did not reproduce the shape: carrier outcome = %q, want completed",
			carrier.TurnOutcome)
	}
	if carrier.TurnFailureReason != "" {
		t.Fatalf("a completed carrier already records a reason %q", carrier.TurnFailureReason)
	}

	// The prompt failure arrives second, loses the claim, and has prose to offer.
	h.AbandonInFlightTurn(t.Context(), "c1", epoch, "A network error occurred.")

	if got := carrierOf(t, cs, "c1").TurnFailureReason; got != "" {
		t.Errorf("carrier reason = %q, want empty: a turn that SUCCEEDED must not record why it failed", got)
	}
}
