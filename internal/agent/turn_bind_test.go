package agent

// The five open sources, provisional acknowledgement, and the wire's brackets.

import (
	"slices"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// A wire turn_start binds to the pending pre-open rather than closing it and
// opening a turn of its own.
//
// Without the binding, every prompt would produce TWO turns: the pre-open the
// prompt is holding, closed `unknown` by its own bracket, and a wireTurnStart turn
// the prompt has no handle on — so the prompt's outcome would land on a turn
// nothing was waiting for.
func TestWireTurnStart_BindsThePendingPreOpen(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	epoch := h.OpenTurn(ctx, chatID, vibekit.TurnSourcePrompt)
	defer h.ReleaseTurn(chatID, epoch)

	h.translateACPEvent(chatID, newTurnStartMsg())

	if h.coord.TurnOpenedAfter(chatID, epoch) {
		t.Error("the bracket opened a SECOND turn instead of binding to the pre-open")
	}
	if open, _ := h.coord.turns.openEpoch(chatID); open != epoch {
		t.Errorf("open epoch = %d, want the pre-open %d", open, epoch)
	}
	if got := turnEndedStops(t, h); len(got) != 0 {
		t.Errorf("the bracket announced an end: %v", got)
	}
}

// A turn_start arriving with a turn already open and NOTHING pending means the
// previous turn's end never came. It is closed `unknown` and a wireTurnStart turn
// opens in its place — the agent-initiated class, and it needs no timer.
func TestWireTurnStart_ClosesATurnWhoseEndNeverArrived(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	startedTurnOn(t, h, cs, chatID, "the first turn's reply")
	first, _ := h.coord.turns.openEpoch(chatID)
	// Acknowledge it, so the next bracket has nothing pending to bind.
	h.translateACPEvent(chatID, newTurnStartMsg())

	h.translateACPEvent(chatID, newTurnStartMsg())

	if got := turnEndedStops(t, h); !slices.Equal(got, []string{string(vibekit.StopReasonUnknown)}) {
		t.Errorf("turn_ended stops = %v, want exactly one %q", got, vibekit.StopReasonUnknown)
	}
	second, open := h.coord.turns.openEpoch(chatID)
	if !open {
		t.Fatal("no turn is open after the second bracket, so the agent's turn has no record")
	}
	if second == first {
		t.Errorf("the second bracket reused epoch %d instead of opening its own turn", first)
	}
	c, _ := cs.Get(ctx, chatID)
	if !hasAssistantContent(c, "the first turn's reply") {
		t.Errorf("the displaced turn's reply was not persisted; messages = %+v", c.Messages)
	}
}

// A frame carrying agentInitiated revises a provisional binding: the started turn
// was the AGENT's, and the pre-open is owed its own bracket after all.
//
// This is the only discriminator on the wire — the flag rides content frames and
// never the bracket — so what the test pins is the handover: the agent's turn
// keeps the buffer the frames were already folded into, and the pre-open comes
// back with a fresh one.
func TestReviseTurnBinding_HandsTheBufferToTheAgentsTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	preOpen := h.OpenTurn(ctx, chatID, vibekit.TurnSourcePrompt)
	defer h.ReleaseTurn(chatID, preOpen)
	h.translateACPEvent(chatID, newTurnStartMsg())

	h.translateACPEvent(chatID, newAgentInitiatedChunkMsg("the agent woke itself"))

	agentTurn, open := h.coord.turns.openEpoch(chatID)
	if !open {
		t.Fatal("no turn is open after the revision")
	}
	if agentTurn == preOpen {
		t.Fatal("the agent's content is still folding into the pre-open, so the binding was never revised")
	}
	if agentTurn <= preOpen {
		t.Errorf("the agent's turn is epoch %d, want a LATER epoch than the pre-open's %d: "+
			"the empty-turn gate's structural clause reads exactly that ordering", agentTurn, preOpen)
	}
	if buf := h.liveTurnBuffer(chatID); buf == nil || buf.Content.String() != "the agent woke itself" {
		t.Error("the agent's turn does not hold the content that was folded before the revision")
	}
	// The pre-open is pending again, with nothing folded into it.
	if pending, ok := pendingEpoch(h, chatID); !ok || pending != preOpen {
		t.Errorf("pending epoch = (%d, %v), want the pre-open %d back in the set: "+
			"its own bracket has nothing to bind to otherwise", pending, ok, preOpen)
	}
}

