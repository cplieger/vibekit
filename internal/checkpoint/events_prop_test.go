package checkpoint

import (
	"strconv"
	"testing"

	"pgregory.net/rapid"
)

// FuzzCompareTagsOrdering exercises compareTags with arbitrary byte
// pairs and asserts the comparison function satisfies reflexivity,
// antisymmetry, and that atoiSafe never panics on arbitrary input.
// Seed corpus drawn from TestCompareTags and
// TestCompareTagsAgreesWithNumericOrder.
func FuzzCompareTagsOrdering(f *testing.F) {
	seeds := []string{
		"0", "1", "2", "10", "0.0", "1.0", "1.2", "1.10",
		"2.1", "2.9", "2.10", "10.5", "",
	}
	for _, a := range seeds {
		for _, b := range seeds {
			f.Add(a, b)
		}
	}

	f.Fuzz(func(t *testing.T, a, b string) {
		ab := compareTags(a, b)
		ba := compareTags(b, a)

		// Reflexivity.
		if aa := compareTags(a, a); aa != 0 {
			t.Errorf("reflexivity: compareTags(%q, %q) = %d, want 0", a, a, aa)
		}

		// Antisymmetry: sign(cmp(a,b)) == -sign(cmp(b,a)).
		if sign(ab) != -sign(ba) {
			t.Errorf("antisymmetry: compareTags(%q,%q)=%d, compareTags(%q,%q)=%d",
				a, b, ab, b, a, ba)
		}
	})
}

// FuzzParseTagRoundTrip generates (turn, tool) pairs, formats them
// as tags via the same logic allocateTag uses, then asserts parseTag
// recovers the original values.
func FuzzParseTagRoundTrip(f *testing.F) {
	f.Add(0, 0)
	f.Add(1, 0)
	f.Add(1, 1)
	f.Add(10, 5)
	f.Add(999, 42)

	f.Fuzz(func(t *testing.T, turn, tool int) {
		// Constrain to non-negative values that atoiSafe handles.
		if turn < 0 || tool < 0 || turn > 1_000_000 || tool > 1_000_000 {
			return
		}
		var tag string
		if tool == 0 {
			tag = strconv.Itoa(turn)
		} else {
			tag = strconv.Itoa(turn) + "." + strconv.Itoa(tool)
		}
		gotTurn, gotTool := parseTag(tag)
		if gotTurn != turn {
			t.Errorf("parseTag(%q) turn = %d, want %d", tag, gotTurn, turn)
		}
		// tool==0 produces "N" which parseTag reads as (N, 0).
		if gotTool != tool {
			t.Errorf("parseTag(%q) tool = %d, want %d", tag, gotTool, tool)
		}
	})
}

func sign(x int) int {
	switch {
	case x < 0:
		return -1
	case x > 0:
		return 1
	default:
		return 0
	}
}

func TestContentAtTag_RapidInvariants(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := newState()
		turn := 0

		type snapshot struct {
			path, tag, beforeSHA, afterSHA string
		}
		var snapshots []snapshot

		numOps := rapid.IntRange(1, 40).Draw(t, "numOps")
		for i := range numOps {
			// Randomly advance turn or take a snapshot.
			if rapid.Bool().Draw(t, "advanceTurn_"+strconv.Itoa(i)) {
				turn++
				s.apply(&event{Kind: kindTurnStart, Turn: turn})
				continue
			}
			path := rapid.SampledFrom([]string{"a.go", "b.go", "c.go"}).Draw(t, "path_"+strconv.Itoa(i))
			beforeSHA := rapid.StringMatching(`[0-9a-f]{8}`).Draw(t, "before_"+strconv.Itoa(i))
			afterSHA := rapid.StringMatching(`[0-9a-f]{8}`).Draw(t, "after_"+strconv.Itoa(i))
			tag := s.allocateTag()
			toolIdx := s.toolsInTurn
			s.apply(&event{
				Kind:      kindSnapshot,
				Tag:       tag,
				Path:      path,
				BeforeSHA: beforeSHA,
				AfterSHA:  afterSHA,
				Turn:      turn,
				Tool:      toolIdx,
			})
			snapshots = append(snapshots, snapshot{path, tag, beforeSHA, afterSHA})
		}

		// Build a map of (path, tag) → last beforeSHA to handle the case
		// where the same path is snapshotted at the same tag (which shouldn't
		// happen with correct tag allocation, but we verify the lookup matches
		// the actual fileHistory).
		type pathTag struct{ path, tag string }
		lastBefore := make(map[pathTag]string)
		for _, snap := range snapshots {
			lastBefore[pathTag{snap.path, snap.tag}] = snap.beforeSHA
		}

		for pt, expectedBefore := range lastBefore {
			got, ok := s.contentAtTag(pt.path, pt.tag)
			if expectedBefore == "" {
				if ok {
					t.Fatalf("contentAtTag(%q, %q) = (%q, true), want (_, false) for empty beforeSHA",
						pt.path, pt.tag, got)
				}
			} else {
				if !ok || got != expectedBefore {
					t.Fatalf("contentAtTag(%q, %q) = (%q, %v), want (%q, true)",
						pt.path, pt.tag, got, ok, expectedBefore)
				}
			}

			// Invariant 3: contentAtOrBeforeTag must return a result whenever contentAtTag does.
			catResult, catOk := s.contentAtTag(pt.path, pt.tag)
			caobResult, caobOk := s.contentAtOrBeforeTag(pt.path, pt.tag)
			if catOk && !caobOk {
				t.Fatalf("contentAtTag(%q, %q) = (%q, true) but contentAtOrBeforeTag = (_, false)",
					pt.path, pt.tag, catResult)
			}
			if catOk && caobOk && catResult != caobResult {
				t.Fatalf("contentAtTag(%q, %q) = %q but contentAtOrBeforeTag = %q",
					pt.path, pt.tag, catResult, caobResult)
			}
		}

		// Invariant 2: contentAtTag for a tag before any write to a path returns ("", false).
		if len(s.orderedTags) > 0 {
			_, ok := s.contentAtTag("never_written.go", s.orderedTags[0])
			if ok {
				t.Fatal("contentAtTag for unwritten path returned true")
			}
		}

		// Invariant 4: the function never panics on tags not present in the log.
		s.contentAtTag("a.go", "999.999")
		s.contentAtOrBeforeTag("a.go", "999.999")
	})
}
