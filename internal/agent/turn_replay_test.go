package agent

// The connect replay reads the TURN, not the prompt slot.

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// replayedTurnStates runs the two connect-time replays over one snapshot of the
// open-turn set and returns the turn_state events by chat, plus every chat that
// got a chat_status.
func replayedTurnStates(tb testing.TB, rt *Runtime) (turnStates map[vibekit.ChatID]vibekit.TurnStatePayload, statuses map[vibekit.ChatID]struct{}) {
	tb.Helper()
	turnStates = make(map[vibekit.ChatID]vibekit.TurnStatePayload)
	statuses = make(map[vibekit.ChatID]struct{})
	collect := func(evt vibekit.ServerEvent) error {
		switch evt.Type {
		case vibekit.EventTurnState:
			p, ok := evt.Payload.(vibekit.TurnStatePayload)
			if !ok {
				tb.Fatalf("turn_state carried %T", evt.Payload)
			}
			turnStates[evt.ChatID] = p
		case vibekit.EventChatStatus:
			statuses[evt.ChatID] = struct{}{}
		}
		return nil
	}
	open := rt.coord.turns.openTurns()
	if err := rt.replayTurnState(collect, "", open); err != nil {
		tb.Fatalf("replayTurnState: %v", err)
	}
	if err := rt.replayWaitingStatus(collect, "", open); err != nil {
		tb.Fatalf("replayWaitingStatus: %v", err)
	}
	return turnStates, statuses
}

// TestReplayTurnState_ServesATurnVibekitDidNotPrompt is the headline of P8: a
// client reconnecting during an agent-initiated turn used to be told nothing,
// because the replay enumerated the chats holding the PROMPT SLOT and such a turn
// holds none. What the client drew was an idle chat over a live transcript.
func TestReplayTurnState_ServesATurnVibekitDidNotPrompt(t *testing.T) {
	rt, _, _ := newTestHub()

	// A fold with no open turn is how vibekit first hears of a turn it did not
	// prompt, and it holds no prompt slot at any point.
	buf := rt.stageTurnBuffer(t, "c1")
	buf.StartTurn("m-agent")
	buf.AppendTextDelta("the agent woke itself", "")

	states, _ := replayedTurnStates(t, rt)
	got, ok := states["c1"]
	if !ok {
		t.Fatal("an agent-initiated turn was replayed as nothing, so the client draws it idle")
	}
	if got.Message == nil || got.Message.Content != "the agent woke itself" {
		t.Errorf("turn_state carried %+v, want the in-flight message", got.Message)
	}
}

// TestReplayTurnState_AFinalizingTurnDoesNotReadIdle pins the third state.
//
// turnFinalizing is the window where the turn has been claimed and its
// persistence and broadcast have not completed. A client told the chat is idle
// there renders idle and is then corrected by a turn_ended it was never sent,
// because the finalize's broadcast predates its connection.
func TestReplayTurnState_AFinalizingTurnDoesNotReadIdle(t *testing.T) {
	rt, _, _ := newTestHub()
	buf := rt.stageTurnBuffer(t, "c1")
	buf.StartTurn("m1")
	buf.AppendTextDelta("half an answer", "")

	if _, won := rt.coord.turns.claimOpen(t.Context(), "c1"); !won {
		t.Fatal("the fixture could not claim the turn, so the chat never reached turnFinalizing")
	}

	states, _ := replayedTurnStates(t, rt)
	if _, ok := states["c1"]; !ok {
		t.Error("a finalizing turn was replayed as nothing, so the client draws it idle mid-finalize")
	}
}

// TestReplayTurnState_APrimeIsNeverServed pins the fold-time source policy at the
// replay door.
//
// A prime is vibekit's own transcript replay, sent as a real session/prompt. Its
// frames are neither broadcast nor persisted, so serving them here would render
// the priming preamble as conversation and then lose it on the next reload — the
// vanishing-message class, arriving through the one door that synthesizes state
// rather than replaying it.
func TestReplayTurnState_APrimeIsNeverServed(t *testing.T) {
	rt, _, _ := newTestHub()
	if rt.coord.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrime) == 0 {
		t.Fatal("the fixture could not open a prime turn")
	}
	buf := rt.stageTurnBuffer(t, "c1")
	buf.StartTurn("m-prime")
	buf.AppendTextDelta("The context was just switched", "")
	// A retained waiting status, so the second assertion below can fail: without
	// one there is nothing for replayWaitingStatus to emit and the check is vacuous.
	rt.bus.chatStatus.Set("c1", vibekit.ChatStatusPayload{Status: vibekit.ChatStatusWaitingOnUser})

	states, statuses := replayedTurnStates(t, rt)
	if got, ok := states["c1"]; ok {
		t.Errorf("a prime's buffer was served as turn_state (%+v), so the preamble renders as conversation", got.Message)
	}
	// And the chat is still BUSY, so a status the agent declared before the prime
	// is not re-asserted over a turn that is running.
	if _, ok := statuses["c1"]; ok {
		t.Error("a chat with an open prime turn was replayed a chat_status, so a stale status describes the wrong turn")
	}
}
