package agent

// What a workflow step's turn persists and announces, whichever closer reaches it.
// The RUN finishing is the closer that exists for it — a step's own turn_end is
// dropped by the attribution gate, so without it the content stays in memory for the
// life of the process and the Turn record never closes — but a bridge death and the
// chat's own bracket claim the same turn, and both rulings are keyed on the turn's
// SOURCE so all three agree.

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// stagedStepTurn opens a turn the way a chat-parented run's step frames open one
// — through the real fold target with the step SOURCE — and stages `text` on it.
//
// It goes through TurnFoldTarget rather than setting the source by hand, so the
// turn under test is the one production produces: openWire is what stamps
// TurnSourceWorkflowStep, and a test asserting the source it just assigned would
// pin nothing.
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

// outcomeMarkerCount counts the chat's persisted turn-outcome marker rows.
//
// A COUNT rather than turn_segment_test.go's outcomeMarker, which answers with the
// last one: the ruling here is that a step turn persists NO marker, and the
// idempotency case needs the number to be unchanged rather than merely non-nil.
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

// appendUserRow persists a prompt sent while the turn was still folding — the
// trailing row the ordering rule is about, and the one the client renders AFTER the
// reply it arrived during.
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

// rowOrder reports where the chat's assistant row and its trailing user row landed,
// -1 for either that is absent.
//
// A READ rather than an assertion helper: three closers now share the ordering rule
// and each case states its own expectation, so the walk is the only thing worth
// having once.
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

// TestCloseStepTurn_PersistsAStepsContent is the defect. A chat-parented run's
// step frames fold onto the launching chat and open a turn there, the attribution
// gate drops that turn's own turn_end, and before this closer nothing on the run's
// completion reached the turn lifecycle at all — so a reader who watched a run and
// then reloaded lost everything its steps produced except each node's captured
// output.
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

// TestCloseStepTurn_LeavesAPromptTurnAlone is why the read is keyed on the turn's
// SOURCE rather than on displaceableEngineTurn, which also matches the chat's own
// agent-initiated turn. A run ending says nothing about a turn this chat prompted:
// closing one would take a live reply away from the reader mid-answer.
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

// TestCloseStepTurn_AnEmptyStepTurnPersistsAndAnnouncesNothing is THE RULING, and
// it is the inverse of the prompted case a marker exists for.
//
// `carried` is snap.Started, and every path that opens a step turn AND folds
// content calls ensureTurnStarted immediately — so nothing folded here means no
// message_created was ever broadcast and nothing entered the TRANSCRIPT. A marker
// would therefore close no transcript divergence, and what it WOULD do is open a
// headless turn card with a stopped mark and an empty body in a conversation the
// turn is not about. The announcement goes with it: turn_ended means "this chat's
// turn ended", and every effect of one lands on the launching chat's OWN last turn.
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

// TestCloseStepTurn_InsertsAheadOfATrailingUserRow pins where the message LANDS.
//
// A prompt sent while a run's steps were still folding persists its user row
// first, and the client places such a row AFTER the reply it arrived during. A
// plain append would record the reply as following that prompt, so the file and
// the array every client is showing would disagree about the order.
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

// TestCloseAsInterrupted_InsertsAStepsContentAheadOfATrailingUserRow is the same
// ordering rule reached by a BRIDGE DEATH, which is the closer it did not cover
// while the choice was keyed on the closer rather than on the turn's source.
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

// TestCloseOnWireEnd_InsertsAStepsContentAheadOfATrailingUserRow is the third
// closer that can claim a step turn: the CHAT's own bracket, whose AnyOpen claim
// takes whatever is open. Narrower window than a bridge death — the trailing row
// means a prompt already on disk whose StartTurn has not yet displaced the step turn
// — and it was the other closer the per-closer list left out.
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

// TestCloseAsInterrupted_AnEmptyStepTurnPersistsAndAnnouncesNothing is the ruling
// on the closer the marker gate could not reach. A bridge death claims whatever is
// open, so an agent process exiting while an EMPTY step turn was open used to append
// an EventInterrupted row — a headless turn card with a BROKEN mark, in a
// conversation the turn is not about — and announce it on top.
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

// TestCloseStepTurn_AStepTurnWithContentStillAnnounces is the other side of the
// broadcast gate, and it is what stops the gate being narrowed to `carried` alone:
// a carrier exists here, so the client's latch is right and the dot it sets is
// re-derived from the persisted header after a reload.
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

// TestCloseOnWireEnd_AnEmptyStepTurnCancelledStillPersistsAndAnnounces is the
// population the gate must NOT swallow. A cancel persists its own EventCancelled row
// carrying the outcome whatever the turn's source, so there IS a carrier and the
// client must be told — which is why the gate reads what the close persisted rather
// than re-deriving it from the source.
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

// TestCloseStepTurn_FailsAnUnsettledToolCall is the spinner rule. The run's
// terminal transition ends the only thing that could still send a
// tool_call_update for a step's call, so a card persisted `in_progress` renders as
// a permanent spinner on every later reload.
func TestCloseStepTurn_FailsAnUnsettledToolCall(t *testing.T) {
	h, cs, _ := newTestHub()
	stagedStepTurn(t, h, cs, "c1", "the step ran a command")
	buf := h.liveTurnBuffer("c1")
	if buf == nil {
		t.Fatal("the fixture opened no step turn")
	}
	buf.AppendToolCall(&vibekit.ToolCall{ID: "tc-1", Title: "Run command", Status: vibekit.ToolInProgress})

	h.coord.CloseStepTurn(t.Context(), "c1")

	// TWO guards rather than one disjunction: the second reads msgs[0], so folding
	// them makes the Fatalf's own condition panic when nothing was persisted.
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

// TestCloseStepTurn_IsIdempotent is what makes the call safe at a site that
// cannot know whether an earlier frame already closed the turn. The claim is
// first-wins, so a second call finds nothing open.
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

// TestCloseStepTurn_MintsNoLifecycle is why the read goes through `lookup`. This
// is asked once per terminal run frame — a run-lifecycle event, not a turn one —
// so asking must not record that it was asked. An entry leaves the map only
// through forget, so a minted one and its `changed` channel would outlive every
// chat that never had a turn.
func TestCloseStepTurn_MintsNoLifecycle(t *testing.T) {
	h, _, _ := newTestHub()

	h.coord.CloseStepTurn(t.Context(), "c-none")

	if _, ok := h.coord.turns.lookup("c-none"); ok {
		t.Error("asking about a chat with no turn minted a lifecycle for it")
	}
}
