package translate

// Tests for the terminal link on a tool call: where the id comes from, WHEN it
// is adopted relative to the status fold, and what happens when the output it
// names is gone.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// BF5. A single frame can carry both the type:"terminal" content block and
// `completed`. With the status fold running first, that frame's adoption looked
// up an id the tool call did not yet have — and never would, because the update
// carrying the link had already gone by. The measured symptom was a finished
// command whose card persisted an empty output.
//
// This is the test that fails if the two folds are ever reordered: the link and
// the terminal status arrive in ONE frame, which is the only arrangement that
// can tell the difference.
func TestAdoptTerminalOutput_LinkAndCompletionInOneFrame(t *testing.T) {
	const termID = "term-1"
	tr, _, deps, events, chatID := primeToolCall(t)
	deps.terminals[termID] = termRendered{
		text:  "hello\n",
		spans: []vibekit.TextSpan{{Start: 0, End: 5, FG: 1, BG: -1}},
	}

	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1",
		"status":     "completed",
		"content": []map[string]any{
			{"type": "terminal", "terminalId": termID},
		},
	}), "")

	tc, ok := lastToolCallUpdate(t, events)
	if !ok {
		t.Fatal("no tool_call_update was broadcast")
	}
	if tc.TerminalID != termID {
		t.Errorf("terminal_id = %q, want %q: the link was not adopted from the frame", tc.TerminalID, termID)
	}
	if tc.Output != "hello\n" {
		t.Errorf("output = %q, want %q: adoption ran before the link was set", tc.Output, "hello\n")
	}
	if len(tc.OutputSpans) != 1 || tc.OutputSpans[0].FG != 1 {
		t.Errorf("output_spans = %+v, want the terminal's one red span", tc.OutputSpans)
	}
}

// The ordinary sequence still works: the link arrives on an in_progress frame
// and the completion adopts against it.
func TestAdoptTerminalOutput_LinkOnAnEarlierFrame(t *testing.T) {
	const termID = "term-2"
	tr, _, deps, events, chatID := primeToolCall(t)
	deps.terminals[termID] = termRendered{text: "done\n"}

	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "status": "in_progress",
		"content": []map[string]any{{"type": "terminal", "terminalId": termID}},
	}), "")
	// An in_progress frame must NOT adopt: the command is still running, so the
	// bytes are provisional and the live stream owns them.
	if tc, ok := lastToolCallUpdate(t, events); ok && tc.Output != "" {
		t.Errorf("output = %q on an in_progress frame, want it empty until completion", tc.Output)
	}
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "status": "completed",
	}), "")

	tc, ok := lastToolCallUpdate(t, events)
	if !ok {
		t.Fatal("no tool_call_update was broadcast")
	}
	if tc.Output != "done\n" {
		t.Errorf("output = %q, want %q", tc.Output, "done\n")
	}
}

// The terminal's output WINS over anything already on the tool call. Two things
// can have put text there: an earlier ACP content block (a fragment of what the
// terminal holds in full), or KAS's synthesized explanation for a command that
// never spawned — and the second only happens when there is no terminal, so it
// never reaches this path. Preferring the tool call would persist the fragment.
func TestAdoptTerminalOutput_TerminalWinsOverAnEarlierFragment(t *testing.T) {
	const termID = "term-3"
	tr, _, deps, events, chatID := primeToolCall(t)
	deps.terminals[termID] = termRendered{text: "line 1\nline 2\nline 3\n"}

	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "status": "in_progress",
		"content": []map[string]any{
			{"type": "terminal", "terminalId": termID},
			{"type": "content", "content": map[string]any{"text": "line 1"}},
		},
	}), "")
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "status": "completed",
	}), "")

	tc, _ := lastToolCallUpdate(t, events)
	if tc.Output != "line 1\nline 2\nline 3\n" {
		t.Errorf("output = %q, want the terminal's full text rather than the fragment", tc.Output)
	}
}

// A type:"terminal" block's own text is never folded into the output delta: the
// bytes arrive on the terminal/* surface, so consuming both would double-render.
func TestParseToolUpdateContent_TerminalBlockContributesNoOutput(t *testing.T) {
	tr, _, _, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "status": "in_progress",
		"content": []map[string]any{
			{
				"type": "terminal", "terminalId": "term-4",
				"content": map[string]any{"text": "SHOULD NOT APPEAR"},
			},
		},
	}), "")
	tc, _ := lastToolCallUpdate(t, events)
	if strings.Contains(tc.Output, "SHOULD NOT APPEAR") {
		t.Errorf("output = %q, want the terminal block's text left to the stream", tc.Output)
	}
}

