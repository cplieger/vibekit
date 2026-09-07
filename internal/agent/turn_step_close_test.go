package agent

// What a workflow step's turn persists and announces. The RUN finishing is the closer
// that exists for it (the attribution gate drops a step's own turn_end), but a bridge
// death and the chat's bracket claim the same turn, so all three key on its SOURCE.

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// stagedStepTurn opens a step turn through the real fold target rather than setting
// the source by hand: openWire is what stamps TurnSourceWorkflowStep, and a test
// asserting the source it just assigned would pin nothing.
func stagedStepTurn(t *testing.T, h *Runtime, cs *fakeChatStore, chatID vibekit.ChatID, text string) {
	t.Helper()
	if err := cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	buf := h.coord.TurnFoldTarget(t.Context(), chatID, vibekit.TurnSourceWorkflowStep)
	if text == "" {
		return
	}
	buf.Started = true
	buf.MessageID = newMessageID()
	buf.Content.WriteString(text)
}

// outcomeMarkerCount is a COUNT rather than a last-one lookup: the ruling is that a
// step turn persists NO marker, so the idempotency case needs the number unchanged.
func outcomeMarkerCount(t *testing.T, cs *fakeChatStore, chatID vibekit.ChatID) int {
	t.Helper()
	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		t.Fatalf("chat %q vanished", chatID)
	}
	n := 0
	for i := range c.Messages {
		if c.Messages[i].EventKind == vibekit.EventTurnOutcome {
			n++
		}
	}
	return n
}

// appendUserRow persists a prompt sent while the turn was still folding: the trailing
// row the client renders AFTER the reply it arrived during.
func appendUserRow(t *testing.T, cs *fakeChatStore, chatID vibekit.ChatID, text string) {
	t.Helper()
	if err := cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool {
		c.Messages = append(c.Messages, vibekit.Message{
			ID:      newMessageID(),
			Role:    vibekit.RoleUser,
			Content: text,
		})
		return true
	}); err != nil {
		t.Fatalf("append the user row: %v", err)
	}
}

// rowOrder is a READ rather than an assertion helper: three closers share the ordering
// rule and each case states its own expectation, so only the walk is worth having once.
func rowOrder(t *testing.T, cs *fakeChatStore, chatID vibekit.ChatID) (assistantAt, userAt int) {
	t.Helper()
	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		t.Fatalf("chat %q vanished", chatID)
	}
	assistantAt, userAt = -1, -1
	for i := range c.Messages {
		switch c.Messages[i].Role {
		case vibekit.RoleAssistant:
			assistantAt = i
		case vibekit.RoleUser:
			userAt = i
		case vibekit.RoleEvent:
		}
	}
	return assistantAt, userAt
}

// TestCloseStepTurn_PersistsAStepsContent is the defect: a step's frames open a turn on
// the launching chat whose own turn_end the attribution gate drops, so without a closer
// on the run's completion a reader who reloaded lost everything the steps produced.
func TestCloseStepTurn_PersistsAStepsContent(t *testing.T) {
	h, cs, _ := newTestHub()
	const reply = "the step got this far before its run ended"
	stagedStepTurn(t, h, cs, "c1", reply)

	h.coord.CloseStepTurn(t.Context(), "c1")

	msgs := assistantMessages(t, cs, "c1")
	if len(msgs) != 1 {
		t.Fatalf("persisted %d assistant messages, want exactly 1 — the step's content is the whole point", len(msgs))
	}
	if msgs[0].Content != reply {
		t.Errorf("persisted content = %q, want %q", msgs[0].Content, reply)
	}
	if msgs[0].TurnOutcome != vibekit.TurnOutcomeUnknown {
		t.Errorf("persisted outcome = %q, want unknown — the run ended, how the step's own turn ended never arrived",
			msgs[0].TurnOutcome)
	}
	if msgs[0].TurnFailureReason != stepRunEndedCause {
		t.Errorf("persisted reason = %q, want %q", msgs[0].TurnFailureReason, stepRunEndedCause)
	}
	if h.liveTurnBuffer("c1") != nil {
		t.Error("the step turn is still open, so the next frame extends a turn its run has finished")
	}
}

