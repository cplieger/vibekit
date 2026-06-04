package auth

import (
	"testing"
)

// FuzzLineRingPushSample exercises the lineRing's sliding-window eviction
// and per-line cap truncation logic with arbitrary push sequences. The
// invariants:
//   - Sample() length never exceeds 2*halfCap
//   - No element in Sample() exceeds perLineCap bytes
//   - First elements are preserved until halfCap is reached
//   - Last elements form a sliding window of size halfCap
func FuzzLineRingPushSample(f *testing.F) {
	f.Add([]byte("short\nmedium line here\na very long line that should be truncated"))
	f.Add([]byte{})
	f.Add([]byte("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\nl\nm"))

	f.Fuzz(func(t *testing.T, data []byte) {
		const halfCap = 5
		const perLineCap = 16
		ring := newLineRing(halfCap, perLineCap)

		// Push lines split on newlines, or byte chunks.
		var pushed int
		for i := 0; i < len(data); {
			end := i
			for end < len(data) && data[end] != '\n' {
				end++
			}
			ring.Push(string(data[i:end]))
			pushed++
			i = end + 1
		}

		sample := ring.Sample()

		// Invariant 1: length bounded.
		if len(sample) > 2*halfCap {
			t.Fatalf("Sample() len %d > 2*halfCap (%d)", len(sample), 2*halfCap)
		}

		// Invariant 2: no element exceeds perLineCap.
		for i, s := range sample {
			if len(s) > perLineCap {
				t.Fatalf("Sample()[%d] len %d > perLineCap %d", i, len(s), perLineCap)
			}
		}

		// Invariant 3: if pushed <= halfCap, sample == first (all preserved).
		if pushed <= halfCap && len(sample) != pushed {
			t.Fatalf("pushed %d <= halfCap but sample len %d", pushed, len(sample))
		}

		// Invariant 4: if pushed > 2*halfCap, sample len == 2*halfCap.
		if pushed > 2*halfCap && len(sample) != 2*halfCap {
			t.Fatalf("pushed %d > 2*halfCap but sample len %d != %d",
				pushed, len(sample), 2*halfCap)
		}
	})
}
