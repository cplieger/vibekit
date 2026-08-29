package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

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
	h.coord.OpenTurn(t.Context(), chatID, vibekit.TurnSourcePrompt)
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

// TestForwardExit_DeliberateCloseIsNotADeath is the discriminator the third actor
// rests on.
//
// Every teardown vibekit performs itself removes the bridge from the map first
// and has its own closer: the model-switch fallback, the empty-turn recovery's
// session refresh, the shutdown drain. Reading a deliberate stop as a death would
// close the turn those paths are still driving — the empty-turn retry is the case
// that bites, because it closes the bridge mid-turn on purpose and then answers
// the same turn.
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

	if got := turnEndedStops(t, h); !slices.Equal(got, []string{string(vibekit.StopReasonInterrupted)}) {
		t.Errorf("turn_ended stops = %v, want exactly one interrupted", got)
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

// TestFinalizeLocalShellTurn_AnnouncesTheEnd: a `!cmd` turn is a turn, so its end
// goes through the finalizer rather than being a broadcast the shell handler
// writes itself. That is what stops a second closer producing another one.
func TestFinalizeLocalShellTurn_AnnouncesTheEnd(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	epoch := h.coord.OpenTurn(t.Context(), "c1", vibekit.TurnSourceLocalShell)

	h.coord.FinalizeLocalShellTurn(t.Context(), "c1", epoch)

	if got := turnEndedStops(t, h); !slices.Equal(got, []string{string(vibekit.StopReasonEndTurn)}) {
		t.Errorf("turn_ended stops = %v, want exactly one end_turn", got)
	}
}

// TestOpenTurn_LocalShellRecordsNoModel is the stated source rule: no model
// answered a shell command, so a footer claiming one would be a lie about who
// produced the output.
func TestOpenTurn_LocalShellRecordsNoModel(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "sonnet-4"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	h.coord.OpenTurn(t.Context(), "c1", vibekit.TurnSourceLocalShell)

	turn, won := h.coord.turns.claimOpen(t.Context(), "c1")
	if !won {
		t.Fatal("no turn was opened")
	}
	if turn.Model != "" {
		t.Errorf("a local shell turn recorded model %q", turn.Model)
	}
}

// TestOpenTurn_CapturesTheCreditBaseline is what gives a turn its own spend. The
// baseline used to be a local in the prompt handler, so a turn vibekit did not
// prompt had none at all and its credits were attributed to whatever turn came
// next.
func TestOpenTurn_CapturesTheCreditBaseline(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Usage.Credits = 4.5
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	h.coord.OpenTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)

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
	epoch := h.coord.OpenTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)
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
	epoch := h.coord.OpenTurn(t.Context(), "c1", vibekit.TurnSourceLocalShell)

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
	epoch := h.coord.OpenTurn(t.Context(), "c1", vibekit.TurnSourceLocalShell)
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
// Zero was overloaded: it meant "take whatever is open" AND it is what OpenTurn
// returns when it refuses, which is reachable — a user cancel landing while a
// prompt's OpenTurn is parked on the previous turn's persistence. The prompt Call
// then fails on the dead context and the failure path ran with epoch zero, so
// AbandonInFlightTurn claimed the chat's open turn: an agent-initiated one, whose
// partial it persisted under an interrupt outcome carrying ANOTHER turn's failure
// reason, with an interrupted divider to match. "Close whatever is open" now has
// its own spelling.
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
// flight when the claim landed cannot race them.
//
// turnFinalizing excludes the NEXT fold, not the one already past TurnFoldTarget,
// and the position advance runs as consumeFrame's defer — so the settle wakes at the
// instant frame N finishes and the folder is free to begin N+1 in parallel with the
// claim. Reading eight exported fields one at a time then races a strings.Builder
// and three slices, which by the memory model can persist a torn Content as the
// turn's final text. Run with -race; that is the point of it.
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