// pendingEpoch is the epoch of the chat's unacknowledged pre-open, read the way
// the lifecycle holds it rather than by consuming it: bindPending RETIRES what it
// binds, so a test that called it to ask the question would answer it and change
// it in one step.
func pendingEpoch(h *Runtime, chatID vibekit.ChatID) (vibekit.TurnEpoch, bool) {
	lc := h.coord.turns.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.pending == nil {
		return 0, false
	}
	return lc.pending.Epoch, true
}

// A pre-open whose call fails LOCALLY is retired, so the next prompt's bracket
// binds to the next prompt's turn.
//
// Without retirement the dead pre-open stays the binding candidate forever: the
// next prompt's turn_start acknowledges a finished turn, that prompt's own turn
// never receives a bracket, and it closes on the prompt response's outcome — which
// can only ever be end_turn or cancelled, so a refusal or an error would persist
// as `completed`.
func TestPreOpen_IsRetiredWhenItFinalizes(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	failed := h.OpenTurn(ctx, chatID, vibekit.TurnSourcePrompt)
	h.AbandonInFlightTurn(ctx, chatID, failed, "the pipe died")
	h.ReleaseTurn(chatID, failed)

	if _, ok := pendingEpoch(h, chatID); ok {
		t.Fatal("a finalized pre-open is still the binding candidate, so the NEXT prompt's " +
			"bracket would acknowledge a dead turn")
	}

	// The next prompt gets its own bracket and the wire's own outcome.
	next := h.OpenTurn(ctx, chatID, vibekit.TurnSourcePrompt)
	defer h.ReleaseTurn(chatID, next)
	h.translateACPEvent(chatID, newTurnStartMsg())
	h.translateACPEvent(chatID, newChunkMsg("the second answer"))
	h.translateACPEvent(chatID, newTurnEndMsg("refusal"))

	result, err := h.AwaitTurn(ctx, chatID, next)
	if err != nil {
		t.Fatalf("AwaitTurn: %v", err)
	}
	if !result.WireEnded || result.Stop != "refusal" {
		t.Errorf("the next prompt's turn = {wire:%v stop:%q}, want the wire's own refusal",
			result.WireEnded, result.Stop)
	}
}

// A turn_end for a chat with NO open turn persists nothing.
//
// Without the no-op, a cancel-grace expiry that closed its turn locally would meet
// the later wire bracket, and the fold-with-no-open-turn rule would manufacture a
// spurious empty persisted turn out of it — an extra turn in the index, with an
// outcome, that the user never took.
func TestWireTurnEnd_WithNoOpenTurnPersistsNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	h.translateACPEvent(chatID, newTurnEndMsg("end_turn"))

	c, _ := cs.Get(ctx, chatID)
	if len(c.Messages) != 0 {
		t.Errorf("messages = %+v, want none: a bracket for a turn nobody opened is a no-op", c.Messages)
	}
	if got := turnEndedStops(t, h); len(got) != 0 {
		t.Errorf("turn_ended stops = %v, want none", got)
	}
	if _, open := h.coord.turns.openEpoch(chatID); open {
		t.Error("the bracket opened a turn in order to close it")
	}
}

// A REPLAYED turn_end closes no live turn.
//
// KAS replays a whole transcript on session/load as ordinary session/update
// notifications, so without the per-frame replay gate a resume would close the
// turn that resume is happening during. This passed before the brackets were
// wired, because nothing consumed them; it has to keep passing now that something
// does.
func TestWireTurnEnd_ReplayedBracketClosesNoLiveTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	const chatID vibekit.ChatID = "c1"
	startedTurnOn(t, h, cs, chatID, "a live reply")
	epoch, _ := h.coord.turns.openEpoch(chatID)

	h.translateACPEvent(chatID, newReplayedTurnEndMsg("end_turn"))

	open, stillOpen := h.coord.turns.openEpoch(chatID)
	if !stillOpen || open != epoch {
		t.Errorf("open epoch = (%d, %v), want the live turn %d still open", open, stillOpen, epoch)
	}
	if got := turnEndedStops(t, h); len(got) != 0 {
		t.Errorf("turn_ended stops = %v, want none: stored history is not something happening now", got)
	}
}

