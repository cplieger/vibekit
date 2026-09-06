package agent

// The mid-turn segment seal: what SealTurnSegment persists, when it refuses, and
// what a split turn's closer still has to carry.

import (
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func assistantMessages(t *testing.T, cs *fakeChatStore, chatID vibekit.ChatID) []vibekit.Message {
	t.Helper()
	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		t.Fatalf("chat %q vanished", chatID)
	}
	var out []vibekit.Message
	for i := range c.Messages {
		if c.Messages[i].Role == vibekit.RoleAssistant {
			out = append(out, c.Messages[i])
		}
	}
	return out
}

// eventMessageOf returns the chat's LAST event message of one kind, or nil.
func eventMessageOf(
	t *testing.T,
	cs *fakeChatStore,
	chatID vibekit.ChatID,
	kind vibekit.EventKind,
) *vibekit.Message {
	t.Helper()
	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		t.Fatalf("chat %q vanished", chatID)
	}
	var found *vibekit.Message
	for i := range c.Messages {
		if c.Messages[i].EventKind == kind {
			found = &c.Messages[i]
		}
	}
	return found
}

func outcomeMarker(t *testing.T, cs *fakeChatStore, chatID vibekit.ChatID) *vibekit.Message {
	t.Helper()
	return eventMessageOf(t, cs, chatID, vibekit.EventTurnOutcome)
}

// A seal represents a boundary INSIDE a turn as the sibling message every consumer
// already reads in array order, so the buffer must be left ready for the rest.
func TestSealTurnSegment_PersistsTheSegmentAndReadiesTheBuffer(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "before the boundary")

	if !h.coord.SealTurnSegment(t.Context(), "c1") {
		t.Fatal("SealTurnSegment refused a started turn with content")
	}

	msgs := assistantMessages(t, cs, "c1")
	if len(msgs) != 1 {
		t.Fatalf("persisted %d assistant messages, want 1", len(msgs))
	}
	if msgs[0].Content != "before the boundary" {
		t.Errorf("sealed segment content = %q, want %q", msgs[0].Content, "before the boundary")
	}
	// Exactly one persisted message per turn may carry the outcome; a second carrier
	// opens a spurious segment for both projections.
	if msgs[0].TurnOutcome != "" || msgs[0].TurnCredits != 0 || msgs[0].TurnElapsedMs != 0 {
		t.Errorf("sealed segment carried turn facts: outcome=%q credits=%v elapsed=%v; want none",
			msgs[0].TurnOutcome, msgs[0].TurnCredits, msgs[0].TurnElapsedMs)
	}
	// The rest of the turn must accumulate into a fresh message, not re-extend the
	// one already on disk.
	if snap := h.liveTurnBuffer("c1").TakeTurn(); snap.Started || snap.Content != "" {
		t.Errorf("after the seal the buffer holds %q with Started = %t, want empty and false",
			snap.Content, snap.Started)
	}
}

// A turn holding a tool call in flight is NOT split: an update resolves its call
// against the CURRENT buffer, so a call sealed mid-flight can never be written back
// and its card renders as a permanent spinner in a message nothing rewrites.
func TestSealTurnSegment_RefusesWithAToolInFlight(t *testing.T) {
	h, cs, _ := newTestHub()
	logs := captureLogs(t) // not parallel: swaps the slog default
	startedTurnOn(t, h, cs, "c1", "before the boundary")
	h.liveTurnBuffer("c1").AppendToolCall(&vibekit.ToolCall{ID: "t-1", Status: vibekit.ToolInProgress})

	if h.coord.SealTurnSegment(t.Context(), "c1") {
		t.Error("SealTurnSegment split a turn holding an in-flight tool call")
	}

	if msgs := assistantMessages(t, cs, "c1"); len(msgs) != 0 {
		t.Errorf("a refused seal persisted %d assistant messages, want 0", len(msgs))
	}
	if snap := h.liveTurnBuffer("c1").TakeTurn(); snap.Content != "before the boundary" {
		t.Errorf("after a refused seal the buffer holds %q, want the turn's content intact", snap.Content)
	}
	if !strings.Contains(logs.String(), "turn segment not sealed") {
		t.Errorf("the refusal was silent, so the wrong position has no explanation; logs = %q", logs.String())
	}
}

// A seal must never OPEN a turn to discover there was none: an opened-then-sealed
// turn is a phantom assistant message on a chat that was idle.
func TestSealTurnSegment_RefusesWithNoTurnOpen(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	if h.coord.SealTurnSegment(t.Context(), "c1") {
		t.Error("SealTurnSegment reported a seal on a chat with no open turn")
	}
	c, _ := cs.Get(t.Context(), "c1")
	if len(c.Messages) != 0 {
		t.Errorf("a seal with no turn open persisted %d messages: %+v", len(c.Messages), c.Messages)
	}
}