// TestCloseStepTurn_LeavesAPromptTurnAlone is why the read keys on the turn's SOURCE
// rather than on displaceableEngineTurn, which also matches an agent-initiated turn:
// closing a prompted turn takes a live reply away from the reader mid-answer.
func TestCloseStepTurn_LeavesAPromptTurnAlone(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "the chat's own reply, still streaming")

	h.coord.CloseStepTurn(t.Context(), "c1")

	if got := assistantMessages(t, cs, "c1"); len(got) != 0 {
		t.Errorf("persisted %d assistant messages for a prompt turn, want 0", len(got))
	}
	if got := outcomeMarkerCount(t, cs, "c1"); got != 0 {
		t.Errorf("persisted %d outcome markers for a prompt turn, want 0", got)
	}
	if h.liveTurnBuffer("c1") == nil {
		t.Error("the chat's own prompt turn was closed by a run finishing")
	}
}

// TestCloseStepTurn_AnEmptyStepTurnPersistsAndAnnouncesNothing is THE RULING. Nothing
// folded means no message_created was broadcast and nothing entered the transcript, so
// a marker would close no divergence and would instead open a headless turn card in a
// conversation the turn is not about. The announcement goes with it, because every
// effect of turn_ended lands on the launching chat's OWN last turn.
func TestCloseStepTurn_AnEmptyStepTurnPersistsAndAnnouncesNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	stagedStepTurn(t, h, cs, "c1", "")

	h.coord.CloseStepTurn(t.Context(), "c1")

	if got := assistantMessages(t, cs, "c1"); len(got) != 0 {
		t.Errorf("persisted %d assistant messages for a step turn that emitted nothing, want 0", len(got))
	}
	if got := outcomeMarkerCount(t, cs, "c1"); got != 0 {
		t.Errorf("persisted %d outcome markers, want 0 — a marker is a turn card in a chat the turn is not about",
			got)
	}
	if got := turnEndedStops(t, h); len(got) != 0 {
		t.Errorf("announced turn_ended %v for a step turn that persisted nothing; the client would latch "+
			"a done dot on the launching chat's own last turn that no reload re-derives", got)
	}
}

// TestCloseStepTurn_InsertsAheadOfATrailingUserRow pins where the message LANDS: a
// prompt sent mid-fold persists its user row first and the client places it AFTER the
// reply, so a plain append makes the file and every client's array disagree on order.
func TestCloseStepTurn_InsertsAheadOfATrailingUserRow(t *testing.T) {
	h, cs, _ := newTestHub()
	stagedStepTurn(t, h, cs, "c1", "the step's reply")
	appendUserRow(t, cs, "c1", "a prompt sent while the run was still going")

	h.coord.CloseStepTurn(t.Context(), "c1")

	assistantAt, userAt := rowOrder(t, cs, "c1")
	if assistantAt < 0 || userAt < 0 {
		t.Fatalf("expected one assistant row and one user row, got assistant=%d user=%d", assistantAt, userAt)
	}
	if assistantAt > userAt {
		t.Errorf("the step's reply persisted at %d, AFTER the user row at %d — the file disagrees with every client's array",
			assistantAt, userAt)
	}
}

// TestCloseAsInterrupted_InsertsAStepsContentAheadOfATrailingUserRow is the same ordering
// rule reached by a BRIDGE DEATH, the closer a per-closer choice left out.
func TestCloseAsInterrupted_InsertsAStepsContentAheadOfATrailingUserRow(t *testing.T) {
	h, cs, _ := newTestHub()
	stagedStepTurn(t, h, cs, "c1", "the step got this far before the process exited")
	appendUserRow(t, cs, "c1", "a prompt sent while the run was still going")

	h.coord.closeTurnOnBridgeDeath(t.Context(), "c1")

	assistantAt, userAt := rowOrder(t, cs, "c1")
	if assistantAt < 0 || userAt < 0 {
		t.Fatalf("expected one assistant row and one user row, got assistant=%d user=%d", assistantAt, userAt)
	}
	if assistantAt > userAt {
		t.Errorf("the step's reply persisted at %d, AFTER the user row at %d — the file disagrees with every client's array",
			assistantAt, userAt)
	}
}

