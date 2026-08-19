package hub

import (
	"fmt"
	"testing"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestLineTrackerRecord(t *testing.T) {
	lt := buffer.NewLineTracker()
	lt.Record("chat1", "main.go", buffer.LineRange{StartLine: 1, EndLine: 10, Turn: 1, Kind: "edit"})
	lt.Record("chat1", "main.go", buffer.LineRange{StartLine: 15, EndLine: 20, Turn: 2, Kind: "edit"})
	lt.Record("chat1", "util.go", buffer.LineRange{StartLine: 5, EndLine: 8, Turn: 1, Kind: "create"})

	got := lt.Get("chat1", "main.go")
	if len(got) != 2 {
		t.Fatalf("expected 2 ranges for main.go, got %d", len(got))
	}
	if got[0].StartLine != 1 || got[0].EndLine != 10 {
		t.Errorf("range 0: got %d-%d, want 1-10", got[0].StartLine, got[0].EndLine)
	}
	if got[1].Turn != 2 {
		t.Errorf("range 1 turn: got %d, want 2", got[1].Turn)
	}

	got2 := lt.Get("chat1", "util.go")
	if len(got2) != 1 || got2[0].Kind != "create" {
		t.Errorf("util.go: expected 1 create range, got %v", got2)
	}

	// Unknown file returns nil.
	if lt.Get("chat1", "nope.go") != nil {
		t.Error("expected nil for unknown file")
	}
	// Unknown chat returns nil.
	if lt.Get("chat2", "main.go") != nil {
		t.Error("expected nil for unknown chat")
	}
}

func TestLineTrackerRecordFromDiffs(t *testing.T) {
	lt := buffer.NewLineTracker()
	diffs := []vibekit.ToolDiff{
		{Path: "a.go", NewText: "line1\nline2\nline3\n"},
		{Path: "b.go", NewText: "single"},
		{Path: "", NewText: "skip"}, // empty path skipped
		{Path: "c.go", NewText: ""}, // empty new text skipped
	}
	lt.RecordFromDiffs("c1", diffs, 1, "edit")

	a := lt.Get("c1", "a.go")
	if len(a) != 1 || a[0].EndLine != 3 {
		t.Errorf("a.go: expected 1 range ending at 3, got %v", a)
	}
	b := lt.Get("c1", "b.go")
	if len(b) != 1 || b[0].EndLine != 1 {
		t.Errorf("b.go: expected 1 range ending at 1, got %v", b)
	}
	if lt.Get("c1", "c.go") != nil {
		t.Error("c.go should be nil (empty new text)")
	}
}

func TestLineTrackerClear(t *testing.T) {
	lt := buffer.NewLineTracker()
	lt.Record("c1", "f.go", buffer.LineRange{StartLine: 1, EndLine: 5, Turn: 1, Kind: "edit"})
	lt.Clear("c1")
	if lt.Get("c1", "f.go") != nil {
		t.Error("expected nil after clear")
	}
}

func TestLineTrackerGet(t *testing.T) {
	lt := buffer.NewLineTracker()
	lt.Record("c1", "main.go", buffer.LineRange{StartLine: 1, EndLine: 10, Turn: 1, Kind: "edit"})

	// Missing path returns nil.
	if got := lt.Get("c1", ""); got != nil {
		t.Errorf("expected nil for empty path, got %v", got)
	}

	// Valid path returns data.
	got := lt.Get("c1", "main.go")
	if len(got) != 1 {
		t.Fatalf("expected 1 range, got %d", len(got))
	}
	if got[0].StartLine != 1 || got[0].EndLine != 10 {
		t.Errorf("got %d-%d, want 1-10", got[0].StartLine, got[0].EndLine)
	}

	// Unknown chat returns nil.
	if lt.Get("c2", "main.go") != nil {
		t.Error("expected nil for unknown chat")
	}
}

func BenchmarkLineTrackerRecord(b *testing.B) {
	for _, n := range []int{1, 50, 200} {
		b.Run(fmt.Sprintf("existing_%d", n), func(b *testing.B) {
			lt := buffer.NewLineTracker()
			// Pre-fill with n ranges.
			for i := range n {
				lt.Record("c1", "main.go", buffer.LineRange{StartLine: i * 10, EndLine: i*10 + 5, Turn: i, Kind: "edit"})
			}
			b.ResetTimer()
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				lt.Record("c1", "main.go", buffer.LineRange{StartLine: i*10 + 5000, EndLine: i*10 + 5005, Turn: i, Kind: "edit"})
				i++
			}
		})
	}
}

func BenchmarkLineTrackerRecordFromDiffs(b *testing.B) {
	for _, n := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("diffs_%d", n), func(b *testing.B) {
			lt := buffer.NewLineTracker()
			diffs := make([]vibekit.ToolDiff, n)
			for i := range n {
				diffs[i] = vibekit.ToolDiff{
					Path:    fmt.Sprintf("file%d.go", i),
					NewText: "line1\nline2\nline3\n",
				}
			}
			b.ResetTimer()
			b.ReportAllocs()
			for b.Loop() {
				lt.RecordFromDiffs("c1", diffs, 1, "edit")
			}
		})
	}
}