// A compaction can land between the id mint and the first delta, and `Started` is
// exactly what minting sets — so a seal gated on it persists a BLANK assistant row
// and hands the rest of the reply a second id the client is not streaming under.
func TestSealTurnSegment_RefusesBetweenTheIDMintAndTheFirstDelta(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)
	buf := h.stageTurnBuffer(t, "c1")
	if !buf.StartTurn(newMessageID()) {
		t.Fatal("the fixture could not mint the turn's message id")
	}

	if h.coord.SealTurnSegment(t.Context(), "c1") {
		t.Error("SealTurnSegment sealed a turn whose id was minted but which had streamed nothing")
	}

	if msgs := assistantMessages(t, cs, "c1"); len(msgs) != 0 {
		t.Errorf("the seal persisted %d assistant messages, want 0 — an empty one is a blank transcript row", len(msgs))
	}
	snap := h.liveTurnBuffer("c1").TakeTurn()
	if snap.Segmented {
		t.Error("a refused seal latched Segmented, so the closer will report a split that never happened")
	}
	if !snap.Started || snap.MessageID == "" {
		t.Errorf("after a refused seal Started = %t and MessageID = %q; the reply must keep the id message_created announced",
			snap.Started, snap.MessageID)
	}
}

// The refusal must leave no trace: a turn marked as split sends its closer looking
// for a segment nothing persisted.
func TestSealTurnSegment_RefusesATurnThatEmittedNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	h.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)
	h.stageTurnBuffer(t, "c1")

	if h.coord.SealTurnSegment(t.Context(), "c1") {
		t.Error("SealTurnSegment reported a seal on a turn that emitted nothing")
	}
	if snap := h.liveTurnBuffer("c1").TakeTurn(); snap.Segmented {
		t.Error("a refused seal latched Segmented, so the closer will report a split that never happened")
	}
	c, _ := cs.Get(t.Context(), "c1")
	if len(c.Messages) != 0 {
		t.Errorf("a refused seal persisted %d messages: %+v", len(c.Messages), c.Messages)
	}
}

// A turn whose reply ended exactly at the split point has nothing left to persist,
// so its credits, duration and changed files have no assistant message to ride. The
// marker carries them; without it the footer goes silent on a turn that did work.
func TestCloseOnWireEnd_ASplitTurnWithNothingAfterItKeepsItsFooter(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "everything this turn said")
	diffs := []vibekit.ToolDiff{{Path: "a.go", OldText: "x\n", NewText: "x\ny\n"}}
	h.liveTurnBuffer("c1").TrackFileChanges(diffs, false)
	if !h.coord.SealTurnSegment(t.Context(), "c1") {
		t.Fatal("the fixture could not seal a segment")
	}
	// Spend after the baseline was latched, so the turn has a credit delta.
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Usage.Credits = 0.5
		return true
	}); err != nil {
		t.Fatalf("record spend: %v", err)
	}

	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonEndTurn, "")

	if msgs := assistantMessages(t, cs, "c1"); len(msgs) != 1 {
		t.Errorf("persisted %d assistant messages, want just the sealed segment", len(msgs))
	}
	marker := outcomeMarker(t, cs, "c1")
	if marker == nil {
		t.Fatal("a split turn with nothing after the split persisted no marker, so its footer numbers reach no carrier")
	}
	if marker.TurnCredits != 0.5 {
		t.Errorf("marker TurnCredits = %v, want the turn's 0.5", marker.TurnCredits)
	}
	if marker.ChangedFiles["a.go"] == nil {
		t.Errorf("marker ChangedFiles = %v, want the turn's cumulative map", marker.ChangedFiles)
	}
	if marker.TurnOutcome != vibekit.TurnOutcomeCompleted {
		t.Errorf("marker TurnOutcome = %q, want completed", marker.TurnOutcome)
	}
}

// The cancel event is a cancelled turn's carrier — its presence is what stops a
// marker being written beside it — so the facts have to ride it or they reach
// nothing, and the footer shows live then vanishes on reload.
func TestCloseOnWireEnd_ACancelledSplitTurnKeepsItsFooter(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "everything before the cancel")
	diffs := []vibekit.ToolDiff{{Path: "a.go", OldText: "x\n", NewText: "x\ny\n"}}
	h.liveTurnBuffer("c1").TrackFileChanges(diffs, false)
	if !h.coord.SealTurnSegment(t.Context(), "c1") {
		t.Fatal("the fixture could not seal a segment")
	}
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Usage.Credits = 0.5
		return true
	}); err != nil {
		t.Fatalf("record spend: %v", err)
	}

	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonCancelled, "")

	evt := eventMessageOf(t, cs, "c1", vibekit.EventCancelled)
	if evt == nil {
		t.Fatal("a cancelled turn persisted no cancel event, so nothing carries its outcome")
	}
	if evt.TurnCredits != 0.5 {
		t.Errorf("cancel event TurnCredits = %v, want the turn's 0.5", evt.TurnCredits)
	}
	if evt.ChangedFiles["a.go"] == nil {
		t.Errorf("cancel event ChangedFiles = %v, want the turn's cumulative map", evt.ChangedFiles)
	}
	if evt.TurnOutcome == "" {
		t.Error("cancel event carries no TurnOutcome, so both projections read the turn as still open")
	}
	// One carrier per turn: a marker beside the cancel event opens a spurious second
	// turn for both projections.
	if m := outcomeMarker(t, cs, "c1"); m != nil {
		t.Errorf("a cancelled turn persisted a second carrier: %+v", m)
	}
}