// The signal that makes this whole adoption path diagnosable: a terminal link
// that resolves to no record. An empty card is otherwise indistinguishable from
// a command that printed nothing — which is exactly how agent output stayed
// invisible for the whole life of the feature. The benign cases must stay silent
// or the signal is worthless, and the most common benign case by far is a
// command that legitimately printed nothing.
//
// Not parallel: captureSlog swaps the process-global slog default.
func TestAdoptTerminalOutput_MissIsLogged(t *testing.T) {
	const warning = "terminal output missing at completion"
	tests := []struct {
		name string
		// terminal, when non-nil, is registered under the id the tool call links.
		terminal *termRendered
		// priorOutput, when set, arrives on an earlier in_progress frame — the
		// fold applies a same-frame content block AFTER adoption, so this is the
		// only way to have text on the call before the miss.
		priorOutput  string
		linkTerminal bool
		wantWarn     bool
		wantBytes    string
		reason       string
	}{{
		name: "GoneAndNothingToShow", linkTerminal: true, wantWarn: true, wantBytes: "output_bytes=0",
		reason: "the record is absent and the card will render empty: the actionable miss",
	}, {
		name: "GoneWithAFragmentAlreadyOnTheCall", linkTerminal: true,
		priorOutput: "partial", wantWarn: true, wantBytes: "output_bytes=8",
		reason: "still a miss (the full output is lost); the byte count is what tells a reader it is a fragment",
	}, {
		name: "Found", linkTerminal: true, terminal: &termRendered{text: "hi\n"}, wantWarn: false,
		reason: "adoption succeeded",
	}, {
		name: "FoundButEmpty", linkTerminal: true, terminal: &termRendered{text: ""}, wantWarn: false,
		reason: "a genuinely silent command is registered and empty, not missing —" +
			" and `mkdir -p` is common enough that warning here would drown the signal",
	}, {
		name: "NoTerminalAtAll", wantWarn: false,
		reason: "most tool calls have no terminal; warning here would drown the signal",
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const termID = "term-1"
			var logs bytes.Buffer
			t.Cleanup(captureSlog(&logs))

			tr, _, deps, _, chatID := primeToolCall(t)
			if tt.terminal != nil {
				deps.terminals[termID] = *tt.terminal
			}
			if tt.priorOutput != "" {
				tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
					"toolCallId": "tc-1", "status": "in_progress",
					"content": []map[string]any{
						{"type": "content", "content": map[string]any{"text": tt.priorOutput}},
					},
				}), "")
			}
			update := map[string]any{"toolCallId": "tc-1", "status": "completed"}
			if tt.linkTerminal {
				update["content"] = []map[string]any{
					{"type": "terminal", "terminalId": termID},
				}
			}
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, update), "")

			if got := strings.Contains(logs.String(), warning); got != tt.wantWarn {
				t.Errorf("logged miss = %v, want %v (%s)\nlogs: %s",
					got, tt.wantWarn, tt.reason, logs.String())
			}
			if !tt.wantWarn {
				return
			}
			// The line has to be actionable: the terminal id is the only handle on
			// the hub-side record, and the byte count is what separates an empty
			// card from a surviving fragment.
			for _, want := range []string{
				"terminal_id=" + termID, "tool_call_id=tc-1",
				"chat_id=" + string(chatID), tt.wantBytes,
			} {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("log line missing %q, want it for diagnosis\nlogs: %s", want, logs.String())
				}
			}
		})
	}
}

// The link is also taken from the INITIAL tool_call frame, which is what lets the
// card subscribe to the live stream from its first paint rather than waiting for
// an update.
func TestHandleToolCall_TakesTheTerminalLinkFromTheCreateFrame(t *testing.T) {
	deps, _, events := newLineCaptureDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "tc-mid" }))
	tr.HandleToolCall(t.Context(), "c1", mustJSON(t, map[string]any{
		"toolCallId": "tc-9", "title": "run", "kind": "execute", "status": "in_progress",
		"content": []map[string]any{{"type": "terminal", "terminalId": "term-9"}},
	}), "")

	for _, e := range *events {
		if e.Type != vibekit.EventToolCall {
			continue
		}
		p, ok := e.Payload.(vibekit.ToolCallPayload)
		if !ok {
			t.Fatalf("tool_call payload = %T", e.Payload)
		}
		if p.ToolCall.TerminalID != "term-9" {
			t.Errorf("terminal_id = %q, want %q on the create frame", p.ToolCall.TerminalID, "term-9")
		}
		return
	}
	t.Fatal("no tool_call event was broadcast")
}
