package buffer

import (
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// Test_gk_vibekit_u24_AppendThinkingDelta covers buffer.go:73 (the
// last-block-is-thinking comparison) and buffer.go:75 (the coalesced
// return index n-1).
func Test_gk_vibekit_u24_AppendThinkingDelta(t *testing.T) {
	t.Run("consecutive thinking deltas coalesce into block 0", func(t *testing.T) {
		buf := &Buffer{}
		i0 := buf.AppendThinkingDelta("aaa")
		i1 := buf.AppendThinkingDelta("bbb")
		// 73:57 CONDITIONALS_NEGATION (== -> !=): under != the 2nd delta would
		//   not match the existing thinking block and append a new one (i1=1,
		//   len=2).
		// 75:12 ARITHMETIC_BASE / INVERT_NEGATIVES (n-1 -> n+1): the coalesced
		//   return is n-1 = 0, not 2.
		if i0 != 0 {
			t.Errorf("AppendThinkingDelta(first) = %d, want 0", i0)
		}
		if i1 != 0 {
			t.Errorf("AppendThinkingDelta(second) = %d, want 0", i1)
		}
		if got, want := len(buf.Blocks), 1; got != want {
			t.Fatalf("len(Blocks) = %d, want %d", got, want)
		}
		if got, want := buf.Blocks[0].Thinking, "aaabbb"; got != want {
			t.Errorf("Blocks[0].Thinking = %q, want %q", got, want)
		}
	})

	t.Run("thinking after a non-thinking block starts a new block", func(t *testing.T) {
		buf := &Buffer{}
		buf.AppendTextDelta("answer")
		i := buf.AppendThinkingDelta("reasoning")
		// 73:57 CONDITIONALS_NEGATION false-branch: a text trailing block must
		// NOT coalesce with a thinking delta, so a new block at index 1 forms.
		if i != 1 {
			t.Errorf("AppendThinkingDelta after text = %d, want 1", i)
		}
		if got, want := len(buf.Blocks), 2; got != want {
			t.Fatalf("len(Blocks) = %d, want %d", got, want)
		}
		if got, want := buf.Blocks[1].Type, api.BlockThinking; got != want {
			t.Errorf("Blocks[1].Type = %q, want %q", got, want)
		}
		if got, want := buf.Blocks[1].Thinking, "reasoning"; got != want {
			t.Errorf("Blocks[1].Thinking = %q, want %q", got, want)
		}
	})
}

// Test_gk_vibekit_u24_TrackFileChanges_lineCounts covers buffer.go:108
// (NewText != "") and buffer.go:111 (OldText != "").
func Test_gk_vibekit_u24_TrackFileChanges_lineCounts(t *testing.T) {
	buf := &Buffer{}
	buf.TrackFileChanges([]api.ToolDiff{
		{Path: "f.go", NewText: "a\nb\nc", OldText: "x\ny\nz\nw"},
	}, false)
	fc := buf.ChangedFiles["f.go"]
	if fc == nil {
		t.Fatal("ChangedFiles[\"f.go\"] is nil")
	}
	// 108:16 CONDITIONALS_NEGATION (!= -> ==): under == the non-empty NewText
	// branch never runs and LinesAdded stays 0. "a\nb\nc" has 2 newlines.
	if got, want := fc.LinesAdded, 2; got != want {
		t.Errorf("LinesAdded = %d, want %d", got, want)
	}
	// 111:16 CONDITIONALS_NEGATION (!= -> ==): under == LinesRemoved stays 0.
	// "x\ny\nz\nw" has 3 newlines.
	if got, want := fc.LinesRemoved, 3; got != want {
		t.Errorf("LinesRemoved = %d, want %d", got, want)
	}
}

// Test_gk_vibekit_u24_ComputeDuration_subtractsStart covers buffer.go:138
// (now - start). The existing TestComputeDuration only asserts dur >= 0,
// which the mutated `now + start` also satisfies; this bounds the value.
func Test_gk_vibekit_u24_ComputeDuration_subtractsStart(t *testing.T) {
	buf := &Buffer{ToolStartTimes: map[string]int64{}}
	start := time.Now().UnixMilli()
	buf.ToolStartTimes["t"] = start
	dur := buf.ComputeDuration("t")
	// Original computes (now - start): a small non-negative elapsed value.
	// 138:36 ARITHMETIC_BASE / INVERT_NEGATIVES (- -> +): (now + start) is
	// ~2x the epoch-milli count (trillions), far outside this bound.
	if dur < 0 || dur > 60_000 {
		t.Errorf("ComputeDuration elapsed = %d, want within [0, 60000] ms", dur)
	}
}

// Test_gk_vibekit_u24_fileHeapLess covers lines.go:36
// (h[i].lastTurn < h[j].lastTurn).
func Test_gk_vibekit_u24_fileHeapLess(t *testing.T) {
	h := fileHeap{
		{path: "a", lastTurn: 1},
		{path: "b", lastTurn: 2},
		{path: "c", lastTurn: 2},
	}
	if !h.Less(0, 1) {
		t.Errorf("Less(0,1) [1 < 2] = false, want true")
	}
	// Equal lastTurn: strict < is false.
	// 36:62 CONDITIONALS_BOUNDARY (< -> <=): <= would return true.
	// 36:62 CONDITIONALS_NEGATION (< -> >=): >= would return true.
	if h.Less(1, 2) {
		t.Errorf("Less(1,2) [2 < 2] = true, want false")
	}
	if h.Less(1, 0) {
		t.Errorf("Less(1,0) [2 < 1] = true, want false")
	}
}

// Test_gk_vibekit_u24_fileHeapPop covers lines.go:44 (index = -1) and
// lines.go:45 (old[:n-1]).
func Test_gk_vibekit_u24_fileHeapPop(t *testing.T) {
	e0 := &fileHeapEntry{path: "a", lastTurn: 1, index: 0}
	e1 := &fileHeapEntry{path: "b", lastTurn: 2, index: 1}
	h := fileHeap{e0, e1}
	popped, ok := h.Pop().(*fileHeapEntry)
	if !ok {
		t.Fatalf("Pop() returned %T, want *fileHeapEntry", popped)
	}
	// Pop tombstones the removed entry's heap index.
	// 44:12 ARITHMETIC_BASE / INVERT_NEGATIVES (-1 -> 1): index must be -1.
	if got, want := popped.index, -1; got != want {
		t.Errorf("popped.index = %d, want %d", got, want)
	}
	// 45:13 ARITHMETIC_BASE / INVERT_NEGATIVES (old[:n-1] -> old[:n+1]): the
	// slice shrinks by one to the single un-popped entry (n+1 grows past cap).
	if got, want := len(h), 1; got != want {
		t.Fatalf("len(heap) after Pop = %d, want %d", got, want)
	}
	if h[0] != e0 {
		t.Errorf("surviving entry path = %q, want %q", h[0].path, e0.path)
	}
}

// Test_gk_vibekit_u24_RecordFromDiffs_endLine covers lines.go:116 (lines == 0).
func Test_gk_vibekit_u24_RecordFromDiffs_endLine(t *testing.T) {
	lt := NewLineTracker()
	// "a\nb\nc": 2 newlines, no trailing newline -> lines = 2 + 1 = 3.
	lt.RecordFromDiffs("chat-u24", []api.ToolDiff{{Path: "f.go", NewText: "a\nb\nc"}}, 5, "edit")
	ranges := lt.Get("chat-u24", "f.go")
	if len(ranges) != 1 {
		t.Fatalf("ranges = %d, want 1", len(ranges))
	}
	// lines is always >= 1 for non-empty NewText, so the original `== 0` branch
	// never fires and the computed 3 is kept.
	// 116:12 CONDITIONALS_NEGATION (== -> !=): `!= 0` always fires and clamps
	// lines to 1, so EndLine would be 1.
	if got, want := ranges[0].EndLine, 3; got != want {
		t.Errorf("RecordFromDiffs EndLine = %d, want %d", got, want)
	}
	if got, want := ranges[0].StartLine, 1; got != want {
		t.Errorf("RecordFromDiffs StartLine = %d, want %d", got, want)
	}
}
