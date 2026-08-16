package translate

import (
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// steerAcksFrom pulls the ack-bearing steer_injected frames out of a capture.
// The read frame KAS sends carries no Ack, so filtering on that field is what
// separates the two halves of the event rather than counting frames.
func steerAcksFrom(events []api.ServerEvent) []api.SteerInjectedPayload {
	var out []api.SteerInjectedPayload
	for _, e := range events {
		if e.Type != api.EventSteerInjected {
			continue
		}
		p, ok := e.Payload.(api.SteerInjectedPayload)
		if ok && p.Ack != "" {
			out = append(out, p)
		}
	}
	return out
}

// feedChunk streams one text delta through the live handler.
func feedChunk(t *testing.T, tr *Translator, chatID api.ChatID, text string) {
	t.Helper()
	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": text},
	}), false)
}

// The whole point of P26: the agent's own statement reaches the client instead of
// being discarded with the marker.
func TestHandleAssistantChunk_BroadcastsTheAgentsAcknowledgement(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, withIDGenerator(func() string { return "m1" }))
	chatID := api.ChatID("c1")

	feedChunk(t, tr, chatID, "Done. [STEERING steer-abc: rebased onto main instead]")

	acks := steerAcksFrom(*events)
	if len(acks) != 1 {
		t.Fatalf("got %d ack frames, want 1: %v", len(acks), eventTypes(*events))
	}
	if acks[0].SteerID != "steer-abc" {
		t.Errorf("SteerID = %q, want steer-abc", acks[0].SteerID)
	}
	if acks[0].Ack != "rebased onto main instead" {
		t.Errorf("Ack = %q, want the agent's sentence", acks[0].Ack)
	}
	// Text is empty deliberately: the steer's own text lives in KAS's buffer,
	// not in this layer, and the client merges by id rather than replacing.
	if acks[0].Text != "" {
		t.Errorf("Text = %q, want empty on the ack frame", acks[0].Text)
	}
}

// The marker closing a response usually arrives as its OWN delta, and that delta
// emits no text — so the handler returns early. This is the case a broadcast
// placed after that return would silently never serve, which makes it the case
// worth pinning.
func TestHandleAssistantChunk_AcknowledgementSurvivesAMarkerOnlyDelta(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, withIDGenerator(func() string { return "m1" }))
	chatID := api.ChatID("c1")

	feedChunk(t, tr, chatID, "All set.")
	feedChunk(t, tr, chatID, "[STEERING steer-solo: switched to the new API]")

	acks := steerAcksFrom(*events)
	if len(acks) != 1 {
		t.Fatalf("got %d ack frames from a marker-only delta, want 1: %v", len(acks), eventTypes(*events))
	}
	if acks[0].Ack != "switched to the new API" {
		t.Errorf("Ack = %q", acks[0].Ack)
	}
	// And the marker still does not reach the transcript.
	buf := deps.bufStore.GetOrInit(chatID)
	if got := buf.Content.String(); got != "All set." {
		t.Errorf("persisted content = %q, want the marker stripped", got)
	}
}

// Split across chunk boundaries the ack must fire exactly once, on the delta
// that closes the marker. Firing per chunk would put a truncated sentence on the
// chip and then correct it.
func TestHandleAssistantChunk_AcknowledgementFiresOnceWhenSplit(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, withIDGenerator(func() string { return "m1" }))
	chatID := api.ChatID("c1")

	for _, part := range []string{"ok ", "[STEERING ste", "er-split: kept the ", "existing shape]", " bye"} {
		feedChunk(t, tr, chatID, part)
	}

	acks := steerAcksFrom(*events)
	if len(acks) != 1 {
		t.Fatalf("got %d ack frames, want exactly 1: %+v", len(acks), acks)
	}
	if acks[0].SteerID != "steer-split" || acks[0].Ack != "kept the existing shape" {
		t.Errorf("ack = %+v", acks[0])
	}
}

// Reasoning is not screened for markers at all (KAS's own recordSteeringAcks
// reads text entries only), so a marker-shaped string in a thought must not
// broadcast an ack for a steer nothing answered.
func TestHandleAssistantChunk_ReasoningYieldsNoAcknowledgement(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, withIDGenerator(func() string { return "m1" }))
	chatID := api.ChatID("c1")

	tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "[STEERING steer-x: a thought]"},
	}), true)

	if acks := steerAcksFrom(*events); len(acks) != 0 {
		t.Errorf("got %d ack frames from reasoning, want none: %+v", len(acks), acks)
	}
}

// An empty body is not an answer. The marker `[STEERING steer-1: ]` cannot match
// the pattern at all (it requires at least one body character), but a body of
// pure whitespace trims to nothing, and a chip reading "read:" with nothing after
// it is worse than a chip reading "read".
func TestHandleAssistantChunk_EmptyAcknowledgementIsNotBroadcast(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, withIDGenerator(func() string { return "m1" }))
	chatID := api.ChatID("c1")

	feedChunk(t, tr, chatID, "done [STEERING steer-blank:    ]")

	if acks := steerAcksFrom(*events); len(acks) != 0 {
		t.Errorf("got %d ack frames for a whitespace body, want none: %+v", len(acks), acks)
	}
}
