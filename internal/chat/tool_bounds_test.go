package chat

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// persistedCall stores one tool call and returns it as the store wrote it,
// read back off disk so the assertion is about the RECORD and not about a value
// the test still holds.
func persistedCall(t *testing.T, tc vibekit.ToolCall) vibekit.ToolCall {
	t.Helper()
	s, _ := newTestStore(t)
	if err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = []vibekit.Message{callMessage(tc)}
		return true
	}); err != nil {
		t.Fatalf("Setup: Mutate: %v", err)
	}
	got, ok := s.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("Setup: Get = not found")
	}
	if len(got.Messages) != 1 || len(got.Messages[0].ToolCalls) != 1 {
		t.Fatalf("Setup: want one message with one tool call, got %+v", got.Messages)
	}
	return got.Messages[0].ToolCalls[0]
}

// TestStoreBound_OversizeOutputIsCutWithItsOriginalSizeRecorded is the bound that
// applies whatever the chat cap is, unlimited included: it is what keeps one turn
// from adding an unbounded number of bytes to the record.
//
// The fixture must CONTAIN an output over persistBudget.outputBytes, and the
// assertion must read the value back off DISK — a marker set on an in-memory copy
// would pass while nothing was persisted.
func TestStoreBound_OversizeOutputIsCutWithItsOriginalSizeRecorded(t *testing.T) {
	full := strings.Repeat("o", persistBudget.outputBytes*3)
	got := persistedCall(t, vibekit.ToolCall{
		ID: "tc1", Title: "Execute", Kind: vibekit.ToolKindExecute,
		Status: vibekit.ToolCompleted, Output: full,
		OutputSpans: []vibekit.TextSpan{{Start: 0, End: 4, Attrs: 1}},
	})

	if len(got.Output) >= len(full) {
		t.Errorf("persisted output is %d bytes, want fewer than the %d written", len(got.Output), len(full))
	}
	if len(got.Output) > persistBudget.outputBytes+1 {
		t.Errorf("persisted output is %d bytes, want at most %d", len(got.Output), persistBudget.outputBytes+1)
	}
	if got.Truncated == nil {
		t.Fatal("truncated = nil, want the marker: without it the reader is shown less than happened, silently")
	}
	if got.Truncated.OutputBytes != len(full) {
		t.Errorf("truncated.output_bytes = %d, want %d (the size before the cut)",
			got.Truncated.OutputBytes, len(full))
	}
	if len(got.OutputSpans) != 0 {
		t.Errorf("output_spans = %v, want none: the offsets index the whole output", got.OutputSpans)
	}
	if got.HasFull {
		t.Error("has_full = true on the persisted record; it is the transcript read path's field")
	}
}

// TestStoreBound_OversizeDiffIsDroppedNotTruncated pins the whole-or-nothing rule
// at the persist layer: a ToolDiff is a before/after pair the client line-diffs,
// so a cut pair renders hunks describing an edit nobody made.
//
// The fixture must CONTAIN one diff that fits and one that does not, or a test
// could pass by dropping everything.
func TestStoreBound_OversizeDiffIsDroppedNotTruncated(t *testing.T) {
	half := persistBudget.diffBytes
	diffs := []vibekit.ToolDiff{
		{Path: "small.go", OldText: "a", NewText: "b"},
		{Path: "huge.go", OldText: strings.Repeat("o", half), NewText: strings.Repeat("n", half)},
	}
	got := persistedCall(t, vibekit.ToolCall{
		ID: "tc1", Title: "Edit", Kind: vibekit.ToolKindEdit,
		Status: vibekit.ToolCompleted, Diffs: diffs,
	})

	if len(got.Diffs) != 1 || got.Diffs[0].Path != "small.go" {
		t.Fatalf("persisted diffs = %v, want only the one that fit", got.Diffs)
	}
	if got.Diffs[0].OldText != "a" || got.Diffs[0].NewText != "b" {
		t.Errorf("kept diff = %+v, want it whole", got.Diffs[0])
	}
	if got.Truncated == nil {
		t.Fatal("truncated = nil, want the marker")
	}
	if got.Truncated.DiffCount != 2 {
		t.Errorf("truncated.diff_count = %d, want 2 (the count before the cut)", got.Truncated.DiffCount)
	}
	if want := diffsBytes(diffs); got.Truncated.DiffBytes != want {
		t.Errorf("truncated.diff_bytes = %d, want %d", got.Truncated.DiffBytes, want)
	}
}

