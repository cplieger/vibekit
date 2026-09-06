package agent

// What the ONE carrier of a turn's facts has to hold, and where it lands.
//
// A turn's footer facts — credits, elapsed, changed files, model — belong to the
// turn rather than to any part of it, so exactly one persisted message carries
// them. When the turn was SPLIT and produced nothing after the split, that
// carrier is an event row, and every fact the assistant message would have held
// has to reach it instead.

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// The client's turn ledger reads turn_model off every row in a turn's body, so a
// segmented turn whose only carrier is an event showed its numbers attributed to
// nothing. The buffer's LATCHED model is the answer rather than the turn
// record's: the record holds what the chat was set to at open, and a fast
// in-session switch moves it after that.
func TestCloseOnWireEnd_ASplitTurnsCarrierNamesItsModel(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "everything before the boundary")
	h.liveTurnBuffer("c1").SetModel("m-latched")
	if !h.coord.SealTurnSegment(t.Context(), "c1") {
		t.Fatal("the fixture could not seal a segment")
	}

	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonEndTurn, "")

	marker := outcomeMarker(t, cs, "c1")
	if marker == nil {
		t.Fatal("a split turn with nothing after it persisted no carrier")
	}
	if marker.TurnModel != "m-latched" {
		t.Errorf("carrier TurnModel = %q, want the buffer's latched %q", marker.TurnModel, "m-latched")
	}
	// The segment itself claims none of the turn's facts, so the carrier is the
	// only place the model can be read from.
	for _, m := range assistantMessages(t, cs, "c1") {
		if m.TurnModel != "" {
			t.Errorf("segment %q claims TurnModel %q; exactly one carrier per turn", m.ID, m.TurnModel)
		}
	}
}

// A cancelled split turn's carrier is the cancel event, and it needs the model
// for the same reason the marker does.
func TestCloseOnWireEnd_ACancelledSplitTurnsEventNamesItsModel(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "everything before the cancel")
	h.liveTurnBuffer("c1").SetModel("m-latched")
	if !h.coord.SealTurnSegment(t.Context(), "c1") {
		t.Fatal("the fixture could not seal a segment")
	}

	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonCancelled, "")

	evt := eventMessageOf(t, cs, "c1", vibekit.EventCancelled)
	if evt == nil {
		t.Fatal("a cancelled split turn persisted no cancel event")
	}
	if evt.TurnModel != "m-latched" {
		t.Errorf("cancel event TurnModel = %q, want the buffer's latched %q", evt.TurnModel, "m-latched")
	}
}

// An INTERRUPTED split turn keeps its changed files, which the cancel arm's fix
// did not reach.
//
// SplitSegment clears Started, so a split turn always takes the no-partial branch,
// where the divider carried the conclusion and nothing else. Omitting the stats is
// deliberate; that reasoning never covered the file map, because those files are on
// disk whatever stopped the turn and the divider is their only carrier.
func TestAbandonInFlightTurn_AnInterruptedSplitTurnKeepsItsChangedFiles(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "everything before the interruption")
	h.liveTurnBuffer("c1").SetModel("m-latched")
	diffs := []vibekit.ToolDiff{{Path: "a.go", OldText: "x\n", NewText: "x\ny\n"}}
	h.liveTurnBuffer("c1").TrackFileChanges(diffs, false)
	if !h.coord.SealTurnSegment(t.Context(), "c1") {
		t.Fatal("the fixture could not seal a segment")
	}
	epoch, open := h.coord.turns.openEpoch("c1")
	if !open {
		t.Fatal("the seal closed the turn; there is nothing left to interrupt")
	}

	h.coord.AbandonInFlightTurn(t.Context(), "c1", epoch, "the pipe died")

	divider := eventMessageOf(t, cs, "c1", vibekit.EventInterrupted)
	if divider == nil {
		t.Fatal("an interrupted split turn persisted no divider, so nothing carries its outcome")
	}
	if divider.ChangedFiles["a.go"] == nil {
		t.Errorf("divider ChangedFiles = %v, want the turn's cumulative map", divider.ChangedFiles)
	}
	if divider.TurnModel != "m-latched" {
		t.Errorf("divider TurnModel = %q, want the buffer's latched %q", divider.TurnModel, "m-latched")
	}
	// Still no spend: that omission is deliberate and unchanged.
	if divider.TurnCredits != 0 || divider.TurnElapsedMs != 0 {
		t.Errorf("divider carries credits %v / elapsed %v, want neither for an interrupted turn",
			divider.TurnCredits, divider.TurnElapsedMs)
	}
	// One carrier: a marker beside the divider would open a spurious second turn.
	if m := outcomeMarker(t, cs, "c1"); m != nil {
		t.Errorf("an interrupted turn persisted a second carrier: %+v", m)
	}
}

// A turn a PROMPT displaced is persisted AHEAD of the prompt that ended it, so
// the chat file records the order the two actually ran in.
//
// The prompt writes its user row before it asks for admission and an engine-opened
// turn holds no reservation, so the displacement runs with that row already on
// disk. Appending recorded the reply as following the prompt, which projectTurns
// reads as a headerless turn BELOW it — while the client's array has it above,
// since the broadcast carries the streamed message's id and merges in place.
func TestStartTurn_ADisplacedTurnIsPersistedAheadOfThePromptThatEndedIt(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	// An engine-opened turn mid-reply: a workflow step's frames folding onto the
	// launching chat is the ordinary case.
	if err := cs.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	h.coord.StartTurn(ctx, "c1", vibekit.TurnSourceWireTurnStart)
	buf := h.stageTurnBuffer(t, "c1")
	buf.Started = true
	buf.MessageID = "m-step"
	buf.Content.WriteString("the step's reply")

	// The prompt's own user row lands first, exactly as CmdPrompt writes it.
	if err := cs.AppendMessage(ctx, "c1", &vibekit.Message{
		ID: "u-2", Role: vibekit.RoleUser, Ts: 1, Content: "a new question",
	}); err != nil {
		t.Fatalf("append the prompt's user row: %v", err)
	}

	h.coord.StartTurn(ctx, "c1", vibekit.TurnSourcePrompt)

	c, ok := cs.Get(ctx, "c1")
	if !ok {
		t.Fatal("chat c1 vanished")
	}
	got := make([]string, 0, len(c.Messages))
	for i := range c.Messages {
		got = append(got, c.Messages[i].ID)
	}
	if len(got) != 2 || got[0] != "m-step" || got[1] != "u-2" {
		t.Errorf("persisted ids = %v, want [m-step u-2]: the displaced reply ran first", got)
	}
}
