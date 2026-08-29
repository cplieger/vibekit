package agent

// The read-loop sequence and the parked settle.
//
// Every test here drives frames through consumeFrame — Forward's body minus its
// range loop — rather than through a live Forward goroutine, and that is what
// makes them deterministic: holding the folder is not calling it, and filling the
// pipe is queuing the frames the test will hand it. The frames, their sequences,
// the deferred advance and the settle's wait are all the production ones.

import (
	"slices"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// The headline ordering property, and the fault it closes: a settle taken on the
// response alone decides the wire never closed the turn while the wire's own
// turn_end is still unread, and the turn then ends carrying the prompt response's
// outcome with the content that arrived behind the response missing.
//
// The discriminator is sharp in BOTH directions: the wire says `refusal`, the
// response says `end_turn`, and the chunk arrives behind the response.
func TestSettle_WaitsForQueuedFramesAndTakesTheWireOutcome(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	gen := h.coord.turns.attachForward(chatID)
	epoch := h.StartTurn(ctx, chatID, vibekit.TurnSourcePrompt)
	defer h.ReleaseTurn(chatID, epoch)

	// The pipe: the wire has already delivered the bracket and the reply, and the
	// folder has consumed neither.
	queued := []vibekit.Notification{
		{Msg: newTurnStartMsg(), Seq: 1},
		{Msg: newChunkMsg("the whole reply"), Seq: 2},
		{Msg: newTurnEndMsg("refusal"), Seq: 3},
	}

	_, before := h.bus.fanout.Bounds()
	settled := make(chan struct{})
	go func() {
		defer close(settled)
		h.SettleTurnOnResponse(ctx, chatID, epoch, 3,
			&vibekit.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})})
	}()
	waitForParkedSettle(t, h.coord.turns, chatID, epoch, 3)

	for _, n := range queued {
		h.coord.consumeFrame(chatID, gen, n)
	}
	<-settled

	ends := payloadsOfType[vibekit.TurnEndedPayload](t, bufferedSince(h, before), vibekit.EventTurnEnded)
	if len(ends) != 1 {
		t.Fatalf("turn_ended count = %d, want exactly 1 complete turn", len(ends))
	}
	if ends[0].StopReason != "refusal" {
		t.Errorf("stop reason = %q, want the WIRE's %q: the local settle ran on the "+
			"response while turn_end was still queued", ends[0].StopReason, "refusal")
	}
	result, err := h.AwaitTurn(ctx, chatID, epoch)
	if err != nil {
		t.Fatalf("AwaitTurn: %v", err)
	}
	if !result.WireEnded {
		t.Error("the turn reports WireEnded false, so the empty-turn recovery is armed on a locally-closed turn")
	}
	if result.EmittedNothing {
		t.Error("the turn reports EmittedNothing, so the settle measured it before the reply was folded")
	}
	c, _ := cs.Get(ctx, chatID)
	if !hasAssistantContent(c, "the whole reply") {
		t.Errorf("the persisted turn does not carry the reply that arrived behind the response; messages = %+v", c.Messages)
	}
}

// A settle whose last delivered frame folds NOTHING still closes, which is why the
// position advances for every frame CONSUMED. Measured: on a caught
// publishTerminalEvent failure `turn_completion` is sent BEFORE the turn-end
// broadcast, so bound to folds this settle parks forever and `thinking` never
// clears — on the exact path the local fallback exists for.
func TestSettle_ClosesWhenTheLastDeliveredFrameFoldsNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	gen := h.coord.turns.attachForward(chatID)
	epoch := h.StartTurn(ctx, chatID, vibekit.TurnSourcePrompt)
	defer h.ReleaseTurn(chatID, epoch)

	// No turn_end: this is the fault path where the bracket never comes, and the
	// trailing frame is metering.
	queued := []vibekit.Notification{
		{Msg: newChunkMsg("half an answer"), Seq: 1},
		{Msg: newTurnCompletionMsg(), Seq: 2},
	}

	_, before := h.bus.fanout.Bounds()
	settled := make(chan struct{})
	go func() {
		defer close(settled)
		h.SettleTurnOnResponse(ctx, chatID, epoch, 2,
			&vibekit.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})})
	}()
	waitForParkedSettle(t, h.coord.turns, chatID, epoch, 2)

	for _, n := range queued {
		h.coord.consumeFrame(chatID, gen, n)
	}
	<-settled

	ends := payloadsOfType[vibekit.TurnEndedPayload](t, bufferedSince(h, before), vibekit.EventTurnEnded)
	if len(ends) != 1 {
		t.Fatalf("turn_ended count = %d, want 1: the settle parked behind a frame that folds nothing", len(ends))
	}
	result, err := h.AwaitTurn(ctx, chatID, epoch)
	if err != nil {
		t.Fatalf("AwaitTurn: %v", err)
	}
	if result.WireEnded {
		t.Error("a turn no bracket closed reports WireEnded true")
	}
}

