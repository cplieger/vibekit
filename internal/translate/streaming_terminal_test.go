package translate

// The terminal link on a tool call: where the id comes from, WHEN it is adopted
// relative to the status fold, and what happens when the output it names is gone.

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// A single frame can carry both the type:"terminal" content block and `completed`,
// so the link must be set before adoption runs — with the status fold first,
// adoption looks up an id the tool call does not yet have and never will, and the
// card persists an empty output. One frame carrying both is the only arrangement
// that can tell the two orderings apart.
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
	}), FrameAttribution{})

	tc, ok := lastToolCallUpdate(t, deps, events)
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
	}), FrameAttribution{})
	// No adoption while the command runs: the live stream owns those bytes.
	if tc, ok := lastToolCallUpdate(t, deps, events); ok && tc.Output != "" {
		t.Errorf("output = %q on an in_progress frame, want it empty until completion", tc.Output)
	}
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "status": "completed",
	}), FrameAttribution{})

	tc, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_call_update was broadcast")
	}
	if tc.Output != "done\n" {
		t.Errorf("output = %q, want %q", tc.Output, "done\n")
	}
}

// The terminal's output WINS over anything already on the tool call, because what
// is already there is an earlier ACP content block — a fragment of what the
// terminal holds in full — and preferring the tool call would persist it.
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
	}), FrameAttribution{})
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "status": "completed",
	}), FrameAttribution{})

	tc, _ := lastToolCallUpdate(t, deps, events)
	if tc.Output != "line 1\nline 2\nline 3\n" {
		t.Errorf("output = %q, want the terminal's full text rather than the fragment", tc.Output)
	}
}

// A type:"terminal" block's own text is never folded into the output delta: the
// bytes arrive on the terminal/* surface, so consuming both would double-render.
func TestParseToolUpdateContent_TerminalBlockContributesNoOutput(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"toolCallId": "tc-1", "status": "in_progress",
		"content": []map[string]any{
			{
				"type": "terminal", "terminalId": "term-4",
				"content": map[string]any{"text": "SHOULD NOT APPEAR"},
			},
		},
	}), FrameAttribution{})
	tc, _ := lastToolCallUpdate(t, deps, events)
	if strings.Contains(tc.Output, "SHOULD NOT APPEAR") {
		t.Errorf("output = %q, want the terminal block's text left to the stream", tc.Output)
	}
}

// A terminal link resolving to no record is the only signal that makes this path
// diagnosable, since an empty card is otherwise indistinguishable from a command
// that printed nothing. The benign cases must stay silent or the signal is
// worthless.
//
// Not parallel: captureSlog swaps the process-global slog default.
func TestAdoptTerminalOutput_MissIsLogged(t *testing.T) {
	const warning = "terminal output missing at completion"
	tests := []struct {
		name string
		// terminal, when non-nil, is registered under the id the tool call links.
		terminal *termRendered
		// priorOutput arrives on an earlier in_progress frame: a same-frame block
		// folds AFTER adoption, so it cannot be on the call before the miss.
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
				}), FrameAttribution{})
			}
			update := map[string]any{"toolCallId": "tc-1", "status": "completed"}
			if tt.linkTerminal {
				update["content"] = []map[string]any{
					{"type": "terminal", "terminalId": termID},
				}
			}
			tr.HandleToolCallUpdate(t.Context(), chatID, mustJSON(t, update), FrameAttribution{})

			if got := strings.Contains(logs.String(), warning); got != tt.wantWarn {
				t.Errorf("logged miss = %v, want %v (%s)\nlogs: %s",
					got, tt.wantWarn, tt.reason, logs.String())
			}
			if !tt.wantWarn {
				return
			}
			// The terminal id is the only handle on the runtime-side record, and the
			// byte count separates an empty card from a surviving fragment.
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

// The link is also taken from the INITIAL tool_call frame, so the card can
// subscribe to the live stream from its first paint rather than after an update.
func TestHandleToolCall_TakesTheTerminalLinkFromTheCreateFrame(t *testing.T) {
	deps, _, events := newLineCaptureDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "tc-mid" }))
	tr.HandleToolCall(t.Context(), "c1", mustJSON(t, map[string]any{
		"toolCallId": "tc-9", "title": "run", "kind": "execute", "status": "in_progress",
		"content": []map[string]any{{"type": "terminal", "terminalId": "term-9"}},
	}), FrameAttribution{})

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

// buffer.ComputeDuration CONSUMES its start time, so it answers 0 on a second
// read, and KAS can send more than one terminal status frame for one tool call.
// Assigning it unconditionally writes that 0 over a correct duration, which the
// turn-end persist then makes durable.
//
// The start time is staged rather than slept for, so the expectation is a fixed
// number rather than whatever the clock did.
func TestApplyToolCallStatus_SecondTerminalFrameKeepsTheFirstDuration(t *testing.T) {
	tr, _, deps, events, chatID := primeToolCall(t)
	buf := deps.bufStore.GetOrInit(chatID)
	buf.ToolStartTimes["tc-1"] = time.Now().UnixMilli() - 5000

	completed := mustJSON(t, map[string]any{"toolCallId": "tc-1", "status": "completed"})
	tr.HandleToolCallUpdate(t.Context(), chatID, completed, FrameAttribution{})

	first, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_call_update event for the first terminal frame")
	}
	if first.DurationMs < 5000 {
		t.Fatalf("first DurationMs = %d, want >= 5000", first.DurationMs)
	}

	tr.HandleToolCallUpdate(t.Context(), chatID, completed, FrameAttribution{})

	second, ok := lastToolCallUpdate(t, deps, events)
	if !ok {
		t.Fatal("no tool_call_update event for the second terminal frame")
	}
	if second.DurationMs != first.DurationMs {
		t.Errorf("second DurationMs = %d, want %d kept from the first frame", second.DurationMs, first.DurationMs)
	}
}
