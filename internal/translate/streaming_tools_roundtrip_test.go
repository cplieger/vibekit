package translate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
	"pgregory.net/rapid"
)

// The round-trip property `toolCallDelta` claims: apply the frame to the value
// the fold started from and you get the value the fold produced.
//
// It is the whole reason the client keeps no rules of its own about which fields
// accumulate, and until this file it was asserted by a comment naming a test that
// did not exist. The per-field tests in streaming_tools_delta_test.go check the
// BUILDER against a hand-written expectation, which cannot catch the builder and
// an applier drifting apart — that is a different question and it needs both
// sides in one assertion.
//
// applyDelta below is the SPECIFICATION of the wire contract, not a copy of
// production code: nothing in the server applies a delta (the buffer holds the
// whole object), so the only production applier is `foldToolCallDelta` in
// static-src/store.ts, in another language. So the contract is pinned twice —
// here against this spec over rapid-generated fold sequences, and in
// static-src/tool-call-delta.node.test.ts, which reads the same fixture this file
// writes its cases from and drives the real client fold.

// applyDelta is the wire contract: every omitted field means unchanged, output
// appends unless replaced, diffs append, everything else is assigned.
//
// It deliberately mirrors the CLIENT's fold rather than any Go code, because the
// client is the only consumer a delta has.
func applyDelta(before vibekit.ToolCall, d *vibekit.ToolCallUpdatePayload) vibekit.ToolCall {
	out := before
	if d.Title != "" {
		out.Title = d.Title
	}
	if d.Kind != "" {
		out.Kind = d.Kind
	}
	if d.Status != "" {
		out.Status = d.Status
	}
	switch {
	case d.OutputReplace:
		out.Output = d.OutputDelta
	case d.OutputDelta != "":
		out.Output = before.Output + d.OutputDelta
	}
	if len(d.OutputSpans) > 0 {
		out.OutputSpans = d.OutputSpans
	}
	if len(d.DiffsAppended) > 0 {
		out.Diffs = append(append([]vibekit.ToolDiff(nil), before.Diffs...), d.DiffsAppended...)
	}
	if len(d.Locations) > 0 {
		out.Locations = d.Locations
	}
	if d.DurationMs != 0 {
		out.DurationMs = d.DurationMs
	}
	if d.TerminalID != "" {
		out.TerminalID = d.TerminalID
	}
	if d.SubSessionID != "" {
		out.SubSessionID = d.SubSessionID
	}
	if d.AgentSubtaskID != "" {
		out.AgentSubtaskID = d.AgentSubtaskID
	}
	if d.WorkflowID != "" {
		out.WorkflowID = d.WorkflowID
	}
	if d.Checkpoint != nil {
		out.Checkpoint = d.Checkpoint
	}
	if d.Disclosed != nil {
		out.Disclosed = d.Disclosed
	}
	if d.Denial != nil {
		out.Denial = d.Denial
	}
	return out
}

// TestToolCallDelta_RoundTripsOverAFoldSequence draws a sequence of LEGAL folds
// and asserts each step's frame reconstructs that step's result.
//
// Legal is load-bearing, and it is the property's real precondition. `omitempty`
// means no field can express a reset to its zero, so the round trip holds only
// for the transitions the server's fold can actually produce: status, title and
// kind are set to non-empty values, output appends or is replaced wholesale, diffs
// append, the four identity ids and the three metadata blocks are set once. A
// generator free to blank a field would falsify the property by asking the wire
// for something the fold never does — which is why each step below is one of the
// folds applyToolCallUpdate performs and nothing else.
func TestToolCallDelta_RoundTripsOverAFoldSequence(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		tc := vibekit.ToolCall{
			ID:     "call-1",
			Title:  "Reading",
			Kind:   vibekit.ToolKindRead,
			Status: vibekit.ToolPending,
		}
		for range rapid.IntRange(1, 20).Draw(rt, "steps") {
			before := tc
			foldStep(rt, &tc)
			d := toolCallDelta("msg-1", &before, &tc)
			got := applyDelta(before, &d)
			if !sameToolCall(&got, &tc) {
				t.Fatalf("round trip lost a change\nbefore = %+v\ndelta  = %+v\nfolded = %+v\nwant   = %+v",
					before, d, got, tc)
			}
			// The frame must also name the call it addresses, or a client cannot
			// find anything to apply it to.
			if d.ToolCallID != tc.ID || d.MessageID != "msg-1" {
				t.Fatalf("delta address = (%q, %q), want (%q, %q)",
					d.MessageID, d.ToolCallID, "msg-1", tc.ID)
			}
		}
	})
}