// A closer armed for turn N, firing after turn N+1 opened, closes NOTHING.
//
// Epoch-scoped claiming is what makes a stale closer harmless. Before it, a
// closer took whatever was open, so a settle that arrived late — a slow persist,
// a re-ordered response — ended the NEXT turn and announced its own outcome for
// it.
func TestSettle_ArmedForAnEarlierTurnClosesNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	first := h.StartTurn(ctx, chatID, vibekit.TurnSourcePrompt)
	defer h.ReleaseTurn(chatID, first)
	h.SettleTurnOnResponse(ctx, chatID, first, 0,
		&vibekit.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})})

	second := h.StartTurn(ctx, chatID, vibekit.TurnSourcePrompt)
	defer h.ReleaseTurn(chatID, second)

	_, before := h.bus.fanout.Bounds()
	h.SettleTurnOnResponse(ctx, chatID, first, 0,
		&vibekit.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "cancelled"})})

	if ends := payloadsOfType[vibekit.TurnEndedPayload](t, bufferedSince(h, before), vibekit.EventTurnEnded); len(ends) != 0 {
		t.Errorf("a closer armed for turn %d announced an end after turn %d opened: %+v", first, second, ends)
	}
	if _, open := h.coord.turns.openEpoch(chatID); !open {
		t.Error("the second turn is no longer open, so the stale closer took it")
	}
}

// A bridge dying mid-prompt closes the turn ONCE, does not panic, and the parked
// settle defers to the death closer.
//
// The deference is unconditional by design: the death actor names the process
// that went away, where the settle could only report the prompt response's own
// outcome for a turn whose cause was nothing of the kind. Forward seals the
// position before that closer runs, so the settle has already given up by then.
func TestBridgeDeath_ClosesTheTurnOnceAndTheParkedSettleDefers(t *testing.T) {
	h, cs, br := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	// A turn mid-stream: content folded, no bracket yet, which is what "dying
	// mid-prompt" looks like from the turn's side.
	startedTurnOn(t, h, cs, chatID, "half an answer")
	epoch, _ := h.coord.turns.openEpoch(chatID)
	sb, _ := h.bridge.mgr.orInsert(chatID)
	sb.bridge = br

	forwardDone := make(chan struct{})
	go func() {
		defer close(forwardDone)
		h.coord.Forward(chatID, br)
	}()
	settled := make(chan struct{})
	go func() {
		defer close(settled)
		// A position the dead bridge will never deliver.
		h.SettleTurnOnResponse(ctx, chatID, epoch, 99,
			&vibekit.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})})
	}()
	waitForParkedSettle(t, h.coord.turns, chatID, epoch, 99)

	// Stop WITHOUT removing the bridge: the process died on its own.
	br.Stop()
	<-forwardDone
	<-settled

	if got := turnEndedStops(t, h); !slices.Equal(got, []string{string(vibekit.StopReasonInterrupted)}) {
		t.Errorf("turn_ended stops = %v, want exactly one interrupted: the parked settle "+
			"either beat the death closer or announced a second end", got)
	}
	result, err := h.AwaitTurn(ctx, chatID, epoch)
	if err != nil {
		t.Fatalf("AwaitTurn: %v", err)
	}
	if result.Interrupt == "" {
		t.Error("the turn records no interrupt cause, so nothing durable says the agent process went away")
	}
	if result.WireEnded {
		t.Error("a turn no bracket closed reports WireEnded true, which arms the empty-turn recovery on a dead bridge")
	}
}

