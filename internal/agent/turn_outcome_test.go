package agent

// The outcome, durable: what a finalized turn RECORDS, and what a prime records
// instead (nothing at all).

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// turnEndedOutcomes returns the outcome of every turn_ended broadcast so far.
func turnEndedOutcomes(t *testing.T, h *Runtime) []vibekit.TurnOutcome {
	t.Helper()
	var out []vibekit.TurnOutcome
	for _, e := range bufferedSince(h, 0) {
		var msg struct {
			Type    vibekit.EventType `json:"type"`
			Payload struct {
				Outcome vibekit.TurnOutcome `json:"outcome"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &msg); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if msg.Type == vibekit.EventTurnEnded {
			out = append(out, msg.Payload.Outcome)
		}
	}
	return out
}

// TestCloseOnWireEnd_StampsTheOutcomeOnTheAssistantMessage is the durability the
// whole of P9 turns on.
//
// A stop reason rides the live turn_ended SSE and was never stored, so a reloaded
// transcript had to infer the outcome from whichever event messages happened to
// survive — and `error` produces none, so a turn that streamed an answer and then
// failed read `completed` on reload with its footer suppressed, indistinguishable
// from a clean short reply.
func TestCloseOnWireEnd_StampsTheOutcomeOnTheAssistantMessage(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "here is half an answer")

	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonError)

	c, ok := cs.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat record vanished")
	}
	var stamped *vibekit.Message
	for i := range c.Messages {
		if c.Messages[i].Role == vibekit.RoleAssistant {
			stamped = &c.Messages[i]
		}
	}
	if stamped == nil {
		t.Fatal("no assistant message was persisted")
	}
	if stamped.TurnOutcome != vibekit.TurnOutcomeFailed {
		t.Errorf("persisted outcome = %q, want failed — a reload reads this, not the SSE", stamped.TurnOutcome)
	}
	if stamped.TurnStopReasonRaw != vibekit.StopReasonError {
		t.Errorf("persisted raw stop reason = %q, want %q", stamped.TurnStopReasonRaw, vibekit.StopReasonError)
	}
	if got := turnEndedOutcomes(t, h); len(got) != 1 || got[0] != vibekit.TurnOutcomeFailed {
		t.Errorf("broadcast outcomes = %v, want exactly [failed]", got)
	}
}

// TestCloseOnWireEnd_TruncationCompletesRatherThanFails pins the one mapping a
// reader would get wrong by instinct: a turn stopped at a bound finished the work
// it was allowed to do, so it COMPLETED with its answer cut off. Grading it failed
// would report a bounded turn as broken.
func TestCloseOnWireEnd_TruncationCompletesRatherThanFails(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "as much as the budget allowed")

	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonMaxTokens)

	c, _ := cs.Get(t.Context(), "c1")
	for i := range c.Messages {
		m := &c.Messages[i]
		if m.Role != vibekit.RoleAssistant {
			continue
		}
		if m.TurnOutcome != vibekit.TurnOutcomeCompleted || !m.TurnTruncated {
			t.Errorf("outcome=%q truncated=%v, want completed + truncated", m.TurnOutcome, m.TurnTruncated)
		}
	}
}

// TestCloseOnWireEnd_AnEmptyFailedTurnPersistsAMarker is the carrier rule.
//
// A turn that emitted nothing has no assistant message, so the outcome has nowhere
// to live: before the marker, such a turn appended nothing at all, read `completed`
// on reload, and — having no message of its own — joined the PREVIOUS turn's body
// and vanished from the turn index.
func TestCloseOnWireEnd_AnEmptyFailedTurnPersistsAMarker(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	h.coord.OpenTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)

	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonError)

	c, _ := cs.Get(t.Context(), "c1")
	var marker *vibekit.Message
	for i := range c.Messages {
		if c.Messages[i].EventKind == vibekit.EventTurnOutcome {
			marker = &c.Messages[i]
		}
	}
	if marker == nil {
		t.Fatalf("no outcome marker was persisted, so the failure is unreadable after a reload. messages=%d", len(c.Messages))
	}
	if marker.Role != vibekit.RoleEvent || marker.TurnOutcome != vibekit.TurnOutcomeFailed {
		t.Errorf("marker = %+v, want a RoleEvent carrying failed", marker)
	}
}

// TestCloseOnWireEnd_AnEmptyCompletedPromptedTurnPersistsNothing is the other half
// of the same rule, and what keeps the transcript clean.
//
// The skip is SEMANTIC, not "the outcome is completed": a completed untruncated
// marker states only what the derivation answers by default, so it is worth
// omitting exactly where the transcript already carries a row that IS this turn —
// and a prompted turn's user message is that row. See the agent-initiated twin
// below for the case where nothing else represents the turn.
func TestCloseOnWireEnd_AnEmptyCompletedPromptedTurnPersistsNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	h.coord.OpenTurn(t.Context(), "c1", vibekit.TurnSourcePrompt)

	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonEndTurn)

	c, _ := cs.Get(t.Context(), "c1")
	for i := range c.Messages {
		if c.Messages[i].EventKind == vibekit.EventTurnOutcome {
			t.Errorf("an empty COMPLETED prompted turn persisted a marker: %+v", c.Messages[i])
		}
	}
}

// TestCloseOnWireEnd_AnEmptyCompletedAgentTurnStillPersistsItsMarker is the case a
// narrowing on `Outcome == completed` alone DELETED, and the reason the skip above
// had to become semantic.
//
// A KAS auto-wake that emits nothing and ends `end_turn` is a real turn: it opened,
// it was announced live, it consumed a slot in the session's numbering. With no
// user message and no assistant message, the marker is the only thing that can
// persist it — so skipping it left the turn absent from the transcript AND from the
// rail, with the live client having been told it ended and a reload disagreeing.
func TestCloseOnWireEnd_AnEmptyCompletedAgentTurnStillPersistsItsMarker(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	// A turn vibekit did not prompt: the first frame of the bracket opens a
	// wireTurnStart turn, and nothing folds into it.
	h.stageTurnBuffer(t, "c1")

	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonEndTurn)

	c, _ := cs.Get(t.Context(), "c1")
	var marker *vibekit.Message
	for i := range c.Messages {
		if c.Messages[i].EventKind == vibekit.EventTurnOutcome {
			marker = &c.Messages[i]
		}
	}
	if marker == nil {
		t.Fatalf("an empty completed agent-initiated turn persisted nothing, so it does "+
			"not exist after a reload. messages=%d", len(c.Messages))
	}
	if marker.Role != vibekit.RoleEvent || marker.TurnOutcome != vibekit.TurnOutcomeCompleted {
		t.Errorf("marker = %+v, want a RoleEvent carrying completed", marker)
	}
}

// TestCloseOnWireEnd_APrimeBroadcastsAndPersistsNothing is the fold-time source
// policy at the finalize door.
//
// The prime is a real session/prompt carrying the transcript, so its reply is real
// text on the wire. Revision 4 suppressed only the persist and left three leaks,
// all reachable: the chunks were broadcast live and then vanished on reload (the
// vanishing-message class this codebase has already paid for), a reconnect could be
// served the prime's buffer, and a steer sent into the window was swallowed. This
// pins the finalize half; the fold half is buffer.Buffer.Muted, the serve half is
// replayTurnState and the steer half is CmdSteer.
func TestCloseOnWireEnd_APrimeBroadcastsAndPersistsNothing(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if h.coord.OpenTurn(t.Context(), "c1", vibekit.TurnSourcePrime) == 0 {
		t.Fatal("the fixture could not open a prime turn")
	}
	buf := h.stageTurnBuffer(t, "c1")
	buf.Started = true
	buf.MessageID = newMessageID()
	buf.Content.WriteString("Caught up.")

	h.coord.WireTurnEnd(t.Context(), "c1", vibekit.StopReasonEndTurn)

	if got := turnEndedStops(t, h); len(got) != 0 {
		t.Errorf("a prime announced its end (%v); the client would render a turn it must not see", got)
	}
	c, _ := cs.Get(t.Context(), "c1")
	if len(c.Messages) != 0 {
		t.Errorf("a prime persisted %d messages: %+v", len(c.Messages), c.Messages)
	}
}

// TestOpenTurn_APrimesBufferIsMuted pins the FOLD half: the frames land in the
// prime's own buffer — a revised binding hands that buffer to the agent's own turn,
// whose content it then is — and publish nothing while they are the prime's.
func TestOpenTurn_APrimesBufferIsMuted(t *testing.T) {
	h, _, _ := newTestHub()
	cases := []struct {
		name      string
		source    vibekit.TurnOpenSource
		wantMuted bool
	}{
		{name: "a prime publishes nothing", source: vibekit.TurnSourcePrime, wantMuted: true},
		{name: "a prompt publishes", source: vibekit.TurnSourcePrompt, wantMuted: false},
		{name: "a turn the wire started publishes", source: vibekit.TurnSourceWireTurnStart, wantMuted: false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chatID := vibekit.ChatID("c" + string(rune('1'+i)))
			if tc.source == vibekit.TurnSourceWireTurnStart {
				buf := h.stageTurnBuffer(t, chatID)
				if buf.Muted() != tc.wantMuted {
					t.Errorf("Muted() = %v, want %v", buf.Muted(), tc.wantMuted)
				}
				return
			}
			if h.coord.OpenTurn(t.Context(), chatID, tc.source) == 0 {
				t.Fatal("the fixture could not open a turn")
			}
			buf := h.stageTurnBuffer(t, chatID)
			if buf.Muted() != tc.wantMuted {
				t.Errorf("Muted() = %v, want %v", buf.Muted(), tc.wantMuted)
			}
		})
	}
}
