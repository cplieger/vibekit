package buffer

// The mid-turn segment split: what SplitSegment seals, what it leaves standing,
// and when ToolsSettled refuses to let it happen at all.

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// A split hands back everything the sealed segment produced and leaves the buffer
// ready for the rest of the turn, so what the boundary falls between is one
// message each side rather than one message holding both.
func TestSplitSegment_SealsTheSegmentAndClearsThePerMessageFields(t *testing.T) {
	buf := New()
	buf.StartTurn("m-1")
	buf.AppendTextDelta("before", "")
	buf.AppendThinkingDelta("planning", "")
	buf.AppendToolCall(&vibekit.ToolCall{ID: "t-1", Status: vibekit.ToolCompleted})
	buf.AppendToolUseBlock("t-1", "")
	buf.AppendCodeReferences([]vibekit.CodeReference{{LicenseName: "MIT"}})

	snap := buf.SplitSegment()

	if snap.MessageID != "m-1" {
		t.Errorf("sealed segment MessageID = %q, want %q", snap.MessageID, "m-1")
	}
	if snap.Content != "before" {
		t.Errorf("sealed segment Content = %q, want %q", snap.Content, "before")
	}
	if snap.Reasoning != "planning" {
		t.Errorf("sealed segment Reasoning = %q, want %q", snap.Reasoning, "planning")
	}
	// Three blocks: the text, the thinking and the tool use.
	if len(snap.ToolCalls) != 1 || len(snap.Blocks) != 3 || len(snap.CodeReferences) != 1 {
		t.Errorf("sealed segment carried %d tool calls, %d blocks, %d code refs; want 1, 3, 1",
			len(snap.ToolCalls), len(snap.Blocks), len(snap.CodeReferences))
	}
	if !snap.Started || !snap.Segmented {
		t.Errorf("sealed segment Started = %t, Segmented = %t, want both true", snap.Started, snap.Segmented)
	}

	// The rest of the turn accumulates into a fresh message, so nothing the sealed
	// segment already carries may still be here to be persisted twice.
	after := buf.TakeTurn()
	if after.Content != "" || after.Reasoning != "" {
		t.Errorf("after the split Content = %q, Reasoning = %q, want both empty", after.Content, after.Reasoning)
	}
	if len(after.ToolCalls) != 0 || len(after.Blocks) != 0 || len(after.CodeReferences) != 0 {
		t.Errorf("after the split: %d tool calls, %d blocks, %d code refs; want 0, 0, 0",
			len(after.ToolCalls), len(after.Blocks), len(after.CodeReferences))
	}
	if after.MessageID != "" || after.Started {
		t.Errorf("after the split MessageID = %q, Started = %t; want empty and false",
			after.MessageID, after.Started)
	}
	if _, _, ok := buf.ToolCall("t-1"); ok {
		t.Error("the sealed segment's tool call is still resolvable, so an update would fold into the wrong message")
	}
}

// The per-TURN facts survive a split, and each one for its own reason: the footer
// merges ChangedFiles by path so the last write must be the cumulative one, Model
// is latched once for the whole turn, and chunkSeq is what a reconnecting client's
// watermark is compared against.
func TestSplitSegment_KeepsThePerTurnFields(t *testing.T) {
	buf := New()
	buf.StartTurn("m-1")
	buf.SetModel("opus-5")
	buf.SetMuted(true)
	buf.TrackFileChanges([]vibekit.ToolDiff{{Path: "a.go", OldText: "x\n", NewText: "x\ny\n"}}, false)
	_, seqBefore := buf.AppendTextDelta("before", "")

	buf.SplitSegment()

	after := buf.TakeTurn()
	if after.Model != "opus-5" {
		t.Errorf("after the split Model = %q, want the turn's latched %q", after.Model, "opus-5")
	}
	if len(after.ChangedFiles) != 1 || after.ChangedFiles["a.go"] == nil {
		t.Errorf("after the split ChangedFiles = %v, want the turn's cumulative map", after.ChangedFiles)
	}
	if !buf.Muted() {
		t.Error("after the split the buffer is unmuted, so a prime's frames would reach clients")
	}
	if _, seqAfter := buf.AppendTextDelta("after", ""); seqAfter != seqBefore+1 {
		t.Errorf("after the split the next seq = %d, want %d (the watermark needs it monotonic)",
			seqAfter, seqBefore+1)
	}
}