// The wait does NOT stop at the awaited turn's own close, and this is what keeps
// the empty-turn gate's structural clause sound.
//
// The gate re-prompts only when no LATER turn opened on the chat, read live — and
// that read is only meaningful once the folder has consumed everything delivered
// before the response. A wait that returned as soon as the awaited turn finalized
// (the ordinary case, and by construction the mis-bind case) left that clause
// racing the folder over frames already in the queue: the prompt goroutine's path
// is three mutex acquisitions and the folder's is a JSON decode plus a persist, so
// the gate could read false and re-send a prompt that had already been answered.
func TestAwaitPosition_DoesNotStopAtTheAwaitedTurnsOwnClose(t *testing.T) {
	r := newTurnRegistry()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	gen := r.attachForward(chatID)

	turn := r.open(ctx, chatID, vibekit.TurnSourcePrompt, "", 0)
	if turn == nil {
		t.Fatal("open returned no turn")
	}
	// Close it the way the wire's own bracket does, while the caller still holds its
	// handle — so the record is retained and its done channel is closed.
	claimed, won := r.claimOpen(ctx, chatID)
	if !won {
		t.Fatal("claimOpen lost the claim on a freshly opened turn")
	}
	r.finish(claimed, vibekit.TurnResult{Stop: vibekit.StopReasonEndTurn})

	reached := make(chan bool, 1)
	go func() { reached <- r.awaitPosition(ctx, chatID, turn.Epoch, 5) }()

	select {
	case got := <-reached:
		t.Fatalf("awaitPosition returned %v with the folder still at position 0: the settle "+
			"stopped at the turn's own close, so the recovery decision races the folder", got)
	case <-time.After(50 * time.Millisecond):
	}

	r.observe(chatID, gen, 5)
	if got := <-reached; !got {
		t.Error("awaitPosition reported the position unreachable after the folder reached it")
	}
}

// The settle returns only once the folder has caught up, so a LATER turn queued
// behind the response is already visible to the empty-turn gate.
//
// The mis-bind chain end to end: a zero-content auto-wake binds the pre-open and
// its bracket closes it with end_turn and EmittedNothing — the gate's first three
// clauses, satisfied by a turn that was never the prompt's. What must save the
// prompt is the fourth, and it can only answer honestly once the prompted turn's
// own turn_start has been folded.
func TestSettle_ReturnsOnlyAfterTheFolderHasCaughtUp(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	gen := h.coord.turns.attachForward(chatID)
	preOpen := h.StartTurn(ctx, chatID, vibekit.TurnSourcePrompt)
	defer h.ReleaseTurn(chatID, preOpen)

	// The auto-wake's bracket pair mis-binds and closes the pre-open; the prompted
	// turn's own bracket is still queued behind the response.
	h.coord.consumeFrame(chatID, gen, vibekit.Notification{Msg: newTurnStartMsg(), Seq: 1})
	h.coord.consumeFrame(chatID, gen, vibekit.Notification{Msg: newTurnEndMsg("end_turn"), Seq: 2})

	var openedAfterAtSettle bool
	settled := make(chan struct{})
	go func() {
		defer close(settled)
		h.SettleTurnOnResponse(ctx, chatID, preOpen, 4,
			&vibekit.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})})
		openedAfterAtSettle = h.coord.TurnOpenedAfter(chatID, preOpen)
	}()
	waitForParkedSettle(t, h.coord.turns, chatID, preOpen, 4)

	h.coord.consumeFrame(chatID, gen, vibekit.Notification{Msg: newTurnStartMsg(), Seq: 3})
	h.coord.consumeFrame(chatID, gen, vibekit.Notification{Msg: newTurnEndMsg("end_turn"), Seq: 4})
	<-settled

	if !openedAfterAtSettle {
		t.Error("the settle returned before the folder consumed the prompted turn's bracket, so " +
			"the empty-turn gate read no-later-turn and would re-send an answered prompt")
	}
	result, err := h.AwaitTurn(ctx, chatID, preOpen)
	if err != nil {
		t.Fatalf("AwaitTurn: %v", err)
	}
	if !result.WireEnded || !result.EmittedNothing {
		t.Errorf("the mis-bound pre-open = {wire:%v empty:%v}, want both true — the fixture is not "+
			"reproducing the chain the fourth clause exists for", result.WireEnded, result.EmittedNothing)
	}
}
