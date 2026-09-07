package agent

import "testing"

// TestReplayDrain_Complete is the shared condition on its own, away from either
// route's maps and effects. Both routes read this type, so a case here is a case
// for the chat twin AND the step registry — which is the whole reason the
// condition is one type rather than two copies of a comparison.
func TestReplayDrain_Complete(t *testing.T) {
	// note is what the owner reports before asking; the two spellings are the two
	// facts a drain accepts.
	consumed := func(gen, seq uint64) func(*replayDrain) {
		return func(d *replayDrain) { d.noteConsumed(drainPoint{gen: gen, seq: seq}) }
	}
	loadedAt := func(gen, seq uint64) func(*replayDrain) {
		return func(d *replayDrain) { d.markLoadedAt(drainPoint{gen: gen, seq: seq}) }
	}

	for name, tc := range map[string]struct {
		notes  []func(*replayDrain)
		gen    uint64
		sealed bool
		want   bool
	}{
		"a load that has not returned never completes": {
			notes: []func(*replayDrain){consumed(1, 9)},
			gen:   1, want: false,
		},
		"the position reached exactly completes": {
			notes: []func(*replayDrain){loadedAt(1, 4), consumed(1, 4)},
			gen:   1, want: true,
		},
		"a position short of the load does not": {
			notes: []func(*replayDrain){loadedAt(1, 4), consumed(1, 3)},
			gen:   1, want: false,
		},
		"a position PAST the load completes": {
			notes: []func(*replayDrain){loadedAt(1, 4), consumed(1, 7)},
			gen:   1, want: true,
		},
		"the load alone completes when it arrived at a position already reached": {
			// Defect (a): the frames drained before the RPC returned, so the
			// condition holds the instant the load is recorded and no later frame
			// is coming to notice.
			notes: []func(*replayDrain){consumed(1, 4), loadedAt(1, 4)},
			gen:   1, want: true,
		},
		"a load at position ZERO completes on its own": {
			// 0 is a legal position, not a sentinel: the sequence is stamped by a
			// pre-increment, so a response that arrived before any frame is at 0 and
			// there is nothing left to wait for.
			notes: []func(*replayDrain){loadedAt(1, 0)},
			gen:   1, want: true,
		},
		"a straggler from a lower attachment neither advances nor satisfies": {
			notes: []func(*replayDrain){loadedAt(2, 4), consumed(1, 900)},
			gen:   2, want: false,
		},
		"a straggler cannot settle from its OWN attachment either": {
			notes: []func(*replayDrain){loadedAt(2, 4), consumed(1, 900)},
			gen:   1, want: false,
		},
		"a HIGHER attachment resets the observed position": {
			notes: []func(*replayDrain){loadedAt(1, 4), consumed(1, 4), consumed(2, 1)},
			gen:   2, want: false,
		},
		"a HIGHER attachment invalidates the load": {
			// The frames that load bounded are queued on a channel nobody will
			// drain further, so its position means nothing on the new attachment —
			// not even to a seal.
			notes: []func(*replayDrain){loadedAt(1, 4), consumed(2, 9)},
			gen:   2, sealed: true, want: false,
		},
		"a seal completes without the position": {
			notes: []func(*replayDrain){loadedAt(1, 4), consumed(1, 1)},
			gen:   1, sealed: true, want: true,
		},
		"a seal never completes without the load": {
			notes: []func(*replayDrain){consumed(1, 9)},
			gen:   1, sealed: true, want: false,
		},
		"a seal from a lower attachment completes nothing": {
			notes: []func(*replayDrain){loadedAt(2, 4), consumed(2, 4)},
			gen:   1, sealed: true, want: false,
		},
		"a zero drain completes for nobody": {
			gen: 0, want: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var d replayDrain
			for _, note := range tc.notes {
				note(&d)
			}
			if got := d.complete(tc.gen, tc.sealed); got != tc.want {
				t.Errorf("complete(gen=%d, sealed=%v) = %v, want %v (drain %+v)",
					tc.gen, tc.sealed, got, tc.want, d)
			}
		})
	}
}

// TestReplayDrain_ObservedNeverGoesBACKWARD: the consumer reports the position of
// each frame it folds, and a frame can only ever be folded in order — but a
// same-attachment report carrying a lower number must not undo the high-water mark
// either, because the settle is a "have we reached" question and an unnoticed
// regression would park a completed replay forever.
func TestReplayDrain_ObservedNeverGoesBackward(t *testing.T) {
	var d replayDrain
	d.markLoadedAt(drainPoint{gen: 1, seq: 5})
	d.noteConsumed(drainPoint{gen: 1, seq: 5})
	d.noteConsumed(drainPoint{gen: 1, seq: 2})

	if !d.complete(1, false) {
		t.Errorf("a lower same-attachment report un-completed the drain: %+v", d)
	}
}
