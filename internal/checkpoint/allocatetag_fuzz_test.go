package checkpoint

import (
	"testing"

	chktypes "vibekit/internal/checkpoint/types"
)

// FuzzAllocateTagMonotonic verifies that successive allocateTag calls produce
// tags that are parseable by ParseTag and strictly increasing under compareTags.
//
// Bug class: tag collision when toolsInTurn overflows, non-monotonic allocation
// after state resets, non-parseable tag format.
func FuzzAllocateTagMonotonic(f *testing.F) {
	f.Add(0, 0, 5)
	f.Add(1, 0, 3)
	f.Add(1, 2, 4)
	f.Add(0, 10, 2)
	f.Add(100, 0, 6)
	f.Add(0, 0, 1)

	f.Fuzz(func(t *testing.T, turn, startTools, count int) {
		if turn < 0 || turn > 10000 || startTools < 0 || startTools > 10000 || count < 1 || count > 20 {
			return
		}

		s := &state{
			tags:        make(map[string]int),
			tagFiles:    make(map[string][]string),
			fileHistory: make(map[string][]fileObservation),
			blobRefs:    make(map[string]struct{}),
			turn:        turn,
			toolsInTurn: startTools,
		}

		var prev string
		for i := range count {
			tag := s.allocateTag()
			s.toolsInTurn++

			// Invariant 1: tag is parseable.
			if _, err := chktypes.ParseTag(tag); err != nil {
				t.Fatalf("iteration %d: allocateTag() = %q; not parseable: %v", i, tag, err)
			}

			// Invariant 2: successive tags are strictly increasing.
			if prev != "" && compareTags(prev, tag) >= 0 {
				t.Fatalf("iteration %d: tags not monotonic: %q >= %q", i, prev, tag)
			}
			prev = tag
		}
	})
}
