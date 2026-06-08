package buffer

import (
	"fmt"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

func TestLineTracker_Record_Basic(t *testing.T) {
	lt := NewLineTracker()
	lt.Record("chat1", "main.go", 1, 10, 1, "edit")
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
	lt.Record("chat1", "a.go", 1, 5, 1, "edit")
	lt.Record("chat1", "b.go", 10, 20, 1, "edit")
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
		lt.Record("chat1", "file.go", i, i+1, 1, "edit")
	}
	got := lt.Get("chat1", "file.go")
	if len(got) > maxLineRangesPerFile {
		t.Errorf("ranges = %d, want <= %d", len(got), maxLineRangesPerFile)
	}
}

func TestLineTracker_Record_MaxFiles(t *testing.T) {
	lt := NewLineTracker()
	for i := range maxFilesPerChat + 10 {
		lt.Record("chat1", fmt.Sprintf("file%d.go", i), 1, 2, i, "edit")
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
	diffs := []api.ToolDiff{
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
	lt.Record("chat1", "file.go", 1, 10, 1, "edit")
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
	lt.Record("chat1", "known.go", 1, 5, 1, "edit")
	if got := lt.Get("chat1", "unknown.go"); got != nil {
		t.Errorf("expected nil for unknown file, got %v", got)
	}
}
