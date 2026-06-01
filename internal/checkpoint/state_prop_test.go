package checkpoint

import (
	"fmt"
	"slices"
	"testing"

	"pgregory.net/rapid"
)

func TestState_RapidInvariantsHold(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := newState()

		numEvents := rapid.IntRange(1, 50).Draw(t, "numEvents")
		turn := 0

		for i := range numEvents {
			kind := rapid.IntRange(0, 2).Draw(t, fmt.Sprintf("kind_%d", i))
			switch kind {
			case 0: // advanceTurn
				turn++
				s.apply(&event{Kind: kindTurnStart, Turn: turn})
			case 1: // snapshot
				path := rapid.StringMatching(`[a-z]{1,5}\.go`).Draw(t, fmt.Sprintf("path_%d", i))
				beforeSHA := rapid.StringMatching(`[0-9a-f]{8}`).Draw(t, fmt.Sprintf("before_%d", i))
				afterSHA := rapid.StringMatching(`[0-9a-f]{8}`).Draw(t, fmt.Sprintf("after_%d", i))
				tag := s.allocateTag()
				s.apply(&event{
					Kind:      kindSnapshot,
					Tag:       tag,
					Path:      path,
					BeforeSHA: beforeSHA,
					AfterSHA:  afterSHA,
					Turn:      turn,
					Tool:      s.toolsInTurn,
				})
			case 2: // restoreCommitted
				if len(s.orderedTags) > 0 {
					idx := rapid.IntRange(0, len(s.orderedTags)-1).Draw(t, fmt.Sprintf("restore_%d", i))
					s.apply(&event{Kind: kindRestoreCommitted, Tag: s.orderedTags[idx]})
				}
			}
		}

		// Invariant 1: orderedTags is sorted by compareTags.
		if !slices.IsSortedFunc(s.orderedTags, compareTags) {
			t.Fatal("orderedTags not sorted")
		}

		// Invariant 2: len(tags) >= len(orderedTags) (no phantom ordered tags).
		for _, tag := range s.orderedTags {
			if _, ok := s.tags[tag]; !ok {
				t.Fatalf("orderedTag %q not in tags map", tag)
			}
		}

		// Invariant 3: every tag in tagFiles has a corresponding entry in tags.
		for tag := range s.tagFiles {
			if _, ok := s.tags[tag]; !ok {
				t.Fatalf("tagFiles key %q not in tags map", tag)
			}
		}

		// Invariant 4: turn is monotonically non-decreasing (final turn >= 0).
		if s.turn < 0 {
			t.Fatalf("turn = %d, want >= 0", s.turn)
		}

		// Invariant 5: blobRefs contains every SHA referenced by any snapshot.
		for path, history := range s.fileHistory {
			for _, obs := range history {
				if obs.beforeSHA != "" {
					if _, ok := s.blobRefs[obs.beforeSHA]; !ok {
						t.Fatalf("blobRefs missing beforeSHA %q from %s@%s", obs.beforeSHA, path, obs.tag)
					}
				}
				if obs.afterSHA != "" {
					if _, ok := s.blobRefs[obs.afterSHA]; !ok {
						t.Fatalf("blobRefs missing afterSHA %q from %s@%s", obs.afterSHA, path, obs.tag)
					}
				}
			}
		}
	})
}
