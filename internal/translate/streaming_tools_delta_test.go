package translate

import (
	"reflect"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestToolCallDelta_SendsEachOutputChunkOnce is the measurement this reshape
// exists for: the old frame carried the whole accumulated ToolCall, so a call's
// output and diffs were re-sent in full on every later frame — 4.41 MiB of
// output and 5.73 MiB of diffs behind five open tabs, p99 frame 122 KB.
func TestToolCallDelta_SendsEachOutputChunkOnce(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)

	for _, chunk := range []string{"first\n", "second\n", "third\n"} {
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "in_progress",
			"content": []map[string]any{
				{"type": "content", "content": map[string]any{"text": chunk}},
			},
		}), FrameAttribution{})
	}

	deltas := toolCallDeltas(t, events)
	if len(deltas) != 3 {
		t.Fatalf("got %d tool_call_update frames, want 3", len(deltas))
	}
	// parseToolUpdateContent appends a newline per content block, so each frame's
	// delta is its own chunk plus one.
	for i, want := range []string{"first\n\n", "second\n\n", "third\n\n"} {
		if deltas[i].OutputDelta != want {
			t.Errorf("frame %d output_delta = %q, want %q — a delta must not restate what earlier frames delivered",
				i, deltas[i].OutputDelta, want)
		}
		if deltas[i].OutputReplace {
			t.Errorf("frame %d set output_replace on a plain append", i)
		}
	}
	// And the fold still reproduces the whole object, which lastToolCallUpdate
	// cross-checks against the buffer.
	got, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_call_update event emitted")
	}
	if got.Output != "first\n\nsecond\n\nthird\n\n" {
		t.Errorf("folded output = %q, want every chunk in order", got.Output)
	}
}

// TestToolCallDelta_SendsEachDiffOnce is the diff half of the same measurement:
// one Replace-in-File's 184 KB of diffs used to ride every later frame for that
// call.
func TestToolCallDelta_SendsEachDiffOnce(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)

	for _, path := range []string{"a.go", "b.go"} {
		tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
			"toolCallId": "tc-1",
			"status":     "in_progress",
			"content": []map[string]any{
				{"type": "diff", "path": path, "oldText": "x", "newText": "y"},
			},
		}), FrameAttribution{})
	}

	deltas := toolCallDeltas(t, events)
	if len(deltas) != 2 {
		t.Fatalf("got %d tool_call_update frames, want 2", len(deltas))
	}
	for i, want := range []string{"a.go", "b.go"} {
		if len(deltas[i].DiffsAppended) != 1 || deltas[i].DiffsAppended[0].Path != want {
			t.Errorf("frame %d diffs_appended = %+v, want exactly the one new %q",
				i, deltas[i].DiffsAppended, want)
		}
	}
	got, _ := lastToolCallUpdate(t, deps, events)
	if len(got.Diffs) != 2 {
		t.Errorf("folded diffs = %d, want 2 — the card keeps every arrival", len(got.Diffs))
	}
}

// TestToolCallDelta_NeverCarriesTheInput is 1.49 MiB of the measured total. An
// update cannot change the input, so it has no field for it.
func TestToolCallDelta_NeverCarriesTheInput(t *testing.T) {
	fields := reflect.VisibleFields(reflect.TypeFor[vibekit.ToolCallUpdatePayload]())
	for _, f := range fields {
		if f.Name == "Input" {
			t.Error("ToolCallUpdatePayload has an Input field; an update never changes it")
		}
	}
}

// TestToolCallDelta_ReplacesWhenTheTerminalOutputWins is the one rule a
// pure-append wire cannot express.
//
// At completion adoptTerminalOutput takes the terminal's full stream over the ACP
// fragments already on the card, which legitimately shortens or rewrites them. A
// client that could only append would render the fragments plus the full stream.
func TestToolCallDelta_ReplacesWhenTheTerminalOutputWins(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	const termID = "term-1"
	deps.terminals[termID] = termRendered{text: "the terminal's whole stream\n"}

	// A fragment first, so there is something for the terminal's output to win over.
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "in_progress",
		"content": []map[string]any{
			{"type": "content", "content": map[string]any{"text": "a fragment"}},
		},
	}), FrameAttribution{})
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "completed",
		"content": []map[string]any{
			{"type": "terminal", "terminalId": termID},
		},
	}), FrameAttribution{})

	deltas := toolCallDeltas(t, events)
	last := deltas[len(deltas)-1]
	if !last.OutputReplace {
		t.Fatalf("the completing frame did not set output_replace: %+v — the fragment would survive under it",
			last)
	}
	if last.OutputDelta != "the terminal's whole stream\n" {
		t.Errorf("output_delta = %q, want the terminal's whole stream", last.OutputDelta)
	}
	got, _ := lastToolCallUpdate(t, deps, events)
	if got.Output != "the terminal's whole stream\n" {
		t.Errorf("folded output = %q, want the terminal's stream alone", got.Output)
	}
}

// TestToolCallDelta_AnUnchangedFieldIsAbsent is what makes "absent means
// unchanged" safe for the client to rely on.
func TestToolCallDelta_AnUnchangedFieldIsAbsent(t *testing.T) {
	tr, _, _, events, chatID := primeToolCall(t)

	// A frame that changes only the status: KAS sends title and kind nullish on
	// most updates, and the create already set both.
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "completed",
	}), FrameAttribution{})

	deltas := toolCallDeltas(t, events)
	if len(deltas) != 1 {
		t.Fatalf("got %d tool_call_update frames, want 1", len(deltas))
	}
	want := vibekit.ToolCallUpdatePayload{
		MessageID:  "tc-mid",
		ToolCallID: "tc-1",
		Status:     vibekit.ToolCompleted,
	}
	if !reflect.DeepEqual(deltas[0], want) {
		t.Errorf("a status-only frame = %+v,\nwant exactly %+v", deltas[0], want)
	}
}

func TestOutputDelta(t *testing.T) {
	tests := []struct {
		name        string
		before      string
		after       string
		wantDelta   string
		wantReplace bool
	}{
		{name: "unchanged", before: "abc", after: "abc"},
		{name: "appended", before: "abc", after: "abcdef", wantDelta: "def"},
		{name: "first write", before: "", after: "abc", wantDelta: "abc"},
		// The terminal-adoption cases: a shorter or a different value is not an
		// extension, so it has to travel whole.
		{name: "shortened", before: "abcdef", after: "abc", wantDelta: "abc", wantReplace: true},
		{name: "rewritten", before: "abc", after: "xyz", wantDelta: "xyz", wantReplace: true},
		{name: "cleared", before: "abc", after: "", wantDelta: "", wantReplace: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			delta, replace := outputDelta(tc.before, tc.after)
			if delta != tc.wantDelta || replace != tc.wantReplace {
				t.Errorf("outputDelta(%q, %q) = (%q, %t), want (%q, %t)",
					tc.before, tc.after, delta, replace, tc.wantDelta, tc.wantReplace)
			}
		})
	}
}

// toolCallDeltas returns every tool_call_update payload in events, in order.
func toolCallDeltas(t *testing.T, events *[]vibekit.ServerEvent) []vibekit.ToolCallUpdatePayload {
	t.Helper()
	var out []vibekit.ToolCallUpdatePayload
	for _, e := range *events {
		if e.Type != vibekit.EventToolCallUpdate {
			continue
		}
		p, ok := e.Payload.(vibekit.ToolCallUpdatePayload)
		if !ok {
			t.Fatalf("tool_call_update payload type = %T, want vibekit.ToolCallUpdatePayload", e.Payload)
		}
		out = append(out, p)
	}
	return out
}