// A fold with no open turn OPENS one, rather than lazily creating a bare buffer.
//
// The buffer used to be created on the first frame and keyed by chat, so a turn
// vibekit did not prompt had content and no record: nothing to end it, nothing to
// account for it, and its buffer left behind for the next turn's frames to extend.
func TestFold_WithNoOpenTurnOpensAWireTurn(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	h.translateACPEvent(chatID, newChunkMsg("nobody prompted this"))

	epoch, open := h.coord.turns.openEpoch(chatID)
	if !open {
		t.Fatal("a fold with no open turn opened no turn, so this content belongs to nothing")
	}
	lc := h.coord.turns.lifecycleFor(chatID)
	lc.mu.Lock()
	source := lc.cur.Source
	lc.mu.Unlock()
	if source != vibekit.TurnSourceWireTurnStart {
		t.Errorf("turn %d source = %v, want wireTurnStart", epoch, source)
	}
	if buf := h.liveTurnBuffer(chatID); buf == nil || buf.Content.String() != "nobody prompted this" {
		t.Error("the fold did not land in the turn's own buffer")
	}
}

// A local shell turn REFUSES while another turn is open: a shell turn cannot begin
// during an agent turn.
//
// The prompt-slot guard catches the turns vibekit itself prompted; this catches the
// ones it did not, which is the class with no slot to hold. Running anyway would
// fold the command's output into the agent's live turn.
func TestOpenTurn_LocalShellRefusesWhileATurnIsOpen(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	// A turn the ENGINE started, so no prompt slot is held.
	h.translateACPEvent(chatID, newTurnStartMsg())

	if epoch := h.OpenTurn(ctx, chatID, vibekit.TurnSourceLocalShell); epoch != 0 {
		t.Errorf("OpenTurn(localShell) = %d, want 0 (refused) while an agent turn is open", epoch)
	}
	// And it is allowed once that turn ends.
	h.translateACPEvent(chatID, newTurnEndMsg("end_turn"))
	epoch := h.OpenTurn(ctx, chatID, vibekit.TurnSourceLocalShell)
	if epoch == 0 {
		t.Error("OpenTurn(localShell) still refuses on an idle chat")
	}
	h.ReleaseTurn(chatID, epoch)
}

// PrimeIfNeeded awaits its own epoch before returning, which is what keeps the
// unacknowledged set from ever holding two.
//
// A prime that returned with its turn still open would leave the caller's own
// pre-open as a SECOND pending candidate, and a wire turn_start would then bind to
// whichever the registry happened to be holding.
func TestPrimeIfNeeded_ReturnsWithItsOwnTurnFinalized(t *testing.T) {
	h, cs, br := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = append(c.Messages, vibekit.Message{ID: "m1", Role: vibekit.RoleUser, Content: "earlier"})
		return true
	})
	sb, _ := h.bridge.mgr.orInsert(chatID)
	sb.bridge = br
	sb.primeReason = primeReasonReload

	h.coord.PrimeIfNeeded(ctx, chatID)

	if _, open := h.coord.turns.openEpoch(chatID); open {
		t.Error("PrimeIfNeeded returned with its turn still open, so the next prompt's pre-open " +
			"would be a second unacknowledged candidate")
	}
	if _, ok := pendingEpoch(h, chatID); ok {
		t.Error("the prime's turn is still in the pending set after it finalized")
	}
}

