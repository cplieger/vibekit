package agent

// Where persistDisplacedTurn puts the reply, and the one row the walk must NOT
// step over.
//
// The walk exists to skip the user rows a chat carries AHEAD of the turn being
// committed: a prompt persists its row before it asks for admission, so appending
// the reply after it would make projectTurns read a headerless turn. A STEER row is
// the other shape a trailing user row can take, and it is the opposite case — it was
// persisted INSIDE the turn now being committed, so the reply belongs after it.

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// appendSteerRow appends the durable row translate.persistSteer writes for a steer
// the model read mid-turn.
func appendSteerRow(t *testing.T, cs *fakeChatStore, chatID vibekit.ChatID, text string) {
	t.Helper()
	if err := cs.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool {
		c.Messages = append(c.Messages, vibekit.Message{
			ID:          newMessageID(),
			Role:        vibekit.RoleUser,
			UserKind:    vibekit.UserKindSteer,
			SteerState:  vibekit.SteerStateRead,
			SteerOrigin: vibekit.SteerOriginUser,
			Content:     text,
		})
		return true
	}); err != nil {
		t.Fatalf("append the steer row: %v", err)
	}
}

// rowKinds returns each message as one of "prompt", "steer" or "assistant", in file
// order, so a case states the order it wants rather than a pair of indices.
func rowKinds(t *testing.T, cs *fakeChatStore, chatID vibekit.ChatID) []string {
	t.Helper()
	c, ok := cs.Get(t.Context(), chatID)
	if !ok {
		t.Fatalf("chat %q vanished", chatID)
	}
	out := make([]string, 0, len(c.Messages))
	for i := range c.Messages {
		switch {
		case c.Messages[i].UserKind == vibekit.UserKindSteer:
			out = append(out, "steer")
		case c.Messages[i].Role == vibekit.RoleUser:
			out = append(out, "prompt")
		case c.Messages[i].Role == vibekit.RoleAssistant:
			out = append(out, "assistant")
		}
	}
	return out
}

// A steer read during the displaced turn stays ABOVE that turn's reply, which is the
// order the ordinary path produces (the reply is flushed at turn end, after the row).
// Stepping over it put the reply above a message it was answering.
func TestPersistDisplacedTurn_KeepsAReadSteerAboveTheReply(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "the displaced turn's reply")
	appendSteerRow(t, cs, "c1", "use tabs")

	h.coord.persistDisplacedTurn(t.Context(), "c1", &vibekit.Message{
		ID: newMessageID(), Role: vibekit.RoleAssistant, Content: "the displaced turn's reply",
	})

	want := []string{"steer", "assistant"}
	if got := rowKinds(t, cs, "c1"); !equalStrings(got, want) {
		t.Errorf("row order = %v, want %v — the steer joined the turn being committed, "+
			"so the reply goes after it", got, want)
	}
}

// The rule the walk exists for, unchanged: a PROMPT awaiting admission is stepped
// over, or projectTurns reads the reply as a headerless turn below it.
func TestPersistDisplacedTurn_StillStepsOverATrailingPrompt(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "the displaced turn's reply")
	appendUserRow(t, cs, "c1", "a prompt sent while the engine turn was still going")

	h.coord.persistDisplacedTurn(t.Context(), "c1", &vibekit.Message{
		ID: newMessageID(), Role: vibekit.RoleAssistant, Content: "the displaced turn's reply",
	})

	want := []string{"assistant", "prompt"}
	if got := rowKinds(t, cs, "c1"); !equalStrings(got, want) {
		t.Errorf("row order = %v, want %v", got, want)
	}
}

// Both shapes at once, which is what makes the boundary a real one rather than a
// choice between two rules: the walk stops at the steer and the prompt above it
// still gets stepped over is NOT what happens — a steer BELOW a prompt ends the
// walk, so the reply lands between them.
func TestPersistDisplacedTurn_StopsAtTheSteerEvenWithAPromptBelowIt(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "the displaced turn's reply")
	appendSteerRow(t, cs, "c1", "use tabs")
	appendUserRow(t, cs, "c1", "a prompt sent while the engine turn was still going")

	h.coord.persistDisplacedTurn(t.Context(), "c1", &vibekit.Message{
		ID: newMessageID(), Role: vibekit.RoleAssistant, Content: "the displaced turn's reply",
	})

	want := []string{"steer", "assistant", "prompt"}
	if got := rowKinds(t, cs, "c1"); !equalStrings(got, want) {
		t.Errorf("row order = %v, want %v", got, want)
	}
}