// TestStoreBound_OversizeInputKeepsTheClaimLine pins that the bound drops the
// bulky MEMBER, not the object: the card's claim line is built from `path`, so
// dropping the object wholesale would blank the card to save the same bytes.
func TestStoreBound_OversizeInputKeepsTheClaimLine(t *testing.T) {
	in, err := json.Marshal(map[string]any{
		"path": "internal/app/main.go",
		"text": strings.Repeat("L", persistBudget.inputMember*3),
	})
	if err != nil {
		t.Fatalf("Setup: marshal input: %v", err)
	}
	got := persistedCall(t, vibekit.ToolCall{
		ID: "tc1", Title: "Write", Kind: vibekit.ToolKindWrite,
		Status: vibekit.ToolCompleted, Input: in,
	})

	var kept map[string]any
	if uerr := json.Unmarshal(got.Input, &kept); uerr != nil {
		t.Fatalf("persisted input is not an object: %s", got.Input)
	}
	if kept["path"] != "internal/app/main.go" {
		t.Errorf("path = %v, want it kept", kept["path"])
	}
	if _, present := kept["text"]; present {
		t.Error("text survived, want the over-budget member dropped")
	}
	if got.Truncated == nil || got.Truncated.InputBytes != len(in) {
		t.Errorf("truncated = %+v, want InputBytes = %d", got.Truncated, len(in))
	}
}

// TestStoreBound_SmallCallIsPersistedWhole is the case the bound must not touch,
// and the one that makes the three tests above mean something: almost every tool
// call is small, and a bound that cut them would be indistinguishable from a bug.
func TestStoreBound_SmallCallIsPersistedWhole(t *testing.T) {
	in := vibekit.ToolCall{
		ID: "tc1", Title: "Execute", Kind: vibekit.ToolKindExecute,
		Status: vibekit.ToolCompleted, Output: "all done\n",
		Input:       json.RawMessage(`{"command":"ls"}`),
		OutputSpans: []vibekit.TextSpan{{Start: 0, End: 3, Attrs: 1}},
		Diffs:       []vibekit.ToolDiff{{Path: "a.go", OldText: "x", NewText: "y"}},
	}
	got := persistedCall(t, in)

	if got.Truncated != nil {
		t.Errorf("truncated = %+v, want nil for a small call", got.Truncated)
	}
	if got.Output != in.Output || len(got.OutputSpans) != 1 || len(got.Diffs) != 1 {
		t.Errorf("persisted call = %+v, want it byte-identical to what was written", got)
	}
	// Compared after a compaction, because writeChat's MarshalIndent re-indents a
	// nested RawMessage: the bytes differ, the value must not.
	var gotIn, wantIn map[string]any
	if err := json.Unmarshal(got.Input, &gotIn); err != nil {
		t.Fatalf("persisted input is not an object: %s", got.Input)
	}
	if err := json.Unmarshal(in.Input, &wantIn); err != nil {
		t.Fatalf("Setup: fixture input is not an object: %s", in.Input)
	}
	if !maps.Equal(gotIn, wantIn) {
		t.Errorf("input = %v, want %v", gotIn, wantIn)
	}
}

// TestStoreBound_RewriteKeepsTheFirstMeasurement pins idempotence across the
// mutations that follow: every mutation loads the persisted chat and writes it
// back, so an already-cut call passes through the bound again. Reporting the
// second pass's numbers would replace the original size with the truncated one
// and the marker would understate the loss on every later turn.
func TestStoreBound_RewriteKeepsTheFirstMeasurement(t *testing.T) {
	full := strings.Repeat("o", persistBudget.outputBytes*3)
	s, _ := newTestStore(t)
	seed := func() error {
		return s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
			c.Messages = []vibekit.Message{callMessage(vibekit.ToolCall{
				ID: "tc1", Title: "Execute", Kind: vibekit.ToolKindExecute,
				Status: vibekit.ToolCompleted, Output: full,
			})}
			return true
		})
	}
	if err := seed(); err != nil {
		t.Fatalf("Setup: first Mutate: %v", err)
	}
	// A later, unrelated mutation: renaming the chat rewrites the whole file.
	for i := range 3 {
		if err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
			c.Name = strings.Repeat("r", i+1)
			return true
		}); err != nil {
			t.Fatalf("Mutate %d: %v", i, err)
		}
	}

	got, ok := s.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("Get = not found")
	}
	tc := got.Messages[0].ToolCalls[0]
	if tc.Truncated == nil {
		t.Fatal("truncated = nil after a rewrite, want the marker to survive")
	}
	if tc.Truncated.OutputBytes != len(full) {
		t.Errorf("truncated.output_bytes = %d after 3 rewrites, want %d (the FIRST measurement)",
			tc.Truncated.OutputBytes, len(full))
	}
}
