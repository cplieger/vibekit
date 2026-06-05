package checkpoint

import (
	"slices"
	"strconv"
	"testing"
)

// FuzzFindSortedInsert builds a sorted tag slice, inserts a new tag via
// findSorted, and asserts:
//  1. The slice remains sorted after insertion.
//  2. The inserted tag is findable at the returned index.
//  3. No panics on arbitrary (turn, tool) inputs.
func FuzzFindSortedInsert(f *testing.F) {
	f.Add(0, 0, 1, 0)
	f.Add(1, 2, 1, 1)
	f.Add(5, 0, 3, 0)
	f.Add(10, 5, 10, 6)
	f.Add(0, 0, 0, 0)

	f.Fuzz(func(t *testing.T, existTurn, existTool, newTurn, newTool int) {
		// Constrain to reasonable non-negative values.
		if existTurn < 0 || existTool < 0 || newTurn < 0 || newTool < 0 {
			return
		}
		if existTurn > 10000 || existTool > 10000 || newTurn > 10000 || newTool > 10000 {
			return
		}

		// Build a small sorted slice with a few tags around existTurn.
		var tags []string
		for turn := max(0, existTurn-1); turn <= existTurn+1; turn++ {
			for tool := max(0, existTool-1); tool <= existTool+1; tool++ {
				tags = append(tags, formatTag(turn, tool))
			}
		}
		slices.SortFunc(tags, compareTags)
		tags = slices.CompactFunc(tags, func(a, b string) bool { return compareTags(a, b) == 0 })

		newTag := formatTag(newTurn, newTool)
		idx, exists := findSorted(tags, newTag)

		if exists {
			if tags[idx] != newTag {
				t.Fatalf("findSorted reports exists but tags[%d]=%q != %q", idx, tags[idx], newTag)
			}
			return
		}

		// Insert and verify sort order is preserved.
		inserted := slices.Insert(tags, idx, newTag)
		if !slices.IsSortedFunc(inserted, compareTags) {
			t.Fatalf("slice not sorted after insert at %d: %v", idx, inserted)
		}
	})
}

func formatTag(turn, tool int) string {
	if tool == 0 {
		return strconv.Itoa(turn)
	}
	return strconv.Itoa(turn) + "." + strconv.Itoa(tool)
}
