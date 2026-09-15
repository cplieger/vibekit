package buffer

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// splitVersion parses "<id>:<rev>" and fails the test on any other shape.
func splitVersion(t *testing.T, v string) (id, rev uint64) {
	t.Helper()
	idText, revText, ok := strings.Cut(v, ":")
	if !ok {
		t.Fatalf("version %q is not <id>:<rev>", v)
	}
	var err error
	if id, err = strconv.ParseUint(idText, 10, 64); err != nil {
		t.Fatalf("version %q: id is not a uint64: %v", v, err)
	}
	if rev, err = strconv.ParseUint(revText, 10, 64); err != nil {
		t.Fatalf("version %q: rev is not a uint64: %v", v, err)
	}
	return id, rev
}

func TestNew_MintsDistinctIDs(t *testing.T) {
	a, b := New(), New()
	idA, _ := splitVersion(t, a.Version())
	idB, _ := splitVersion(t, b.Version())
	if idA == idB {
		t.Fatalf("two New() buffers share id %d", idA)
	}
	if idA == 0 || idB == 0 {
		t.Fatalf("New() minted id 0 (%d, %d); zero is the composite literal's value", idA, idB)
	}
}

// TestMutators_ReturnStrictlyIncreasingVersionsOnOneID walks every mutator and
// checks the version it returns: same id throughout, rev strictly increasing,
// and the buffer's own Version() agreeing with the last return.
func TestMutators_ReturnStrictlyIncreasingVersionsOnOneID(t *testing.T) {
	buf := New()
	wantID, prevRev := splitVersion(t, buf.Version())
	if prevRev != 0 {
		t.Fatalf("fresh buffer rev = %d, want 0", prevRev)
	}
	call := vibekit.ToolCall{ID: "tc1", Status: vibekit.ToolInProgress}
	mutators := []struct {
		name string
		do   func() string
	}{
		{"StartTurn", func() string { _, v := buf.StartTurn("m1"); return v }},
		{"SetModel", func() string { return buf.SetModel("claude") }},
		{"SetRefusal", func() string { return buf.SetRefusal(&vibekit.RefusalInfo{Category: "x"}) }},
		{"AppendTextDelta", func() string { _, _, v := buf.AppendTextDelta("hi", ""); return v }},
		{"AppendThinkingDelta", func() string { _, _, v := buf.AppendThinkingDelta("hm", ""); return v }},
		{"AppendToolCall", func() string { _, v := buf.AppendToolCall(&call); return v }},
		{"AppendToolUseBlock", func() string { _, v := buf.AppendToolUseBlock("tc1", ""); return v }},
		{"RecordToolStart", func() string { return buf.RecordToolStart("tc1") }},
		{"SetToolCall", func() string { return buf.SetToolCall(0, &call) }},
		{"SetSteerCarry", func() string { return buf.SetSteerCarry("[", "") }},
		{"AppendCodeReferences", func() string {
			_, v := buf.AppendCodeReferences([]vibekit.CodeReference{{URL: "u"}})
			return v
		}},
		{"TrackFileChanges", func() string {
			return buf.TrackFileChanges([]vibekit.ToolDiff{{Path: "a.go", OldText: "a\n", NewText: "b\n"}}, false)
		}},
		{"ComputeDuration", func() string { _, v := buf.ComputeDuration("tc1"); return v }},
		{"MarkOverCap", func() string { _, v := buf.MarkOverCap(); return v }},
		{"MarkInFlightToolsAborted", func() string { _, _, v := buf.MarkInFlightToolsAborted(); return v }},
		{"SplitSegment", func() string { _, v := buf.SplitSegment(); return v }},
	}
	for _, m := range mutators {
		got := m.do()
		id, rev := splitVersion(t, got)
		if id != wantID {
			t.Errorf("%s returned id %d, want the buffer's %d", m.name, id, wantID)
		}
		if rev <= prevRev {
			t.Errorf("%s returned rev %d after %d, want strictly increasing", m.name, rev, prevRev)
		}
		if cur := buf.Version(); cur != got {
			t.Errorf("after %s, Version() = %q, want the mutator's return %q", m.name, cur, got)
		}
		prevRev = rev
	}
}

func TestReaders_MoveNothing(t *testing.T) {
	buf := New()
	buf.StartTurn("m1")
	call := vibekit.ToolCall{ID: "tc1", Status: vibekit.ToolInProgress}
	buf.AppendToolCall(&call)
	buf.AppendTextDelta("text", "")
	before := buf.Version()
	buf.BufferedBytes()
	buf.TakeTurn()
	buf.ToolsSettled()
	buf.ToolCall("tc1")
	buf.ToolCallCount()
	buf.SteerCarry()
	buf.HasModel()
	buf.HasToolInFlightSince(time.Now().Add(-time.Hour))
	buf.SnapshotCapped(SnapshotCaps{})
	buf.Version()
	if after := buf.Version(); after != before {
		t.Fatalf("readers moved the version %q -> %q", before, after)
	}
}

// TestTrackFileChanges_NoDeltaReturnsTheCurrentVersion pins the early return: an
// emitter that stamps from it must stamp a real value, and nothing was written, so
// the value is the current one rather than a new mint.
func TestTrackFileChanges_NoDeltaReturnsTheCurrentVersion(t *testing.T) {
	buf := New()
	before := buf.Version()
	got := buf.TrackFileChanges([]vibekit.ToolDiff{{Path: ""}}, false)
	if got != before {
		t.Errorf("TrackFileChanges with no delta returned %q, want the current version %q", got, before)
	}
	if got == "" {
		t.Error("TrackFileChanges with no delta returned an empty version")
	}
}

// TestSplitSegment_KeepsTheIDOnAStillLiveTurn is the R3-H2 case: a split clears
// MessageID on a turn that is still live, so a version keyed on the message would
// collapse across the split. The buffer id survives it.
func TestSplitSegment_KeepsTheIDOnAStillLiveTurn(t *testing.T) {
	buf := New()
	buf.StartTurn("m1")
	_, _, before := buf.AppendTextDelta("first segment", "")
	idBefore, revBefore := splitVersion(t, before)
	snap, after := buf.SplitSegment()
	if !snap.Segmented {
		t.Fatal("SplitSegment on a turn with content did not report Segmented")
	}
	idAfter, revAfter := splitVersion(t, after)
	if idAfter != idBefore {
		t.Errorf("SplitSegment changed the id %d -> %d", idBefore, idAfter)
	}
	if revAfter <= revBefore {
		t.Errorf("SplitSegment rev %d did not advance past %d", revAfter, revBefore)
	}
	if got := buf.TakeTurn().MessageID; got != "" {
		t.Errorf("post-split MessageID = %q, want empty (the id is not the message's)", got)
	}
	_, _, next := buf.AppendTextDelta("second segment", "")
	if next == after {
		t.Errorf("a write after the split returned the split's own version %q", next)
	}
}
