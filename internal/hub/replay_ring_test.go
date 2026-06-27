package hub

// Unit tests for replay_ring.go. Concurrent coverage lives in
// replay_ring_concurrent_fuzz_test.go.

import "testing"

// After overflowing a cap-3 ring the oldest event is evicted and
// Events() returns the survivors in oldest -> newest order.
func TestReplayRing_EventsWrappedOrder(t *testing.T) {
	r := newReplayRing(3)
	for _, id := range []uint64{1, 2, 3, 4} {
		r.Append(sseEvent{eventID: id})
	}
	got := r.Events()
	want := []uint64{2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("Events() len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].eventID != want[i] {
			t.Errorf("Events()[%d].eventID = %d, want %d (full order: %v)", i, got[i].eventID, want[i], replayEventIDs(got))
		}
	}
}

func replayEventIDs(evs []sseEvent) []uint64 {
	out := make([]uint64, len(evs))
	for i, e := range evs {
		out[i] = e.eventID
	}
	return out
}
