package buffer

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestBlockAccumulators(t *testing.T) {
	t.Run("text deltas extend the trailing text block", func(t *testing.T) {
		buf := &Buffer{}
		i0, _ := buf.AppendTextDelta("hello ", "")
		i1, _ := buf.AppendTextDelta("world", "")
		if i0 != 0 || i1 != 0 {
			t.Errorf("expected both deltas to land on block 0, got %d / %d", i0, i1)
		}
		if got, want := len(buf.Blocks), 1; got != want {
			t.Fatalf("len(Blocks) = %d, want %d", got, want)
		}
		if got, want := buf.Blocks[0].Type, vibekit.BlockText; got != want {
			t.Errorf("Blocks[0].Type = %q, want %q", got, want)
		}
		if got, want := buf.Blocks[0].Text, "hello world"; got != want {
			t.Errorf("Blocks[0].Text = %q, want %q", got, want)
		}
	})

	t.Run("tool_use breaks a text run into a new block", func(t *testing.T) {
		buf := &Buffer{}
		buf.AppendTextDelta("first", "")
		buf.AppendToolUseBlock("tc-1", "")
		idx, _ := buf.AppendTextDelta("second", "")
		if idx != 2 {
			t.Errorf("text-after-tool block index = %d, want 2", idx)
		}
		if got, want := len(buf.Blocks), 3; got != want {
			t.Fatalf("len(Blocks) = %d, want %d", got, want)
		}
		want := []vibekit.BlockType{vibekit.BlockText, vibekit.BlockToolUse, vibekit.BlockText}
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
		i0, _ := buf.AppendThinkingDelta("reasoning…", "")
		i1, _ := buf.AppendTextDelta("answer.", "")
		if i0 != 0 || i1 != 1 {
			t.Errorf("indices = %d / %d, want 0 / 1", i0, i1)
		}
		if buf.Blocks[0].Type != vibekit.BlockThinking || buf.Blocks[1].Type != vibekit.BlockText {
			t.Errorf("kinds = %q / %q, want thinking / text", buf.Blocks[0].Type, buf.Blocks[1].Type)
		}
	})

	t.Run("back-to-back tool calls each get their own block", func(t *testing.T) {
		buf := &Buffer{}
		i0 := buf.AppendToolUseBlock("a", "")
		i1 := buf.AppendToolUseBlock("b", "")
		if i0 != 0 || i1 != 1 {
			t.Errorf("indices = %d / %d, want 0 / 1", i0, i1)
		}
	})

	t.Run("consecutive thinking deltas coalesce into one block", func(t *testing.T) {
		buf := &Buffer{}
		i0, _ := buf.AppendThinkingDelta("aaa", "")
		i1, _ := buf.AppendThinkingDelta("bbb", "")
		if i0 != 0 || i1 != 0 {
			t.Errorf("expected both deltas to land on block 0, got %d / %d", i0, i1)
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
		buf.AppendTextDelta("answer", "")
		i, _ := buf.AppendThinkingDelta("reasoning", "")
		if i != 1 {
			t.Errorf("thinking-after-text block index = %d, want 1", i)
		}
		if got, want := len(buf.Blocks), 2; got != want {
			t.Fatalf("len(Blocks) = %d, want %d", got, want)
		}
		if got, want := buf.Blocks[1].Type, vibekit.BlockThinking; got != want {
			t.Errorf("Blocks[1].Type = %q, want %q", got, want)
		}
		if got, want := buf.Blocks[1].Thinking, "reasoning"; got != want {
			t.Errorf("Blocks[1].Thinking = %q, want %q", got, want)
		}
	})

	t.Run("same-subtask consecutive text deltas extend one block", func(t *testing.T) {
		buf := &Buffer{}
		i0, _ := buf.AppendTextDelta("sub ", "agent-7")
		i1, _ := buf.AppendTextDelta("text", "agent-7")
		if i0 != 0 || i1 != 0 {
			t.Errorf("expected both deltas to land on block 0, got %d / %d", i0, i1)
		}
		if got, want := len(buf.Blocks), 1; got != want {
			t.Fatalf("len(Blocks) = %d, want %d", got, want)
		}
		if got, want := buf.Blocks[0].Text, "sub text"; got != want {
			t.Errorf("Blocks[0].Text = %q, want %q", got, want)
		}
		if got, want := buf.Blocks[0].AgentSubtaskID, "agent-7"; got != want {
			t.Errorf("Blocks[0].AgentSubtaskID = %q, want %q", got, want)
		}
	})

	t.Run("a differing subtask starts a new block", func(t *testing.T) {
		buf := &Buffer{}
		// Top-level text, then a subagent's text: must NOT merge into the
		// parent's trailing block even though both are BlockText.
		i0, _ := buf.AppendTextDelta("parent", "")
		i1, _ := buf.AppendTextDelta("child", "agent-7")
		if i0 != 0 || i1 != 1 {
			t.Errorf("text indices = %d / %d, want 0 / 1", i0, i1)
		}
		if buf.Blocks[0].AgentSubtaskID != "" {
			t.Errorf("Blocks[0].AgentSubtaskID = %q, want empty (top-level)", buf.Blocks[0].AgentSubtaskID)
		}
		if got, want := buf.Blocks[0].Text, "parent"; got != want {
			t.Errorf("Blocks[0].Text = %q, want %q (child text must not merge in)", got, want)
		}
		if got, want := buf.Blocks[1].AgentSubtaskID, "agent-7"; got != want {
			t.Errorf("Blocks[1].AgentSubtaskID = %q, want %q", got, want)
		}
		// Same guard for the thinking fast-path.
		j0, _ := buf.AppendThinkingDelta("p-think", "agent-7")
		j1, _ := buf.AppendThinkingDelta("c-think", "agent-8")
		if j0 != 2 || j1 != 3 {
			t.Errorf("thinking indices = %d / %d, want 2 / 3", j0, j1)
		}
		if got, want := buf.Blocks[3].AgentSubtaskID, "agent-8"; got != want {
			t.Errorf("Blocks[3].AgentSubtaskID = %q, want %q", got, want)
		}
	})

	t.Run("tool_use block stamps the subtask id", func(t *testing.T) {
		buf := &Buffer{}
		idx := buf.AppendToolUseBlock("tc-42", "agent-9")
		if idx != 0 {
			t.Fatalf("block index = %d, want 0", idx)
		}
		if got, want := buf.Blocks[0].AgentSubtaskID, "agent-9"; got != want {
			t.Errorf("Blocks[0].AgentSubtaskID = %q, want %q", got, want)
		}
		if got, want := buf.Blocks[0].ToolCallID, "tc-42"; got != want {
			t.Errorf("Blocks[0].ToolCallID = %q, want %q", got, want)
		}
	})
}

func TestTrackFileChanges(t *testing.T) {
	tests := []struct {
		name      string
		diffs     []vibekit.ToolDiff
		isNewFile bool
		wantFiles int
	}{
		{"empty diffs", nil, false, 0},
		{"single diff", []vibekit.ToolDiff{{Path: "a.go", NewText: "x\ny\n"}}, false, 1},
		{"multiple diffs same file", []vibekit.ToolDiff{
			{Path: "a.go", NewText: "x\n"},
			{Path: "a.go", NewText: "y\n"},
		}, false, 1},
		{"empty path skipped", []vibekit.ToolDiff{{Path: "", NewText: "x\n"}}, false, 0},
		{"isNewFile propagation", []vibekit.ToolDiff{{Path: "new.go", NewText: "x\n"}}, true, 1},
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
		tools   []vibekit.ToolCall
		wantLen int
	}{
		{"no tool calls", nil, 0},
		{"all completed", []vibekit.ToolCall{
			{Status: vibekit.ToolCompleted},
			{Status: vibekit.ToolCompleted},
		}, 0},
		{"mix of statuses", []vibekit.ToolCall{
			{Status: vibekit.ToolInProgress},
			{Status: vibekit.ToolPending},
			{Status: vibekit.ToolCompleted},
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
				if tc.Status != vibekit.ToolFailed {
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
	// Elapsed must be a small non-negative value. A `now + start` defect
	// would yield roughly twice the epoch-milli count (trillions of ms).
	if dur < 0 || dur > 60_000 {
		t.Errorf("duration = %d, want within [0, 60000] ms", dur)
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

// TestTrackFileChanges_LineCounts verifies per-file added/removed line
// counts derive from the newline counts in the new and old text.
func TestTrackFileChanges_LineCounts(t *testing.T) {
	buf := &Buffer{}
	buf.TrackFileChanges([]vibekit.ToolDiff{
		{Path: "f.go", NewText: "a\nb\nc", OldText: "x\ny\nz\nw"},
	}, false)
	fc := buf.ChangedFiles["f.go"]
	if fc == nil {
		t.Fatal(`ChangedFiles["f.go"] is nil`)
	}
	// "a\nb\nc" has 2 newlines; "x\ny\nz\nw" has 3.
	if got, want := fc.LinesAdded, 2; got != want {
		t.Errorf("LinesAdded = %d, want %d", got, want)
	}
	if got, want := fc.LinesRemoved, 3; got != want {
		t.Errorf("LinesRemoved = %d, want %d", got, want)
	}
}

// TestAppendDelta_SeqMonotonic pins the chunk-sequence contract the
// connect-time turn_state watermark depends on: every text/thinking
// delta gets a strictly increasing, 1-based seq, shared across both
// kinds (the watermark orders ALL deltas of a turn, not per-kind).
func TestAppendDelta_SeqMonotonic(t *testing.T) {
	buf := &Buffer{}
	_, s1 := buf.AppendTextDelta("a", "")
	_, s2 := buf.AppendThinkingDelta("b", "")
	_, s3 := buf.AppendTextDelta("c", "sub-1")
	if s1 != 1 || s2 != 2 || s3 != 3 {
		t.Errorf("seqs = %d,%d,%d, want 1,2,3", s1, s2, s3)
	}
}

// TestSetModel_LatchesFirstWrite pins the per-turn model attribution.
//
// FIRST WRITE WINS, deliberately: the value is latched when the turn opens so a
// mid-turn switch cannot rewrite the attribution of work already done under the
// previous model. And an empty model is IGNORED rather than stamped, so a turn
// whose model could not be resolved carries no field at all — the client renders
// nothing for that, where a blank string would read as an attributed turn.
func TestSetModel_LatchesFirstWrite(t *testing.T) {
	cases := []struct {
		name  string
		write []string
		want  string
	}{
		{name: "no write leaves it empty", write: nil, want: ""},
		{name: "one write lands", write: []string{"sonnet-4"}, want: "sonnet-4"},
		{
			name:  "a second write does not overwrite the first",
			write: []string{"sonnet-4", "opus-4"},
			want:  "sonnet-4",
		},
		{name: "an empty write is ignored", write: []string{""}, want: ""},
		{
			name:  "an empty write does not block a later real one",
			write: []string{"", "opus-4"},
			want:  "opus-4",
		},
		{
			name:  "an empty write cannot clear a latched value",
			write: []string{"opus-4", ""},
			want:  "opus-4",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := &Buffer{}
			for _, m := range tc.write {
				buf.SetModel(m)
			}
			if buf.Model != tc.want {
				t.Errorf("Model = %q, want %q", buf.Model, tc.want)
			}
		})
	}
}

// TestAppendToolCall_IndexAddressesTheAppendedCall pins the index
// AppendToolCall hands back and stores under the call's id.
//
// That index is the whole read-modify-write cycle the dispatch loop runs: it
// appends, later reads the call back by id, folds an update into the copy and
// writes it back at the index. Both the returned index and the stored one have to
// address the call that was just appended — an index off the end makes the call
// unfindable, and SetToolCall's own bounds guard then drops the write silently,
// so a tool call would sit in the transcript never progressing past "started".
func TestAppendToolCall_IndexAddressesTheAppendedCall(t *testing.T) {
	buf := &Buffer{}
	first := buf.AppendToolCall(&vibekit.ToolCall{ID: "tool-1", Title: "Read File"})
	second := buf.AppendToolCall(&vibekit.ToolCall{ID: "tool-2", Title: "Write File"})
	if first != 0 || second != 1 {
		t.Fatalf("AppendToolCall() returned %d then %d, want 0 then 1", first, second)
	}

	got, idx, ok := buf.ToolCall("tool-2")
	if !ok {
		t.Fatalf("ToolCall(%q) not found after AppendToolCall returned index %d", "tool-2", second)
	}
	if idx != second {
		t.Errorf("ToolCall(%q) index = %d, want the %d AppendToolCall returned", "tool-2", idx, second)
	}
	if got.Title != "Write File" {
		t.Errorf("ToolCall(%q) title = %q, want %q", "tool-2", got.Title, "Write File")
	}

	// And the index is writable, which is what the fold does with it.
	got.Title = "Write File (done)"
	buf.SetToolCall(idx, &got)
	if buf.ToolCalls[second].Title != "Write File (done)" {
		t.Errorf("ToolCalls[%d].Title = %q, want the folded update %q",
			second, buf.ToolCalls[second].Title, "Write File (done)")
	}
}
