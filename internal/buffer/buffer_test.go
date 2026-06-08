package buffer

import (
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

func TestBlockAccumulators(t *testing.T) {
	t.Run("text deltas extend the trailing text block", func(t *testing.T) {
		buf := &Buffer{}
		i0 := buf.AppendTextDelta("hello ")
		i1 := buf.AppendTextDelta("world")
		if i0 != 0 || i1 != 0 {
			t.Errorf("expected both deltas to land on block 0, got %d / %d", i0, i1)
		}
		if got, want := len(buf.Blocks), 1; got != want {
			t.Fatalf("len(Blocks) = %d, want %d", got, want)
		}
		if got, want := buf.Blocks[0].Type, api.BlockText; got != want {
			t.Errorf("Blocks[0].Type = %q, want %q", got, want)
		}
		if got, want := buf.Blocks[0].Text, "hello world"; got != want {
			t.Errorf("Blocks[0].Text = %q, want %q", got, want)
		}
	})

	t.Run("tool_use breaks a text run into a new block", func(t *testing.T) {
		buf := &Buffer{}
		buf.AppendTextDelta("first")
		buf.AppendToolUseBlock("tc-1")
		idx := buf.AppendTextDelta("second")
		if idx != 2 {
			t.Errorf("text-after-tool block index = %d, want 2", idx)
		}
		if got, want := len(buf.Blocks), 3; got != want {
			t.Fatalf("len(Blocks) = %d, want %d", got, want)
		}
		want := []api.BlockType{api.BlockText, api.BlockToolUse, api.BlockText}
		for i, w := range want {
			if buf.Blocks[i].Type != w {
				t.Errorf("Blocks[%d].Type = %q, want %q", i, buf.Blocks[i].Type, w)
			}
		}
		if got, want := buf.Blocks[1].ToolCallID, "tc-1"; got != want {
			t.Errorf("Blocks[1].ToolCallID = %q, want %q", got, want)
		}
	})

	t.Run("thinking and text don't coalesce", func(t *testing.T) {
		buf := &Buffer{}
		i0 := buf.AppendThinkingDelta("reasoning…")
		i1 := buf.AppendTextDelta("answer.")
		if i0 != 0 || i1 != 1 {
			t.Errorf("indices = %d / %d, want 0 / 1", i0, i1)
		}
		if buf.Blocks[0].Type != api.BlockThinking || buf.Blocks[1].Type != api.BlockText {
			t.Errorf("kinds = %q / %q, want thinking / text", buf.Blocks[0].Type, buf.Blocks[1].Type)
		}
	})

	t.Run("back-to-back tool calls each get their own block", func(t *testing.T) {
		buf := &Buffer{}
		i0 := buf.AppendToolUseBlock("a")
		i1 := buf.AppendToolUseBlock("b")
		if i0 != 0 || i1 != 1 {
			t.Errorf("indices = %d / %d, want 0 / 1", i0, i1)
		}
	})
}

func TestTrackFileChanges(t *testing.T) {
	tests := []struct {
		name      string
		diffs     []api.ToolDiff
		isNewFile bool
		wantFiles int
	}{
		{"empty diffs", nil, false, 0},
		{"single diff", []api.ToolDiff{{Path: "a.go", NewText: "x\ny\n"}}, false, 1},
		{"multiple diffs same file", []api.ToolDiff{
			{Path: "a.go", NewText: "x\n"},
			{Path: "a.go", NewText: "y\n"},
		}, false, 1},
		{"empty path skipped", []api.ToolDiff{{Path: "", NewText: "x\n"}}, false, 0},
		{"isNewFile propagation", []api.ToolDiff{{Path: "new.go", NewText: "x\n"}}, true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &Buffer{}
			buf.TrackFileChanges(tt.diffs, tt.isNewFile)
			if buf.ChangedFiles == nil && tt.wantFiles > 0 {
				t.Fatal("ChangedFiles is nil")
			}
			if got := len(buf.ChangedFiles); got != tt.wantFiles {
				t.Errorf("len(ChangedFiles) = %d, want %d", got, tt.wantFiles)
			}
			if tt.isNewFile && tt.wantFiles > 0 {
				for _, fc := range buf.ChangedFiles {
					if !fc.IsNewFile {
						t.Error("IsNewFile not propagated")
					}
				}
			}
		})
	}
}

func TestMarkCancelledToolsFailed(t *testing.T) {
	tests := []struct {
		name    string
		tools   []api.ToolCall
		wantLen int
	}{
		{"no tool calls", nil, 0},
		{"all completed", []api.ToolCall{
			{Status: api.ToolCompleted},
			{Status: api.ToolCompleted},
		}, 0},
		{"mix of statuses", []api.ToolCall{
			{Status: api.ToolInProgress},
			{Status: api.ToolPending},
			{Status: api.ToolCompleted},
		}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &Buffer{ToolCalls: tt.tools}
			changed := buf.MarkCancelledToolsFailed()
			if len(changed) != tt.wantLen {
				t.Errorf("changed = %d, want %d", len(changed), tt.wantLen)
			}
			for _, tc := range changed {
				if tc.Status != api.ToolFailed {
					t.Errorf("status = %q, want failed", tc.Status)
				}
			}
			// Idempotent: second call returns nil.
			if tt.wantLen > 0 {
				if got := buf.MarkCancelledToolsFailed(); len(got) != 0 {
					t.Errorf("second call returned %d, want 0", len(got))
				}
			}
		})
	}
}

func TestComputeDuration(t *testing.T) {
	buf := &Buffer{}
	// Unknown tool ID returns 0.
	if got := buf.ComputeDuration("unknown"); got != 0 {
		t.Errorf("unknown tool: got %d, want 0", got)
	}
	// Record start, then compute.
	buf.RecordToolStart("tool-1")
	dur := buf.ComputeDuration("tool-1")
	if dur < 0 {
		t.Errorf("duration = %d, want >= 0", dur)
	}
	// Second call returns 0 (entry removed).
	if got := buf.ComputeDuration("tool-1"); got != 0 {
		t.Errorf("second call: got %d, want 0", got)
	}
}

func TestRecordToolStart(t *testing.T) {
	buf := &Buffer{}
	// Nil map is lazily initialized.
	buf.RecordToolStart("tool-1")
	if buf.ToolStartTimes == nil {
		t.Error("ToolStartTimes not initialized")
	}
	if _, ok := buf.ToolStartTimes["tool-1"]; !ok {
		t.Error("tool-1 not recorded")
	}
}