// TestCloseOnWireEnd_InsertsAStepsContentAheadOfATrailingUserRow is the third closer
// that can claim a step turn: the CHAT's own bracket, whose AnyOpen claim takes whatever
// is open. The trailing row means a prompt on disk whose StartTurn has not displaced it.
func TestCloseOnWireEnd_InsertsAStepsContentAheadOfATrailingUserRow(t *testing.T) {
	h, cs, _ := newTestHub()
	stagedStepTurn(t, h, cs, "c1", "the step's reply")
	appendUserRow(t, cs, "c1", "a prompt sent while the run was still going")

	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonEndTurn, "")

	assistantAt, userAt := rowOrder(t, cs, "c1")
	if assistantAt < 0 || userAt < 0 {
		t.Fatalf("expected one assistant row and one user row, got assistant=%d user=%d", assistantAt, userAt)
	}
	if assistantAt > userAt {
		t.Errorf("the step's reply persisted at %d, AFTER the user row at %d — the file disagrees with every client's array",
			assistantAt, userAt)
	}
}

// TestCloseAsInterrupted_AnEmptyStepTurnPersistsAndAnnouncesNothing is the ruling on the
// closer the marker gate could not reach: a bridge death claims whatever is open, so an
// EventInterrupted row would give the launching chat a headless card with a BROKEN mark.
func TestCloseAsInterrupted_AnEmptyStepTurnPersistsAndAnnouncesNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	stagedStepTurn(t, h, cs, "c1", "")

	h.coord.closeTurnOnBridgeDeath(t.Context(), "c1")

	if got := assistantMessages(t, cs, "c1"); len(got) != 0 {
		t.Errorf("persisted %d assistant messages for a step turn that emitted nothing, want 0", len(got))
	}
	if got := eventMessageOf(t, cs, "c1", vibekit.EventInterrupted); got != nil {
		t.Errorf("persisted an interrupted divider (outcome %q); that row is a turn card in a chat "+
			"the turn is not about", got.TurnOutcome)
	}
	if got := outcomeMarkerCount(t, cs, "c1"); got != 0 {
		t.Errorf("persisted %d outcome markers, want 0", got)
	}
	if got := turnEndedStops(t, h); len(got) != 0 {
		t.Errorf("announced turn_ended %v; a close that persists nothing leaves the client nothing "+
			"to end, and every effect lands on the launching chat's own last turn", got)
	}
	if h.liveTurnBuffer("c1") != nil {
		t.Error("the step turn is still open, so the next frame extends a turn whose process is gone")
	}
}

// TestCloseStepTurn_AStepTurnWithContentStillAnnounces is the other side of the broadcast
// gate: a carrier exists, so the client's latch is right and survives a reload.
func TestCloseStepTurn_AStepTurnWithContentStillAnnounces(t *testing.T) {
	h, cs, _ := newTestHub()
	stagedStepTurn(t, h, cs, "c1", "the step got this far before its run ended")

	h.coord.CloseStepTurn(t.Context(), "c1")

	ends := payloadsOfType[vibekit.TurnEndedPayload](t, bufferedSince(h, 0), vibekit.EventTurnEnded)
	if len(ends) != 1 {
		t.Fatalf("broadcast %d turn_ended events, want exactly 1", len(ends))
	}
	if ends[0].Outcome != vibekit.TurnOutcomeUnknown {
		t.Errorf("turn_ended outcome = %q, want unknown — the run ended, how the step's own turn ended never arrived",
			ends[0].Outcome)
	}
}

