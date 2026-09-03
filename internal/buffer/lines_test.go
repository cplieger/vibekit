package buffer

import (
	"fmt"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestLineTracker_Record_Basic(t *testing.T) {
	lt := NewLineTracker()
	lt.Record("chat1", "main.go", LineRange{StartLine: 1, EndLine: 10, Turn: 1, Kind: "edit"})
	got := lt.Get("chat1", "main.go")
	if len(got) != 1 {
		t.Fatalf("expected 1 range, got %d", len(got))
	}
	if got[0].StartLine != 1 || got[0].EndLine != 10 {
		t.Errorf("range = %+v, want {1,10}", got[0])
	}
}

func TestLineTracker_Record_MultipleFiles(t *testing.T) {
	lt := NewLineTracker()
	lt.Record("chat1", "a.go", LineRange{StartLine: 1, EndLine: 5, Turn: 1, Kind: "edit"})
	lt.Record("chat1", "b.go", LineRange{StartLine: 10, EndLine: 20, Turn: 1, Kind: "edit"})
	if got := lt.Get("chat1", "a.go"); len(got) != 1 {
		t.Errorf("a.go: expected 1 range, got %d", len(got))
	}
	if got := lt.Get("chat1", "b.go"); len(got) != 1 {
		t.Errorf("b.go: expected 1 range, got %d", len(got))
	}
}

func TestLineTracker_Record_MaxRanges(t *testing.T) {
	lt := NewLineTracker()
	for i := range maxLineRangesPerFile + 10 {
		lt.Record("chat1", "file.go", LineRange{StartLine: i, EndLine: i + 1, Turn: 1, Kind: "edit"})
	}
	got := lt.Get("chat1", "file.go")
	if len(got) > maxLineRangesPerFile {
		t.Errorf("ranges = %d, want <= %d", len(got), maxLineRangesPerFile)
	}
}

func TestLineTracker_Record_MaxFiles(t *testing.T) {
	lt := NewLineTracker()
	for i := range maxFilesPerChat + 10 {
		lt.Record("chat1", fmt.Sprintf("file%d.go", i), LineRange{StartLine: 1, EndLine: 2, Turn: i, Kind: "edit"})
	}
	count := 0
	for i := range maxFilesPerChat + 10 {
		if lt.Get("chat1", fmt.Sprintf("file%d.go", i)) != nil {
			count++
		}
	}
	if count > maxFilesPerChat {
		t.Errorf("tracked files = %d, want <= %d", count, maxFilesPerChat)
	}
}

func TestLineTracker_RecordFromDiffs(t *testing.T) {
	lt := NewLineTracker()
	diffs := []vibekit.ToolDiff{
		{Path: "a.go", NewText: "line1\nline2\nline3\n"},
		{Path: "", NewText: "ignored"},
		{Path: "b.go", NewText: ""},
	}
	lt.RecordFromDiffs("chat1", diffs, 1, "edit")
	if got := lt.Get("chat1", "a.go"); got == nil {
		t.Error("a.go: expected ranges, got nil")
	}
	if got := lt.Get("chat1", "b.go"); got != nil {
		t.Errorf("b.go: expected nil (empty NewText), got %v", got)
	}
}

func TestLineTracker_Clear(t *testing.T) {
	lt := NewLineTracker()
	lt.Record("chat1", "file.go", LineRange{StartLine: 1, EndLine: 10, Turn: 1, Kind: "edit"})
	lt.Clear("chat1")
	if got := lt.Get("chat1", "file.go"); got != nil {
		t.Errorf("after Clear, expected nil, got %v", got)
	}
}

func TestLineTracker_Get_UnknownChat(t *testing.T) {
	lt := NewLineTracker()
	if got := lt.Get("nonexistent", "file.go"); got != nil {
		t.Errorf("expected nil for unknown chat, got %v", got)
	}
}

func TestLineTracker_Get_UnknownFile(t *testing.T) {
	lt := NewLineTracker()
	lt.Record("chat1", "known.go", LineRange{StartLine: 1, EndLine: 5, Turn: 1, Kind: "edit"})
	if got := lt.Get("chat1", "unknown.go"); got != nil {
		t.Errorf("expected nil for unknown file, got %v", got)
	}
}

// TestFileHeap_Less verifies the heap orders entries by ascending
// lastTurn and that equal turns compare as not-less (strict <).
func TestFileHeap_Less(t *testing.T) {
	h := fileHeap{
		{path: "a", lastTurn: 1},
		{path: "b", lastTurn: 2},
		{path: "c", lastTurn: 2},
	}
	if !h.Less(0, 1) {
		t.Errorf("Less(0,1) [1 < 2] = false, want true")
	}
	if h.Less(1, 2) {
		t.Errorf("Less(1,2) [2 < 2] = true, want false")
	}
	if h.Less(1, 0) {
		t.Errorf("Less(1,0) [2 < 1] = true, want false")
	}
}

// TestFileHeap_Pop verifies Pop removes the last entry, tombstones its
// heap index to -1, and shrinks the heap by one.
func TestFileHeap_Pop(t *testing.T) {
	e0 := &fileHeapEntry{path: "a", lastTurn: 1, index: 0}
	e1 := &fileHeapEntry{path: "b", lastTurn: 2, index: 1}
	h := fileHeap{e0, e1}
	popped, ok := h.Pop().(*fileHeapEntry)
	if !ok {
		t.Fatalf("Pop() returned %T, want *fileHeapEntry", popped)
	}
	if got, want := popped.index, -1; got != want {
		t.Errorf("popped.index = %d, want %d", got, want)
	}
	if got, want := len(h), 1; got != want {
		t.Fatalf("len(heap) after Pop = %d, want %d", got, want)
	}
	if h[0] != e0 {
		t.Errorf("surviving entry path = %q, want %q", h[0].path, e0.path)
	}
}

// TestLineTracker_RecordFromDiffs_LineCounts verifies the recorded range spans
// the whole new text when there is no old text to diff against — a file
// creation really did change every line — with and without a trailing newline.
func TestLineTracker_RecordFromDiffs_LineCounts(t *testing.T) {
	tests := []struct {
		name    string
		newText string
		wantEnd int
	}{
		{"no trailing newline", "a\nb\nc", 3}, // 2 newlines + 1
		{"trailing newline", "a\nb\nc\n", 3},  // 3 newlines, no +1
		{"single line no newline", "solo", 1}, // 0 newlines + 1
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lt := NewLineTracker()
			lt.RecordFromDiffs("chat", []vibekit.ToolDiff{{Path: "f.go", NewText: tt.newText}}, 5, "edit")
			ranges := lt.Get("chat", "f.go")
			if len(ranges) != 1 {
				t.Fatalf("ranges = %d, want 1", len(ranges))
			}
			if got := ranges[0].StartLine; got != 1 {
				t.Errorf("StartLine = %d, want 1", got)
			}
			if got := ranges[0].EndLine; got != tt.wantEnd {
				t.Errorf("EndLine = %d, want %d", got, tt.wantEnd)
			}
		})
	}
}