// After a revised binding the pre-open receives its OWN bracket through the wire,
// becomes the folding turn again, and its reply is attributed to it.
//
// This is the whole scenario the design's verification list asks for ("the pre-open
// still gets its own bracket") and it was delivered in name only: reclassify hands
// cur to the agent's turn, the agent's turn_end then leaves the chat idle with the
// pre-open referenced by pending alone, and a bind that only cleared pending made
// that record reachable by NOTHING. The next fold minted a THIRD turn that owned the
// prompt's whole reply, AwaitTurn answered ErrNoSuchTurn to the handle holder, and
// on reload the two replies were attributed to each other's turns.
func TestReviseTurnBinding_ThePreOpenStillReceivesItsOwnBracket(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	preOpen := h.OpenTurn(ctx, chatID, vibekit.TurnSourcePrompt)
	defer h.ReleaseTurn(chatID, preOpen)

	// The agent's own turn arrives first, mis-binds, is revised by its content, and
	// then ends — all through the wire, which is the part no test crossed.
	h.translateACPEvent(chatID, newTurnStartMsg())
	h.translateACPEvent(chatID, newAgentInitiatedChunkMsg("the agent woke itself"))
	h.translateACPEvent(chatID, newTurnEndMsg("end_turn"))

	// Now the prompt's own bracket, and its reply.
	h.translateACPEvent(chatID, newTurnStartMsg())
	h.translateACPEvent(chatID, newChunkMsg("the answer to the prompt"))

	open, isOpen := h.coord.turns.openEpoch(chatID)
	if !isOpen || open != preOpen {
		t.Fatalf("open epoch = (%d, %v), want the pre-open %d: its bracket bound a turn "+
			"nothing can reach, so the fold opened a third one", open, isOpen, preOpen)
	}
	if buf := h.liveTurnBuffer(chatID); buf == nil || buf.Content.String() != "the answer to the prompt" {
		t.Error("the prompt's reply did not fold into the turn the prompt is holding")
	}

	h.translateACPEvent(chatID, newTurnEndMsg("end_turn"))

	result, err := h.AwaitTurn(ctx, chatID, preOpen)
	if err != nil {
		t.Fatalf("AwaitTurn on the epoch this caller opened: %v — P1 says a handle holder "+
			"can never be told its own turn does not exist", err)
	}
	if !result.WireEnded {
		t.Error("the pre-open closed without a wire bracket, so its own turn_end reached something else")
	}
	c, _ := cs.Get(ctx, chatID)
	if !hasAssistantContent(c, "the answer to the prompt") {
		t.Errorf("the prompt's reply is not persisted under its own turn; messages = %+v", c.Messages)
	}
	if !hasAssistantContent(c, "the agent woke itself") {
		t.Errorf("the agent's reply is not persisted; messages = %+v", c.Messages)
	}
}

// A prompt-shaped open finding a LIVE agent-initiated turn closes it first, rather
// than displacing it silently.
//
// The design asserted a prompt source cannot encounter an open turn because
// admission control refuses a second prompt. That premise is false: a wireTurnStart
// turn holds no prompt slot. Displacing it lost data outright — the turn is in no
// set a closer can claim, so it was never finalized and never persisted, while its
// content had already been streamed to every client and its tail folded into the
// user's turn.
func TestOpenTurn_PromptClosesALiveAgentTurnRatherThanDisplacingIt(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	const chatID vibekit.ChatID = "c1"
	_ = cs.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	// A turn the ENGINE started, streaming: no prompt slot, nothing pending.
	h.translateACPEvent(chatID, newTurnStartMsg())
	h.translateACPEvent(chatID, newChunkMsg("the auto-wake got this far"))
	agentTurn, _ := h.coord.turns.openEpoch(chatID)

	epoch := h.OpenTurn(ctx, chatID, vibekit.TurnSourcePrompt)
	defer h.ReleaseTurn(chatID, epoch)

	c, _ := cs.Get(ctx, chatID)
	if !hasAssistantContent(c, "the auto-wake got this far") {
		t.Errorf("the displaced turn's streamed content was not persisted, so it vanishes on "+
			"the next reload; messages = %+v", c.Messages)
	}
	if got := turnEndedStops(t, h); !slices.Equal(got, []string{string(vibekit.StopReasonUnknown)}) {
		t.Errorf("turn_ended stops = %v, want exactly one %q: the displaced turn's end was "+
			"never announced", got, vibekit.StopReasonUnknown)
	}
	if open, _ := h.coord.turns.openEpoch(chatID); open != epoch {
		t.Errorf("open epoch = %d, want the prompt's %d (the agent's was %d)", open, epoch, agentTurn)
	}
	if buf := h.liveTurnBuffer(chatID); buf == nil || buf.Content.String() != "" {
		t.Error("the prompt's turn did not get a fresh buffer, so the agent's tail folds into it")
	}
}
