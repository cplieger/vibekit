package buffer

import (
	"testing"

	"vibekit/internal/api"
)

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
