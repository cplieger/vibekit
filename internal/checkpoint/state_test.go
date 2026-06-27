package checkpoint

import "testing"

// TestStateApply_SnapshotAdvancesTurn pins that applySnapshot advances
// s.turn when the event's turn exceeds the current turn: on a fresh
// state (turn 0) a snapshot with Turn 2 advances the state to turn 2.
func TestStateApply_SnapshotAdvancesTurn(t *testing.T) {
	s := newState()
	s.apply(&event{Kind: kindSnapshot, Turn: 2, Tool: 0, Tag: "2", Path: "f"})
	if s.turn != 2 {
		t.Errorf("state.turn = %d, want 2: a snapshot whose turn exceeds the current turn must advance it", s.turn)
	}
}

// TestStateApply_NoRingFullWarnBelowCap pins that applyConflict does
// not emit the ring-full warn when the conflict ring is below capacity,
// while still appending the conflict.
func TestStateApply_NoRingFullWarnBelowCap(t *testing.T) {
	s := newState()
	has := captureLogs(t)
	s.apply(&event{Kind: kindConflict, Path: "f", Tag: "1", BeforeSHA: "b", OtherChat: "o", ExpectedSHA: "e", TS: 1})
	if has("conflict ring full") {
		t.Errorf("applyConflict logged 'conflict ring full' with an empty ring; that warn must fire only at capacity")
	}
	if got := s.conflicts.Len(); got != 1 {
		t.Errorf("conflicts.Len() = %d, want 1: the append must run regardless of the warn", got)
	}
}
