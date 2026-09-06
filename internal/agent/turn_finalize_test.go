package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// turnEndedStops returns the stop reason of every turn_ended event broadcast so
// far, in order. Counting them is the point: a turn announced twice is two cards
// in the transcript for one turn.
func turnEndedStops(t *testing.T, h *Runtime) []string {
	t.Helper()
	var out []string
	for _, e := range bufferedSince(h, 0) {
		var msg struct {
			Type    vibekit.EventType `json:"type"`
			Payload struct {
				StopReason string `json:"stop_reason"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &msg); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if msg.Type == vibekit.EventTurnEnded {
			out = append(out, msg.Payload.StopReason)
		}
	}
	return out
}

// hubOnDisk builds a runtime over the REAL chat store, seeded with chatID.
//
// The recording fake ignores its context entirely, so a durability assertion
// against it holds whether or not the write is detached. chat.Store.Mutate's
// entry guard is the mechanism the durable-context tests are about, and only the
// real store has it.
func hubOnDisk(t *testing.T, chatID vibekit.ChatID) (*Runtime, *chat.Store) {
	t.Helper()
	cs, err := chat.NewStore(t.TempDir())
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
	return h, cs
}

// deadContext is a context already cancelled, which is what BOTH doors into
// finalizeTurn are handed at shutdown: the prompt door's cancel is AfterFunc'd
// onto the shutdown context, and the bridge-death door is passed that context
// itself.
func deadContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

// startedTurnOn stages a chat with a streaming turn open, the way a live prompt
// leaves it: a record on the registry and a buffer with content in it.
func startedTurnOn(t *testing.T, h *Runtime, cs *fakeChatStore, chatID vibekit.ChatID, text string) {
	t.Helper()
	if err := cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	h.coord.StartTurn(t.Context(), chatID, vibekit.TurnSourcePrompt)
	buf := h.stageTurnBuffer(t, chatID)
	buf.Started = true
	buf.MessageID = newMessageID()
	buf.Content.WriteString(text)
}

// TestCloseTurnOnBridgeDeath_ClosesAnOpenTurn is the third actor. A prompt whose
// bridge dies has a settle that may never arrive, so nothing else is going to
// close the turn: the partial would stay in the buffer, the next turn's
// ensureTurnStarted would extend it under its message id, and the client would be
// left with `thinking` set on a chat whose process is gone.
func TestCloseTurnOnBridgeDeath_ClosesAnOpenTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	const partial = "the model got this far before the pipe died"
	startedTurnOn(t, h, cs, "c1", partial)

	h.coord.closeTurnOnBridgeDeath(t.Context(), "c1")

	c, ok := cs.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat record vanished")
	}
	var sawPartial bool
	for i := range c.Messages {
		if m := &c.Messages[i]; m.Role == vibekit.RoleAssistant && m.Content == partial {
			sawPartial = true
		}
	}
	if !sawPartial {
		t.Errorf("the partial was not persisted; the client showed it and a reload would lose it. messages=%d", len(c.Messages))
	}
	divider := dividerIn(c)
	if divider == nil {
		t.Fatal("no interrupted event; the transcript stops mid-reply with nothing saying why")
	}
	if divider.Content != deathInterruptCause {
		t.Errorf("divider content = %q, want %q", divider.Content, deathInterruptCause)
	}
	if got := turnEndedStops(t, h); !slices.Equal(got, []string{string(vibekit.StopReasonInterrupted)}) {
		t.Errorf("turn_ended stops = %v, want exactly one interrupted", got)
	}
}

// TestCloseTurnOnBridgeDeath_IgnoresAChatWithNoOpenTurn: a bridge culled while
// idle, or one whose turn a closer already finished, must announce nothing.
// Opening a turn in order to close it would report an interruption nothing was
// interrupted from, on every bridge exit the app performs.
func TestCloseTurnOnBridgeDeath_IgnoresAChatWithNoOpenTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	h.coord.closeTurnOnBridgeDeath(t.Context(), "c1")

	if got := turnEndedStops(t, h); len(got) != 0 {
		t.Errorf("turn_ended stops = %v, want none", got)
	}
	if c, _ := cs.Get(t.Context(), "c1"); len(c.Messages) != 0 {
		t.Errorf("messages = %+v, want none", c.Messages)
	}
}

// TestCloseTurnOnBridgeDeath_APromptTurnAppendsAfterItsOwnUserRow is the population
// the ordering choice must NOT reach, and the case that catches it being inverted.
//
// A prompt's own user row IS the tail, so walking back over trailing user rows would
// insert the reply BEFORE the prompt it answers and split the turn. The divider and
// the announcement stay too: this turn is the chat's own.
func TestCloseTurnOnBridgeDeath_APromptTurnAppendsAfterItsOwnUserRow(t *testing.T) {
	h, cs, _ := newTestHub()
	const partial = "the model got this far before the pipe died"
	startedTurnOn(t, h, cs, "c1", partial)
	appendUserRow(t, cs, "c1", "the prompt this reply answers")

	h.coord.closeTurnOnBridgeDeath(t.Context(), "c1")

	assistantAt, userAt := rowOrder(t, cs, "c1")
	if assistantAt < 0 || userAt < 0 {
		t.Fatalf("expected one assistant row and one user row, got assistant=%d user=%d", assistantAt, userAt)
	}
	if assistantAt < userAt {
		t.Errorf("the reply persisted at %d, BEFORE its own trigger row at %d — the turn is split and "+
			"projectTurns reads the reply as a headerless turn", assistantAt, userAt)
	}
	c, _ := cs.Get(t.Context(), "c1")
	if dividerIn(c) == nil {
		t.Error("no interrupted event; the transcript stops mid-reply with nothing saying why")
	}
	if got := turnEndedStops(t, h); !slices.Equal(got, []string{string(vibekit.StopReasonInterrupted)}) {
		t.Errorf("turn_ended stops = %v, want exactly one interrupted", got)
	}
}

// TestForwardExit_DeliberateCloseIsNotADeath is the discriminator the third actor
// rests on: every teardown vibekit performs removes the bridge from the map first
// and has its own closer, so reading a deliberate stop as a death closes a turn
// those paths are still driving — the empty-turn retry closes the bridge mid-turn
// on purpose and then answers the same turn.
func TestForwardExit_DeliberateCloseIsNotADeath(t *testing.T) {
	h, cs, br := newTestHub()
	startedTurnOn(t, h, cs, "c1", "half an answer")
	sb, _ := h.bridge.mgr.orInsert("c1")
	sb.bridge = br

	forwardDone := make(chan struct{})
	go func() {
		h.coord.Forward("c1", br)
		close(forwardDone)
	}()

	// CloseBridge removes then stops, which is what makes the exit deliberate.
	h.coord.CloseBridge("c1")
	select {
	case <-forwardDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Forward did not exit after the bridge was stopped")
	}

	if got := turnEndedStops(t, h); len(got) != 0 {
		t.Errorf("turn_ended stops = %v, want none: the turn is still the closing caller's", got)
	}
	if _, open := h.coord.turns.openEpoch("c1"); !open {
		t.Error("the turn was closed by a deliberate teardown")
	}
}

// TestForwardExit_UnexpectedDeathClosesTheTurn is the same seam from the other
// side: a bridge that dies while still registered is a death, because nobody
// removed it.
func TestForwardExit_UnexpectedDeathClosesTheTurn(t *testing.T) {
	h, cs, br := newTestHub()
	startedTurnOn(t, h, cs, "c1", "half an answer")
	sb, _ := h.bridge.mgr.orInsert("c1")
	sb.bridge = br

	forwardDone := make(chan struct{})
	go func() {
		h.coord.Forward("c1", br)
		close(forwardDone)
	}()

	// Stop WITHOUT removing: the process died on its own.
	br.Stop()
	select {
	case <-forwardDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Forward did not exit after the bridge died")
	}

	if got := turnEndedStops(t, h); !slices.Equal(got, []string{string(vibekit.StopReasonInterrupted)}) {
		t.Errorf("turn_ended stops = %v, want exactly one interrupted", got)
	}
}

// TestFinalizeTurn_OneEpochAnnouncesOnce pins the exclusion at the coordinator:
// two closers reaching one turn produce one turn_ended, so the transcript does
// not grow a second boundary for one turn.
func TestFinalizeTurn_OneEpochAnnouncesOnce(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "half an answer")

	h.coord.FlushInFlightTurnOnSwitch(t.Context(), "c1")
	h.coord.FlushInFlightTurnOnSwitch(t.Context(), "c1")

	if got := turnEndedStops(t, h); !slices.Equal(got, []string{string(vibekit.StopReasonCancelled)}) {
		t.Errorf("turn_ended stops = %v, want exactly one cancelled", got)
	}
}

// TestFlushInFlightTurnOnSwitch_DiscardsThePartial is the direction half, and it
// is why the switch closer is not the failure closer. A model switch discards the
// partial — the user asked for a different answer, so the abandoned one is moot —
// where a failed prompt keeps it.
func TestFlushInFlightTurnOnSwitch_DiscardsThePartial(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "half an answer")

	h.coord.FlushInFlightTurnOnSwitch(t.Context(), "c1")

	c, _ := cs.Get(t.Context(), "c1")
	for i := range c.Messages {
		if c.Messages[i].Role == vibekit.RoleAssistant {
			t.Errorf("a model switch persisted the abandoned partial %q", c.Messages[i].Content)
		}
	}
	if h.liveTurnBuffer("c1") != nil {
		t.Error("the buffer survived the switch; the next turn would extend this turn's blocks")
	}
}

// TestFlushInFlightTurnOnSwitch_ConcludesCancelledOnBothChannels is item 1, and it
// asserts BOTH channels because their disagreement was the defect.
//
// The switch closer used to conclude `interrupted`, which the severity table grades
// BROKEN — so live the tab dot went red for a switch the reader asked for. And it
// persisted nothing, so on reload the derivation found no carrier and answered
// `completed`, which hides the footer glyph. Two channels, two wrong answers, and
// neither was `stopped`, which is what a user-initiated discard is.
func TestFlushInFlightTurnOnSwitch_ConcludesCancelledOnBothChannels(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "half an answer")

	h.coord.FlushInFlightTurnOnSwitch(t.Context(), "c1")

	// The RELOAD channel: a carrier on disk, so the derivation reads a verdict
	// instead of falling back to its default.
	c, _ := cs.Get(t.Context(), "c1")
	var marker *vibekit.Message
	for i := range c.Messages {
		if c.Messages[i].TurnOutcome != "" {
			marker = &c.Messages[i]
		}
	}
	if marker == nil {
		t.Fatalf("a discarded turn persisted no carrier, so a reload reads it as completed "+
			"and suppresses its footer. messages=%+v", c.Messages)
	}
	if marker.Role != vibekit.RoleEvent || marker.TurnOutcome != vibekit.TurnOutcomeCancelled {
		t.Errorf("carrier = role %q outcome %q, want a RoleEvent carrying cancelled",
			marker.Role, marker.TurnOutcome)
	}
	if marker.EventKind != vibekit.EventCancelled {
		t.Errorf("carrier event kind = %q, want %q — any other kind renders a visible row "+
			"for a switch that produced nothing to show", marker.EventKind, vibekit.EventCancelled)
	}
	if marker.TurnFailureReason == "" {
		t.Error("the carrier records no reason, so the turn's notice has nothing to say")
	}
	for i := range c.Messages {
		if c.Messages[i].Role == vibekit.RoleAssistant {
			t.Errorf("the discarded partial was persisted: %+v", c.Messages[i])
		}
	}

	// The LIVE channel: the same verdict, so the dot the reader watches and the dot
	// they get back after a refresh agree.
	if got := turnEndedOutcomes(t, h); !slices.Equal(got, []vibekit.TurnOutcome{vibekit.TurnOutcomeCancelled}) {
		t.Errorf("broadcast outcomes = %v, want exactly [cancelled]", got)
	}
	// And the payload's OTHER verdict field, which is a second vocabulary rather
	// than a restatement: announceConclusion serves both the interrupt and the
	// discard closers, so it reads the stop off the conclusion instead of naming
	// one. Hardcoding `interrupted` there — what it did before this closer existed
	// — leaves the outcome above correct and puts the wrong stop on the wire.
	if got := turnEndedStops(t, h); !slices.Equal(got, []string{string(vibekit.StopReasonCancelled)}) {
		t.Errorf("broadcast stop reasons = %v, want exactly [cancelled]", got)
	}
}

// TestFlushInFlightTurnOnSwitch_AnIdleChatRecordsNothing is the other half of the
// discard rule: a switch with nothing in flight is invisible, so it must not leave
// a marker or announce an end for a turn the reader never saw start.
func TestFlushInFlightTurnOnSwitch_AnIdleChatRecordsNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	seedChat(t, cs, "c1")
	epoch := h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)
	t.Cleanup(func() { h.coord.ReleaseTurn("c1", epoch) })

	h.coord.FlushInFlightTurnOnSwitch(t.Context(), "c1")

	if c, _ := cs.Get(t.Context(), "c1"); len(c.Messages) != 0 {
		t.Errorf("an unstarted turn's discard persisted %+v, want nothing", c.Messages)
	}
	if got := turnEndedStops(t, h); len(got) != 0 {
		t.Errorf("turn_ended stops = %v, want none", got)
	}
}

// TestFinalizeLocalShellTurn_AnnouncesTheEnd: a `!cmd` turn is a turn, so its end
// goes through the finalizer rather than being a broadcast the shell handler
// writes itself. That is what stops a second closer producing another one.
func TestFinalizeLocalShellTurn_AnnouncesTheEnd(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	epoch := h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourceLocalShell)

	h.coord.FinalizeLocalShellTurn(t.Context(), "c1", epoch)

	if got := turnEndedStops(t, h); !slices.Equal(got, []string{string(vibekit.StopReasonEndTurn)}) {
		t.Errorf("turn_ended stops = %v, want exactly one end_turn", got)
	}
}

// TestStartTurn_LocalShellRecordsNoModel is the stated source rule: no model
// answered a shell command, so a footer claiming one would be a lie about who
// produced the output.
func TestStartTurn_LocalShellRecordsNoModel(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "sonnet-4"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourceLocalShell)

	turn, won := h.coord.turns.claimOpen(t.Context(), "c1")
	if !won {
		t.Fatal("no turn was opened")
	}
	if turn.Model != "" {
		t.Errorf("a local shell turn recorded model %q", turn.Model)
	}
}

// TestStartTurn_CapturesTheCreditBaseline is what gives a turn its own spend. The
// baseline used to be a local in the prompt handler, so a turn vibekit did not
// prompt had none at all and its credits were attributed to whatever turn came
// next.
func TestStartTurn_CapturesTheCreditBaseline(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Usage.Credits = 4.5
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)

	turn, won := h.coord.turns.claimOpen(t.Context(), "c1")
	if !won {
		t.Fatal("no turn was opened")
	}
	if turn.Credits != 4.5 {
		t.Errorf("credit baseline = %v, want 4.5", turn.Credits)
	}
	// The spend is the difference against it, so a turn that costs 0.5 reports 0.5
	// rather than the chat's running total.
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Usage.Credits = 5.0
		return true
	}); err != nil {
		t.Fatalf("spend: %v", err)
	}
	if got := h.coord.turnStatsFor(t.Context(), turn).CreditsDelta; got != 0.5 {
		t.Errorf("CreditsDelta = %v, want 0.5", got)
	}
}

// TestFinalizeTurn_MeasuresEmptinessAfterFlushingTheCarry is the flush-before-
// measure order, and the defect it closes is a user-visible one: the steering
// filter withholds any trailing text that could still grow into an
// acknowledgement marker, so a reply ending in `[` sits entirely in the carry
// when the turn settles. Measured before the flush, that turn reports having
// produced nothing, the empty-turn recovery recreates the session and re-prompts
// a question the agent had already answered.
func TestFinalizeTurn_MeasuresEmptinessAfterFlushingTheCarry(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	epoch := h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)
	// The turn's whole reply is still withheld: prose that merely looks like the
	// start of a marker, so the flush releases it rather than dropping it.
	const reply = "see the note in ["
	buf := h.stageTurnBuffer(t, "c1")
	buf.Started = true
	buf.MessageID = newMessageID()
	buf.SetSteerCarry(reply, "")

	h.coord.SettleTurnOnResponse(t.Context(), "c1", epoch, 0,
		&vibekit.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})})

	result, err := h.coord.AwaitTurn(t.Context(), "c1", epoch)
	if err != nil {
		t.Fatalf("AwaitTurn: %v", err)
	}
	if result.EmittedNothing {
		t.Error("a turn whose only text was an unflushed carry reported EmittedNothing; the session would be recreated and the prompt re-sent")
	}
	c, _ := cs.Get(t.Context(), "c1")
	var persisted string
	for i := range c.Messages {
		if c.Messages[i].Role == vibekit.RoleAssistant {
			persisted = c.Messages[i].Content
		}
	}
	if persisted != reply {
		t.Errorf("persisted assistant content = %q, want %q", persisted, reply)
	}
}

// TestAwaitTurn_HandleOutlivesTheFinalizeAndDropsOnRelease is the retention
// bound. The record is gone from the chat's lifecycle the moment the turn
// finalizes, so without a handle a caller awaiting its OWN epoch would be told
// the turn never existed; and with retention keyed on anything but the handle it
// could be evicted before that caller read it.
func TestAwaitTurn_HandleOutlivesTheFinalizeAndDropsOnRelease(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	epoch := h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourceLocalShell)

	h.coord.FinalizeLocalShellTurn(t.Context(), "c1", epoch)

	result, err := h.coord.AwaitTurn(t.Context(), "c1", epoch)
	if err != nil {
		t.Fatalf("AwaitTurn on a held handle: %v", err)
	}
	if result.Stop != vibekit.StopReasonEndTurn {
		t.Errorf("Stop = %q, want %q", result.Stop, vibekit.StopReasonEndTurn)
	}
	if result.Epoch != epoch {
		t.Errorf("result Epoch = %d, want %d", result.Epoch, epoch)
	}

	h.coord.ReleaseTurn("c1", epoch)
	if _, err := h.coord.AwaitTurn(t.Context(), "c1", epoch); !errors.Is(err, vibekit.ErrNoSuchTurn) {
		t.Errorf("after the last handle was released, AwaitTurn error = %v, want ErrNoSuchTurn", err)
	}
}

// TestAwaitTurn_UnknownEpochReportsNoSuchTurn: an epoch the chat never minted is
// the one case that answers ErrNoSuchTurn, which is what makes the sentinel
// meaningful for the caller that does hold a handle.
func TestAwaitTurn_UnknownEpochReportsNoSuchTurn(t *testing.T) {
	h, _, _ := newTestHub()
	if _, err := h.coord.AwaitTurn(t.Context(), "c1", 99); !errors.Is(err, vibekit.ErrNoSuchTurn) {
		t.Errorf("AwaitTurn on an unknown epoch = %v, want ErrNoSuchTurn", err)
	}
}

// TestAwaitTurn_DeadContextReturnsRatherThanParking: a waiter selects on its own
// context as well as on the handle, so a caller whose turn context died does not
// park until something finalizes. The finalize afterwards is there to show the
// abandoned wait left the chat's lifecycle usable.
func TestAwaitTurn_DeadContextReturnsRatherThanParking(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	epoch := h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourceLocalShell)
	defer h.coord.ReleaseTurn("c1", epoch)

	dead, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := h.coord.AwaitTurn(dead, "c1", epoch); !errors.Is(err, context.Canceled) {
		t.Errorf("AwaitTurn with a dead context = %v, want context.Canceled", err)
	}

	h.coord.FinalizeLocalShellTurn(t.Context(), "c1", epoch)
	if got := turnEndedStops(t, h); !slices.Equal(got, []string{string(vibekit.StopReasonEndTurn)}) {
		t.Errorf("turn_ended stops = %v, want exactly one end_turn", got)
	}
}

// An epoch-scoped closer handed a ZERO epoch closes nothing.
//
// Zero is also what StartTurn returns when it refuses, which is reachable, so
// overloading it as "take whatever is open" let a failed prompt claim an
// agent-initiated turn and persist its partial under an interrupt outcome
// carrying ANOTHER turn's failure reason. That meaning has its own spelling now.
func TestAbandonInFlightTurn_WithNoEpochClosesNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	// A turn the ENGINE started, streaming. It holds no prompt slot, so admission
	// control never refused the prompt whose open then failed.
	h.translateACPEvent(chatID, newTurnStartMsg())
	h.translateACPEvent(chatID, newChunkMsg("the agent's own reply"))
	agentTurn, _ := h.coord.turns.openEpoch(chatID)

	h.AbandonInFlightTurn(ctx, chatID, 0, "The turn was cancelled before the agent answered.")

	if open, isOpen := h.coord.turns.openEpoch(chatID); !isOpen || open != agentTurn {
		t.Errorf("open epoch = (%d, %v), want the agent's turn %d still open: a prompt failure "+
			"that never opened a turn closed one it does not own", open, isOpen, agentTurn)
	}
	if got := turnEndedStops(t, h); len(got) != 0 {
		t.Errorf("turn_ended stops = %v, want none", got)
	}
	c, _ := cs.Get(ctx, chatID)
	if len(c.Messages) != 0 {
		t.Errorf("messages = %+v, want none: the agent's partial was persisted under the "+
			"prompt's interrupt reason", c.Messages)
	}
}

// The closers read the buffer through ONE guarded snapshot, so a fold still in
// flight when the claim landed cannot race them: turnFinalizing excludes the NEXT
// fold, so the settle wakes as frame N finishes while the folder is free to begin
// N+1. Reading eight exported fields one at a time races a strings.Builder and
// three slices, and can persist a torn Content as the turn's final text. Run with
// -race; that is the point of it.
func TestCloseTurn_ConcurrentFoldDoesNotRaceTheContentSnapshot(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	epoch, buf := h.stagePromptTurn(t, chatID)
	buf.StartTurn(newMessageID())
	// One delta before the folder starts, so the persisted content is not a
	// scheduling outcome: the racing goroutine below is there for the detector.
	buf.AppendTextDelta("the first delta", "")

	stop := make(chan struct{})
	folding := make(chan struct{})
	running := make(chan struct{})
	go func() {
		defer close(folding)
		first := true
		for {
			select {
			case <-stop:
				return
			default:
			}
			buf.AppendTextDelta("x", "")
			buf.AppendToolCall(&vibekit.ToolCall{ID: "tc-1"})
			buf.TrackFileChanges([]vibekit.ToolDiff{{Path: "a.go", NewText: "line\n"}}, false)
			if first {
				close(running)
				first = false
			}
		}
	}()
	// The folder must be LOOPING before the closer starts, or the two never overlap
	// and the detector has nothing to see: a race the test cannot reach is a test
	// that cannot fail.
	<-running

	h.SettleTurnOnResponse(ctx, chatID, epoch, 0,
		&vibekit.RPCResponse{Result: mustJSON(t, map[string]any{"stopReason": "end_turn"})})

	close(stop)
	<-folding

	c, _ := cs.Get(ctx, chatID)
	if !hasAssistantContent(c, "the first delta") {
		t.Errorf("the closer persisted no content for a turn that streamed; messages = %+v", c.Messages)
	}
}

// TestFinalizeTurn_PersistsOnACancelledContext is the incident: a chat streamed
// for 46 minutes, the container restarted, and the transcript came back holding
// only the user message.
//
// The assistant message was already assembled in memory when the closer ran;
// chat.Store.Mutate refused it on its entry guard seeing the shutdown-cancelled
// context, and 81051 characters went with the refusal. The `interrupted` divider
// went the same way on EVERY shutdown, which reads to the client as completed.
func TestFinalizeTurn_PersistsOnACancelledContext(t *testing.T) {
	const chatID vibekit.ChatID = "c1"
	const partial = "the model got this far before the container restarted"
	h, cs := hubOnDisk(t, chatID)
	epoch, buf := h.stagePromptTurn(t, chatID)
	buf.StartTurn(newMessageID())
	buf.AppendTextDelta(partial, "")

	h.coord.finalizeTurn(deadContext(t), chatID, turnClose{Closer: closerBridgeDeath, Epoch: epoch})

	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		t.Fatalf("chat %q vanished", chatID)
	}
	if !hasAssistantContent(c, partial) {
		t.Errorf("the assembled partial was refused and is gone; messages = %+v", c.Messages)
	}
	if divider := dividerIn(c); divider == nil {
		t.Error("no interrupted divider, so the client reads this turn as completed")
	} else if divider.Content != deathInterruptCause {
		t.Errorf("divider content = %q, want %q", divider.Content, deathInterruptCause)
	}
}

// TestFinalizeTurn_TheNoPartialArmPersistsOnACancelledContext covers the other
// interrupted arm, which a SPLIT turn always takes because SplitSegment clears
// Started. There the divider is the turn's only carrier, so a refusal loses the
// outcome, the changed files and the model in one write.
func TestFinalizeTurn_TheNoPartialArmPersistsOnACancelledContext(t *testing.T) {
	const chatID vibekit.ChatID = "c1"
	h, cs := hubOnDisk(t, chatID)
	epoch, buf := h.stagePromptTurn(t, chatID)
	buf.StartTurn(newMessageID())
	buf.AppendTextDelta("everything before the compaction point", "")
	buf.SetModel("m-latched")
	buf.TrackFileChanges([]vibekit.ToolDiff{{Path: "a.go", OldText: "x\n", NewText: "x\ny\n"}}, false)
	if !h.coord.SealTurnSegment(t.Context(), chatID) {
		t.Fatal("the fixture could not seal a segment, so the no-partial arm is unreachable")
	}

	h.coord.finalizeTurn(deadContext(t), chatID, turnClose{Closer: closerBridgeDeath, Epoch: epoch})

	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		t.Fatalf("chat %q vanished", chatID)
	}
	divider := dividerIn(c)
	if divider == nil {
		t.Fatal("a split turn's divider was refused, so nothing carries its outcome")
	}
	if divider.TurnModel != "m-latched" {
		t.Errorf("divider TurnModel = %q, want the buffer's latched %q", divider.TurnModel, "m-latched")
	}
	if divider.ChangedFiles["a.go"] == nil {
		t.Errorf("divider ChangedFiles = %v, want the turn's cumulative map", divider.ChangedFiles)
	}
}

// TestFinalizeTurn_DoesNotDetachThePositionWait is the placement guard: the
// detach sits BELOW the position wait rather than at the top of finalizeTurn.
//
// awaitPosition exits on the folder reaching the position, the folder going away,
// or ctx.Done() — and a wedged kiro-cli that never closes its pipe leaves the
// third as the only one that fires. A detach above it trades a lost turn for a
// hung shutdown, so this asserts on ELAPSED TIME: moved up, it hangs, not fails.
func TestFinalizeTurn_DoesNotDetachThePositionWait(t *testing.T) {
	const chatID vibekit.ChatID = "c1"
	h, cs := hubOnDisk(t, chatID)
	epoch, buf := h.stagePromptTurn(t, chatID)
	buf.StartTurn(newMessageID())
	buf.AppendTextDelta("half an answer", "")

	// A position the folder will never reach: nothing is attached to advance it.
	const unreachable = 99
	settled := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(settled)
		h.coord.finalizeTurn(deadContext(t), chatID, turnClose{
			Closer: closerPromptResponse,
			Epoch:  epoch,
			Seq:    unreachable,
			Resp:   &vibekit.RPCResponse{Result: json.RawMessage(`{"stopReason":"end_turn"}`)},
		})
	}()
	select {
	case <-settled:
	case <-time.After(5 * time.Second):
		t.Fatal("finalizeTurn parked on a position the folder cannot reach; the detach is above awaitPosition")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("finalizeTurn took %v to abandon an unreachable position, want a prompt return", elapsed)
	}

	// And it claimed nothing: the position could not be reached, so the
	// bridge-death closer owns whatever is still open.
	if open, isOpen := h.coord.turns.openEpoch(chatID); !isOpen || open != epoch {
		t.Errorf("open epoch = (%d, %t), want turn %d still open", open, isOpen, epoch)
	}
	if c, _ := cs.Get(t.Context(), chatID); len(c.Messages) != 0 {
		t.Errorf("an abandoned settle persisted %+v, want nothing", c.Messages)
	}
}