// Segmented is what tells the turn's closer an earlier segment already carries
// part of this turn, so it is false until a split actually happens and reported on
// every snapshot afterwards.
func TestSplitSegment_ReportsSegmentedOnEverySnapshotAfterwards(t *testing.T) {
	buf := New()
	if buf.TakeTurn().Segmented {
		t.Error("a turn that was never split reports Segmented, so its closer would look for a segment that does not exist")
	}
	buf.StartTurn("m-1")
	buf.AppendTextDelta("before", "")
	buf.SplitSegment()
	if !buf.TakeTurn().Segmented {
		t.Error("a split turn does not report Segmented, so its footer numbers reach no carrier")
	}
}

// A turn with nothing to seal is left exactly as it was. A caller declining on
// that answer must not find the turn marked as split, or the closer goes looking
// for a segment nothing persisted.
func TestSplitSegment_ATurnThatEmittedNothingIsUntouched(t *testing.T) {
	buf := New()
	buf.SetModel("opus-5")

	snap := buf.SplitSegment()

	if snap.Started || snap.Segmented {
		t.Errorf("splitting an unstarted turn reported Started = %t, Segmented = %t; want both false",
			snap.Started, snap.Segmented)
	}
	if buf.TakeTurn().Segmented {
		t.Error("splitting an unstarted turn latched Segmented, so its closer would report a split that never happened")
	}
}

// A turn whose message id is minted but which has streamed no delta is left alone
// too, and that is the window Started cannot see: StartTurn sets it before any
// content arrives, so splitting there seals an empty segment AND hands the rest of
// the turn a fresh id while the client streams under the first one.
func TestSplitSegment_AMintedIDWithNoDeltaIsUntouched(t *testing.T) {
	buf := New()
	buf.StartTurn("m-1")

	snap := buf.SplitSegment()

	if !snap.Started {
		t.Fatal("the fixture did not start the turn, so it exercises the unstarted case instead")
	}
	if snap.Segmented {
		t.Error("splitting a delta-less turn reported Segmented, so its caller would persist an empty segment")
	}
	after := buf.TakeTurn()
	if after.Segmented {
		t.Error("splitting a delta-less turn latched Segmented, so its closer would report a split that never happened")
	}
	if !after.Started || after.MessageID != "m-1" {
		t.Errorf("after the refused split Started = %t, MessageID = %q; want true and %q",
			after.Started, after.MessageID, "m-1")
	}
}

// ToolsSettled is the one condition that makes a split unsafe: an update resolves
// its call against the CURRENT buffer, so a call still in flight when the split
// happens can never be written back and its card stays a spinner forever in the
// message already on disk.
func TestToolsSettled(t *testing.T) {
	cases := []struct {
		name   string
		status []vibekit.ToolStatus
		want   bool
	}{
		{name: "no tool calls at all", status: nil, want: true},
		{name: "every call completed", status: []vibekit.ToolStatus{vibekit.ToolCompleted}, want: true},
		{
			name:   "a completed and a failed call are both terminal",
			status: []vibekit.ToolStatus{vibekit.ToolCompleted, vibekit.ToolFailed},
			want:   true,
		},
		{name: "a pending call", status: []vibekit.ToolStatus{vibekit.ToolPending}, want: false},
		{name: "an in-progress call", status: []vibekit.ToolStatus{vibekit.ToolInProgress}, want: false},
		{
			name:   "one unsettled call among settled ones",
			status: []vibekit.ToolStatus{vibekit.ToolCompleted, vibekit.ToolInProgress, vibekit.ToolFailed},
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := New()
			for i, st := range tc.status {
				buf.AppendToolCall(&vibekit.ToolCall{ID: string(rune('a' + i)), Status: st})
			}
			if got := buf.ToolsSettled(); got != tc.want {
				t.Errorf("ToolsSettled() with %v = %t, want %t", tc.status, got, tc.want)
			}
		})
	}
}