// TestLineTracker_RecordFromDiffs_OneLineEdit is the editor gutter's half of the
// whole-file-diff bug: with KAS's whole-file NewText the tracker used to record
// 1..300 for a one-line edit, so the gutter painted an accent dot on every line.
func TestLineTracker_RecordFromDiffs_OneLineEdit(t *testing.T) {
	old := bigFile(300)
	lt := NewLineTracker()
	lt.RecordFromDiffs("chat", []vibekit.ToolDiff{
		{Path: "big.go", OldText: old, NewText: replaceLine(old, 149, "line 149 EDITED")},
	}, 1, "edit")
	ranges := lt.Get("chat", "big.go")
	if len(ranges) != 1 {
		t.Fatalf("ranges = %d (%+v), want 1", len(ranges), ranges)
	}
	if ranges[0].StartLine != 150 || ranges[0].EndLine != 150 {
		t.Errorf("range = %d-%d, want 150-150", ranges[0].StartLine, ranges[0].EndLine)
	}
}

// TestLineTracker_RecordFromDiffs_NoOpWrite pins that a write which changed
// nothing marks nothing: there is no modified line to paint.
func TestLineTracker_RecordFromDiffs_NoOpWrite(t *testing.T) {
	same := "a\nb\nc\n"
	lt := NewLineTracker()
	lt.RecordFromDiffs("chat", []vibekit.ToolDiff{{Path: "f.go", OldText: same, NewText: same}}, 1, "edit")
	if got := lt.Get("chat", "f.go"); got != nil {
		t.Errorf("no-op write recorded %+v, want no ranges", got)
	}
}
