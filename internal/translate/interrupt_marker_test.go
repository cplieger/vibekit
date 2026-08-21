package translate

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// chunkDeltas returns every streamed text delta in a capture, in order, so a
// test can assert the sentinel still REACHED the user. Ending the turn without
// showing why would trade a wedge for a silence.
func chunkDeltas(events []vibekit.ServerEvent) []string {
	var out []string
	for _, e := range events {
		if e.Type != vibekit.EventMessageChunk {
			continue
		}
		if p, ok := e.Payload.(vibekit.MessageChunkPayload); ok {
			out = append(out, p.Delta)
		}
	}
	return out
}

// feedChunkAs streams one delta with a chosen reasoning flag and an optional
// `_meta.kiro` block, which is how a workflow-step frame is staged.
func feedChunkAs(t *testing.T, tr *Translator, chatID vibekit.ChatID, text string, isReasoning bool, meta map[string]any) {
	t.Helper()
	frame := map[string]any{"content": map[string]any{"type": "text", "text": text}}
	if meta != nil {
		frame["_meta"] = map[string]any{"kiro": meta}
	}
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, frame), isReasoning)
}

// TestHandleAssistantChunk_SentinelEndsTheTurn is the headline case.
//
// kiro-cli's security filter cancels a tool call, sends this one sentence, and
// then never answers the session/prompt. Bridge.Call has no deadline, so before
// this the prompt slot stayed held until the tab was closed and every later Send
// on the chat answered 409 busy.
//
// Three assertions, and the middle one is the easy thing to get wrong: the turn
// ends, the sentence is STILL broadcast so the user learns why, and the reason
// travels so the transcript's divider can attribute it.
func TestHandleAssistantChunk_SentinelEndsTheTurn(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "m1" }))
	chatID := vibekit.ChatID("c1")

	feedChunk(t, tr, chatID, interruptSentinel)

	if len(deps.turnInterrupts) != 1 {
		t.Fatalf("got %d interrupts, want 1: the turn is wedged until the tab closes",
			len(deps.turnInterrupts))
	}
	if got := deps.turnInterrupts[0].chatID; got != chatID {
		t.Errorf("interrupted chat %q, want %q", got, chatID)
	}
	if got := deps.turnInterrupts[0].reason; got != interruptReason {
		t.Errorf("reason = %q, want %q — the divider has no other source for it", got, interruptReason)
	}

	// The sentence must reach the transcript. It is the only account the user
	// gets of why the turn stopped, and the host's teardown takes the buffer, so
	// a broadcast ordered after the interrupt would be lost.
	deltas := chunkDeltas(*events)
	if len(deltas) != 1 || deltas[0] != interruptSentinel {
		t.Errorf("streamed deltas = %q, want the sentinel shown to the user", deltas)
	}
}

// TestHandleAssistantChunk_SentinelIsExactMatchOnly is the negative half, and it
// is the reason the match rule is equality rather than a substring or a prefix.
//
// Every row here is a turn that must NOT be killed. The quoted-prose row is the
// one that matters most: a model writing about the interruption message would
// otherwise stop its own turn mid-thought.
func TestHandleAssistantChunk_SentinelIsExactMatchOnly(t *testing.T) {
	cases := map[string]struct {
		text        string
		isReasoning bool
		meta        map[string]any
	}{
		"quoted inside prose": {
			text: `kiro-cli prints "` + interruptSentinel + `" when its filter trips.`,
		},
		"sentinel with a trailing sentence": {
			text: interruptSentinel + " Let me try a different approach.",
		},
		"a leading fragment of it": {
			text: "Tool uses were",
		},
		// The accepted miss, pinned so nobody widens the rule without evidence.
		// A prefix matcher would catch this and would also fire on any turn
		// opening with those three words.
		"split across deltas is deliberately missed": {
			text: "Tool uses were interrupted, waiting for",
		},
		// A sentinel-shaped THOUGHT is the model thinking about the sentinel, and
		// KAS reads its own markers from text entries only.
		"as a reasoning chunk": {
			text:        interruptSentinel,
			isReasoning: true,
		},
		// The second member of kiro-cli's own TUI sentinel list. vibekit handles
		// a user cancel end to end already, so matching it here could only end a
		// turn the cancel path is ending — or end one on a quote.
		"the user-cancel sentinel": {
			text: "Response was interrupted by the user",
		},
		// A workflow STEP has no session/prompt of vibekit's to release, and the
		// only stop verb is run-scoped, so a step's frame must not cancel the
		// parent chat's live turn.
		"a workflow step frame": {
			text: interruptSentinel,
			meta: map[string]any{"workflow": map[string]any{
				"workflowId": "wf-1", "nodeId": "n-1", "iteration": 1,
			}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			deps, _ := newEventCaptureDeps()
			tr := New(rolesOf(deps), withIDGenerator(func() string { return "m1" }))

			feedChunkAs(t, tr, "c1", tc.text, tc.isReasoning, tc.meta)

			if len(deps.turnInterrupts) != 0 {
				t.Errorf("the turn was ended by %q; only a delta that IS the sentinel may end one",
					tc.text)
			}
		})
	}
}

// TestHandleAssistantChunk_SentinelToleratesSurroundingWhitespace: the rule is
// exact equality on the TRIMMED delta, because a trailing newline is framing
// rather than content and dropping the trim would miss the real frame.
func TestHandleAssistantChunk_SentinelToleratesSurroundingWhitespace(t *testing.T) {
	for name, text := range map[string]string{
		"trailing newline": interruptSentinel + "\n",
		"leading newline":  "\n" + interruptSentinel,
		"padded":           "  " + interruptSentinel + "  ",
	} {
		t.Run(name, func(t *testing.T) {
			deps, _ := newEventCaptureDeps()
			tr := New(rolesOf(deps), withIDGenerator(func() string { return "m1" }))

			feedChunk(t, tr, "c1", text)

			if len(deps.turnInterrupts) != 1 {
				t.Errorf("%q did not end the turn; whitespace around the sentinel is framing", text)
			}
		})
	}
}

// TestHandleAssistantChunk_SentinelEndsTheTurnOnce: frames keep arriving after
// the filter trips, and the interrupt must not be re-issued per frame. The host
// latches the first cause per turn, and this pins the detector's half — one call
// per matching delta, so two deltas are two calls and the host's latch is what
// makes the SECOND a no-op. Stated here so a future reader does not move the
// latch out of the host on the belief this side deduplicates.
func TestHandleAssistantChunk_SentinelEndsTheTurnOnce(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "m1" }))

	feedChunk(t, tr, "c1", interruptSentinel)
	feedChunk(t, tr, "c1", interruptSentinel)

	if len(deps.turnInterrupts) != 2 {
		t.Errorf("got %d interrupts for 2 sentinel deltas, want 2; the one-shot lives on "+
			"the host (sharedBridge.interruptTurn), not here", len(deps.turnInterrupts))
	}
}