// TestCloseOnWireEnd_AnEmptyStepTurnCancelledStillPersistsAndAnnounces is the population
// the gate must NOT swallow: a cancel persists its own row whatever the turn's source, so
// the gate reads what the close persisted rather than re-deriving it from the source.
func TestCloseOnWireEnd_AnEmptyStepTurnCancelledStillPersistsAndAnnounces(t *testing.T) {
	h, cs, _ := newTestHub()
	stagedStepTurn(t, h, cs, "c1", "")

	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonCancelled, "")

	cancelled := eventMessageOf(t, cs, "c1", vibekit.EventCancelled)
	if cancelled == nil {
		t.Fatal("a cancelled step turn persisted no EventCancelled row, so a reload derives nothing")
	}
	if cancelled.TurnOutcome != vibekit.TurnOutcomeCancelled {
		t.Errorf("the cancel row carries outcome %q, want cancelled", cancelled.TurnOutcome)
	}
	ends := payloadsOfType[vibekit.TurnEndedPayload](t, bufferedSince(h, 0), vibekit.EventTurnEnded)
	if len(ends) != 1 {
		t.Fatalf("broadcast %d turn_ended events, want exactly 1: this close persisted a carrier", len(ends))
	}
	if ends[0].StopReason != vibekit.StopReasonCancelled {
		t.Errorf("turn_ended stop reason = %q, want cancelled", ends[0].StopReason)
	}
}

// TestCloseStepTurn_FailsAnUnsettledToolCall is the spinner rule: the run's terminal
// transition ends the only thing that could still send a tool_call_update, so a card
// persisted `in_progress` renders as a permanent spinner on every later reload.
func TestCloseStepTurn_FailsAnUnsettledToolCall(t *testing.T) {
	h, cs, _ := newTestHub()
	stagedStepTurn(t, h, cs, "c1", "the step ran a command")
	buf := h.liveTurnBuffer("c1")
	if buf == nil {
		t.Fatal("the fixture opened no step turn")
	}
	buf.AppendToolCall(&vibekit.ToolCall{ID: "tc-1", Title: "Run command", Status: vibekit.ToolInProgress})

	h.coord.CloseStepTurn(t.Context(), "c1")

	// TWO guards, not one disjunction: the second reads msgs[0], so folding them makes
	// the Fatalf's own condition panic when nothing was persisted.
	msgs := assistantMessages(t, cs, "c1")
	if len(msgs) != 1 {
		t.Fatalf("persisted %d assistant messages, want exactly 1", len(msgs))
	}
	if len(msgs[0].ToolCalls) != 1 {
		t.Fatalf("the persisted message carries %d tool calls, want 1", len(msgs[0].ToolCalls))
	}
	if got := msgs[0].ToolCalls[0].Status; got != vibekit.ToolFailed {
		t.Errorf("persisted tool status = %q, want failed — nothing is left to settle it", got)
	}
}

// TestCloseStepTurn_IsIdempotent makes the call safe at a site that cannot know whether
// an earlier frame already closed the turn: the claim is first-wins.
func TestCloseStepTurn_IsIdempotent(t *testing.T) {
	h, cs, _ := newTestHub()
	stagedStepTurn(t, h, cs, "c1", "the step's reply")

	h.coord.CloseStepTurn(t.Context(), "c1")
	firstMsgs := len(assistantMessages(t, cs, "c1"))
	firstMarkers := outcomeMarkerCount(t, cs, "c1")

	h.coord.CloseStepTurn(t.Context(), "c1")

	if got := len(assistantMessages(t, cs, "c1")); got != firstMsgs {
		t.Errorf("assistant messages went %d -> %d over a second close, want unchanged", firstMsgs, got)
	}
	if got := outcomeMarkerCount(t, cs, "c1"); got != firstMarkers {
		t.Errorf("outcome markers went %d -> %d over a second close, want unchanged", firstMarkers, got)
	}
}

// TestCloseStepTurn_MintsNoLifecycle is why the read goes through `lookup`: asked once
// per terminal RUN frame, and an entry leaves the map only through forget, so a minted
// one and its `changed` channel would outlive every chat that never had a turn.
func TestCloseStepTurn_MintsNoLifecycle(t *testing.T) {
	h, _, _ := newTestHub()

	h.coord.CloseStepTurn(t.Context(), "c-none")

	if _, ok := h.coord.turns.lookup("c-none"); ok {
		t.Error("asking about a chat with no turn minted a lifecycle for it")
	}
}