// foldStep applies one of the mutations the server's fold performs.
func foldStep(rt *rapid.T, tc *vibekit.ToolCall) {
	switch rapid.IntRange(0, 8).Draw(rt, "step") {
	case 0:
		// The commonest frame by far: a status transition and nothing else.
		tc.Status = rapid.SampledFrom([]vibekit.ToolStatus{
			vibekit.ToolPending, vibekit.ToolInProgress,
			vibekit.ToolCompleted, vibekit.ToolFailed,
		}).Draw(rt, "status")
	case 1:
		tc.Output += rapid.StringN(1, 40, 40).Draw(rt, "chunk")
	case 2:
		// adoptTerminalOutput: at completion a terminal's full stream replaces the
		// ACP fragments already accumulated, which can shorten or rewrite them.
		// This is the one fold a pure-append wire cannot express.
		tc.Output = rapid.StringN(0, 40, 40).Draw(rt, "terminalOutput")
	case 3:
		tc.Diffs = append(tc.Diffs, vibekit.ToolDiff{
			Path:    rapid.StringMatching(`[a-z]{1,8}\.go`).Draw(rt, "diffPath"),
			NewText: rapid.StringN(0, 20, 20).Draw(rt, "newText"),
		})
	case 4:
		tc.Locations = []vibekit.ToolLocation{{
			Path: rapid.StringMatching(`[a-z]{1,8}\.go`).Draw(rt, "locPath"),
			Line: rapid.IntRange(1, 500).Draw(rt, "locLine"),
		}}
	case 5:
		tc.OutputSpans = []vibekit.TextSpan{{
			Start: 0,
			End:   rapid.IntRange(1, 20).Draw(rt, "spanEnd"),
			Attrs: uint16(rapid.IntRange(1, 8).Draw(rt, "spanAttrs")),
		}}
	case 6:
		// The four late identity attachments, each adopted once and never
		// overwritten — so a step that finds one already set changes nothing,
		// which is itself a case worth generating.
		id := rapid.StringMatching(`[a-z]{4,8}`).Draw(rt, "attachID")
		switch rapid.IntRange(0, 3).Draw(rt, "which") {
		case 0:
			if tc.TerminalID == "" {
				tc.TerminalID = id
			}
		case 1:
			if tc.SubSessionID == "" {
				tc.SubSessionID = id
			}
		case 2:
			if tc.AgentSubtaskID == "" {
				tc.AgentSubtaskID = id
			}
		case 3:
			if tc.WorkflowID == "" {
				tc.WorkflowID = id
			}
		}
	case 7:
		if tc.Checkpoint == nil {
			tc.Checkpoint = &vibekit.ToolCheckpoint{
				Original: rapid.StringMatching(`[a-z]{1,8}`).Draw(rt, "cpOriginal"),
			}
		}
	case 8:
		if tc.DurationMs == 0 {
			tc.DurationMs = rapid.IntRange(1, 100_000).Draw(rt, "durationMs")
		}
	}
}

// sameToolCall compares two tool calls by their JSON, which is the representation
// the contract is about — and which treats a nil and an empty slice as equal, as
// the wire does.
func sameToolCall(a, b *vibekit.ToolCall) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// deltaFixture is one cross-language case: the value the fold started from, the
// frame the server sends, and the value a client must end up holding.
type deltaFixture struct {
	Name   string                        `json:"name"`
	Before vibekit.ToolCall              `json:"before"`
	Delta  vibekit.ToolCallUpdatePayload `json:"delta"`
	After  vibekit.ToolCall              `json:"after"`
}

// TestToolCallDelta_SharedFixture pins the BUILDER against the same cases
// static-src/tool-call-delta.node.test.ts drives the client fold with.
//
// The fixture is the contract; neither language owns a private table. A case added
// to one side's expectations only would let the two folds diverge on exactly the
// transition it describes, which is what the round-trip property above cannot see
// across a language boundary.
func TestToolCallDelta_SharedFixture(t *testing.T) {
	path := filepath.Join("testdata", "tool_call_delta.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Setup: read %s: %v", path, err)
	}
	var fx struct {
		Cases []deltaFixture `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("Setup: parse %s: %v", path, err)
	}
	if len(fx.Cases) == 0 {
		t.Fatal("fixture carries no cases; an empty table would pass forever")
	}
	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := toolCallDelta(c.Delta.MessageID, &c.Before, &c.After)
			wantJSON, _ := json.Marshal(c.Delta)
			gotJSON, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("Setup: marshal delta: %v", err)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("toolCallDelta(%q, before, after) = %s, want %s",
					c.Delta.MessageID, gotJSON, wantJSON)
			}
			// And the same case must round-trip, so a fixture whose `after` does
			// not follow from `before` + `delta` fails here rather than teaching
			// the client half a wrong expectation.
			if folded := applyDelta(c.Before, &c.Delta); !sameToolCall(&folded, &c.After) {
				t.Errorf("applyDelta(before, delta) = %+v, want %+v", folded, c.After)
			}
		})
	}
}
