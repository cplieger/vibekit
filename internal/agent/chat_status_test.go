package agent

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/v2/sse"
)

// TestChatStatusCache covers the one turn_state input the assistant buffer
// cannot supply. chat_status arrives on KAS's focus_update channel, so it lives
// on no message and in no replay — deleting the turn mirror without this would
// have silently dropped the label from every mid-turn reconnect.
func TestChatStatusCache(t *testing.T) {
	c := newChatStatusCache()

	if got := c.Get("nobody"); got.Status != "" {
		t.Errorf("unknown chat returned %q, want the zero payload", got.Status)
	}

	c.Merge("c1", vibekit.ChatStatusPayload{Status: "in_progress", Description: "reading files"})
	got := c.Get("c1")
	if got.Status != "in_progress" || got.Description != "reading files" {
		t.Errorf("got %+v, want in_progress/reading files", got)
	}

	// Newest wins: the agent re-declares as focus shifts.
	c.Merge("c1", vibekit.ChatStatusPayload{Status: "waiting_on_user", Description: "needs a decision"})
	if got := c.Get("c1"); got.Status != "waiting_on_user" {
		t.Errorf("got %q, want the latest status", got.Status)
	}

	// Cleared at turn end, so a later connect cannot report a finished turn's
	// label as current — the same reason the live event is ephemeral.
	c.Clear("c1")
	if got := c.Get("c1"); got.Status != "" {
		t.Errorf("status %q survived the turn", got.Status)
	}

	// waiting_on_user is the one status ClearAtTurnEnd RETAINS: its whole meaning is
	// that the turn ended and a person still owes an answer, so a refresh or a second
	// device must still find it. Measured 2026-09-08: deleting that early return left
	// this package green, so nothing pinned the rule the amber dot rests on.
	c.Merge("c2", vibekit.ChatStatusPayload{Status: vibekit.ChatStatusWaitingOnUser, Description: "needs a decision"})
	c.ClearAtTurnEnd("c2")
	if got := c.Get("c2"); got.Status != vibekit.ChatStatusWaitingOnUser || got.Description != "needs a decision" {
		t.Errorf("got %+v, want waiting_on_user retained whole past turn end", got)
	}
	// Every other status goes, which is what keeps a finished turn's label off a
	// later connect.
	c.Merge("c3", vibekit.ChatStatusPayload{Status: "in_progress", Description: "reading files"})
	c.ClearAtTurnEnd("c3")
	if got := c.Get("c3"); got.Status != "" {
		t.Errorf("status %q survived turn end; only waiting_on_user is retained", got.Status)
	}

	// An empty chat id is ignored rather than creating a junk entry: global
	// events carry no chat.
	c.Merge("", vibekit.ChatStatusPayload{Status: "in_progress"})
	if got := c.Get(""); got.Status != "" {
		t.Error("an empty chat id was recorded")
	}

	// A both-empty payload is the discharge's own frame: it must leave no entry, or
	// every chat that ever discharged keeps a phantom the retention rule then has to
	// reason about. Get cannot see one (an absent key and a {"",""} value both read as
	// the zero payload), so Snapshot is the only discriminator. Hygiene rather than
	// correctness — every consumer of a phantom is inert — so this is a pin, not a
	// defect guard.
	c.Merge("c4", vibekit.ChatStatusPayload{Status: "in_progress", Description: "x"})
	c.Merge("c4", vibekit.ChatStatusPayload{})
	if _, ok := c.Snapshot()["c4"]; ok {
		t.Error("a both-empty Merge left a phantom entry; Get cannot see one, so assert through Snapshot")
	}
	// A status with no description is a real declaration and stays.
	c.Merge("c5", vibekit.ChatStatusPayload{Status: "in_progress"})
	if got := c.Get("c5"); got.Status != "in_progress" {
		t.Errorf("status-only Merge left %q, want in_progress", got.Status)
	}

	// ClearWaiting is NARROWER than Clear: it ends the retained claim and reports
	// whether one went, so the discharge cannot delete a status the running turn
	// declared.
	c.Merge("c6", vibekit.ChatStatusPayload{Status: vibekit.ChatStatusWaitingOnUser, Description: "needs a decision"})
	if !c.ClearWaiting("c6") {
		t.Error("ClearWaiting reported no claim for a retained waiting_on_user entry")
	}
	if got := c.Get("c6"); got.Status != "" {
		t.Errorf("status %q survived ClearWaiting", got.Status)
	}
	if c.ClearWaiting("c6") {
		t.Error("ClearWaiting reported a claim for a chat with no entry")
	}
	// An in_progress entry belongs to its turn, so the discharge leaves it whole:
	// clearing it would wipe the tab tooltip's "doing" half for the rest of the turn.
	c.Merge("c7", vibekit.ChatStatusPayload{Status: "in_progress", Description: "reading the parser"})
	if c.ClearWaiting("c7") {
		t.Error("ClearWaiting reported a claim for an in_progress entry")
	}
	if got := c.Get("c7"); got.Status != "in_progress" || got.Description != "reading the parser" {
		t.Errorf("got %+v, want the in_progress entry left whole", got)
	}
}

// TestDischargeWaiting_BroadcastsTheClear covers the half a bare cache delete leaves
// undone: a second connected device holds the amber dot until its next message_chunk,
// a reconnect or a gap, so the discharge publishes an empty chat_status too. The empty
// payload is only true when the retained CLAIM went, which is what the third case pins.
func TestDischargeWaiting_BroadcastsTheClear(t *testing.T) {
	t.Run("a retained claim is cleared and broadcast", func(t *testing.T) {
		rt, _, _ := newTestHub()
		rt.bus.chatStatus.Merge("c1", vibekit.ChatStatusPayload{
			Status:      vibekit.ChatStatusWaitingOnUser,
			Description: "waiting on the user to disposition both proposals",
		})
		_, head := rt.bus.fanout.Bounds()

		rt.DischargeWaiting(t.Context(), "c1")

		if got := rt.bus.chatStatus.Get("c1"); got.Status != "" {
			t.Errorf("status %q survived the discharge", got.Status)
		}
		got := chatStatusFrames(t, bufferedSince(rt, head))
		if len(got) != 1 {
			t.Fatalf("published %d chat_status frames, want 1: %+v", len(got), got)
		}
		if got[0].Status != "" || got[0].Description != "" {
			t.Errorf("frame = %+v, want both fields empty so setAgentStatus deletes them", got[0])
		}
	})

	t.Run("a chat with no entry publishes nothing", func(t *testing.T) {
		rt, _, _ := newTestHub()
		_, head := rt.bus.fanout.Bounds()

		rt.DischargeWaiting(t.Context(), "c1")

		if got := chatStatusFrames(t, bufferedSince(rt, head)); len(got) != 0 {
			t.Errorf("published %d chat_status frames for a chat with no claim, want 0: %+v", len(got), got)
		}
	})

	t.Run("a live in_progress entry is left alone", func(t *testing.T) {
		rt, _, _ := newTestHub()
		live := vibekit.ChatStatusPayload{Status: "in_progress", Description: "reading the parser"}
		rt.bus.chatStatus.Merge("c1", live)
		_, head := rt.bus.fanout.Bounds()

		rt.DischargeWaiting(t.Context(), "c1")

		if got := rt.bus.chatStatus.Get("c1"); got != live {
			t.Errorf("entry = %+v, want %+v: the running turn declared it", got, live)
		}
		if got := chatStatusFrames(t, bufferedSince(rt, head)); len(got) != 0 {
			t.Errorf("published %d chat_status frames over a live declaration, want 0: %+v", len(got), got)
		}
	})
}

// TestChatStatusMerge_OmitIsUnchanged is EXHAUSTIVE over the truth table because the rule
// IS a truth table, and a table with holes is what let a partial declaration destroy a
// retained claim. The four values are DISTINCT so every row discriminates WHICH side
// supplied each field; reusing one status or one description leaves the table exhaustive
// over emptiness and blind to precedence.
func TestChatStatusMerge_OmitIsUnchanged(t *testing.T) {
	const (
		prevStatus = vibekit.ChatStatusWaitingOnUser
		prevDesc   = "d1"
		nextStatus = "idle"
		nextDesc   = "d2"
	)
	for _, tc := range []struct {
		name                        string
		prevS, prevD, nextS, nextD  bool
		wantStatus, wantDescription string
	}{
		{name: "absent prev, empty next", wantStatus: "", wantDescription: ""},
		{name: "absent prev, description only", nextD: true, wantStatus: "", wantDescription: nextDesc},
		{name: "absent prev, status only", nextS: true, wantStatus: nextStatus, wantDescription: ""},
		{name: "absent prev, both", nextS: true, nextD: true, wantStatus: nextStatus, wantDescription: nextDesc},

		{name: "description-only prev, empty next", prevD: true, wantStatus: "", wantDescription: ""},
		{name: "description-only prev, description only", prevD: true, nextD: true, wantStatus: "", wantDescription: nextDesc},
		{name: "description-only prev, status only", prevD: true, nextS: true, wantStatus: nextStatus, wantDescription: prevDesc},
		{name: "description-only prev, both", prevD: true, nextS: true, nextD: true, wantStatus: nextStatus, wantDescription: nextDesc},

		{name: "status-only prev, empty next", prevS: true, wantStatus: "", wantDescription: ""},
		{name: "status-only prev, description only", prevS: true, nextD: true, wantStatus: prevStatus, wantDescription: nextDesc},
		{name: "status-only prev, status only", prevS: true, nextS: true, wantStatus: nextStatus, wantDescription: ""},
		{name: "status-only prev, both", prevS: true, nextS: true, nextD: true, wantStatus: nextStatus, wantDescription: nextDesc},

		{name: "both prev, empty next", prevS: true, prevD: true, wantStatus: "", wantDescription: ""},
		{name: "both prev, description only", prevS: true, prevD: true, nextD: true, wantStatus: prevStatus, wantDescription: nextDesc},
		{name: "both prev, status only", prevS: true, prevD: true, nextS: true, wantStatus: nextStatus, wantDescription: prevDesc},
		{name: "both prev, both", prevS: true, prevD: true, nextS: true, nextD: true, wantStatus: nextStatus, wantDescription: nextDesc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh cache per row: Merge is the cache's only writer, so on a shared one
			// row N's stored entry becomes row N+1's prev and every row after the first
			// asserts against a prev it did not choose.
			c := newChatStatusCache()
			if tc.prevS || tc.prevD {
				seed := vibekit.ChatStatusPayload{}
				if tc.prevS {
					seed.Status = prevStatus
				}
				if tc.prevD {
					seed.Description = prevDesc
				}
				c.Merge("c1", seed)
			}
			next := vibekit.ChatStatusPayload{}
			if tc.nextS {
				next.Status = nextStatus
			}
			if tc.nextD {
				next.Description = nextDesc
			}

			got := c.Merge("c1", next)

			if got.Status != tc.wantStatus || got.Description != tc.wantDescription {
				t.Errorf("Merge returned %+v, want {%q %q}", got, tc.wantStatus, tc.wantDescription)
			}
			if !tc.nextS && !tc.nextD {
				// Get cannot tell an absent key from a stored {"",""}, and a phantom entry
				// is what these rows exist to catch.
				if n := len(c.Snapshot()); n != 0 {
					t.Errorf("a both-empty next left %d entries, want 0: %+v", n, c.Snapshot())
				}
				return
			}
			if stored := c.Get("c1"); stored.Status != tc.wantStatus || stored.Description != tc.wantDescription {
				t.Errorf("stored entry = %+v, want {%q %q}", stored, tc.wantStatus, tc.wantDescription)
			}
		})
	}

	t.Run("an empty chat id publishes the declaration unchanged", func(t *testing.T) {
		c := newChatStatusCache()
		declared := vibekit.ChatStatusPayload{Status: "in_progress", Description: "reading files"}

		got := c.Merge("", declared)

		if got != declared {
			t.Errorf("Merge returned %+v, want the declaration %+v: emit publishes this", got, declared)
		}
		if n := len(c.Snapshot()); n != 0 {
			t.Errorf("an empty chat id recorded %d entries, want 0", n)
		}
	})
}

// TestEmitChatStatus_PublishesTheMergedPayload is the point of the whole change: the cache
// being right is not enough, because the client replaces both fields from the frame. The
// frame assertion is separate from the cache one on purpose.
func TestEmitChatStatus_PublishesTheMergedPayload(t *testing.T) {
	rt, _, _ := newTestHub()
	rt.bus.chatStatus.Merge("c1", vibekit.ChatStatusPayload{
		Status:      vibekit.ChatStatusWaitingOnUser,
		Description: "d1",
	})
	_, head := rt.bus.fanout.Bounds()

	rt.bus.Broadcast(t.Context(), vibekit.NewEvent(vibekit.EventChatStatus, "c1",
		vibekit.ChatStatusPayload{Description: "d2"}))

	if got := rt.bus.chatStatus.Get("c1"); got.Status != vibekit.ChatStatusWaitingOnUser || got.Description != "d2" {
		t.Errorf("entry = %+v, want {waiting_on_user d2}: the omitted status means unchanged", got)
	}
	frames := chatStatusFrames(t, bufferedSince(rt, head))
	if len(frames) != 1 {
		t.Fatalf("published %d chat_status frames, want 1: %+v", len(frames), frames)
	}
	if frames[0].Status != vibekit.ChatStatusWaitingOnUser || frames[0].Description != "d2" {
		t.Errorf("frame = %+v, want {waiting_on_user d2}: setAgentStatus deletes agent_status on an empty status", frames[0])
	}
}

// TestEmitChatStatus_StatusOnlyKeepsTheDescription is the mirror, and it also pins that a
// real declaration still discharges the dot: the status moves off waiting_on_user, which is
// tabStatusFor's own input.
func TestEmitChatStatus_StatusOnlyKeepsTheDescription(t *testing.T) {
	rt, _, _ := newTestHub()
	rt.bus.chatStatus.Merge("c1", vibekit.ChatStatusPayload{
		Status:      vibekit.ChatStatusWaitingOnUser,
		Description: "d1",
	})
	_, head := rt.bus.fanout.Bounds()

	rt.bus.Broadcast(t.Context(), vibekit.NewEvent(vibekit.EventChatStatus, "c1",
		vibekit.ChatStatusPayload{Status: "idle"}))

	got := rt.bus.chatStatus.Get("c1")
	if got.Status != "idle" || got.Description != "d1" {
		t.Errorf("entry = %+v, want {idle d1}: the omitted description means unchanged", got)
	}
	if got.Status == vibekit.ChatStatusWaitingOnUser {
		t.Error("the status stayed waiting_on_user, so the dot never discharges on a real declaration")
	}
	frames := chatStatusFrames(t, bufferedSince(rt, head))
	if len(frames) != 1 {
		t.Fatalf("published %d chat_status frames, want 1: %+v", len(frames), frames)
	}
	if frames[0].Status != "idle" || frames[0].Description != "d1" {
		t.Errorf("frame = %+v, want {idle d1}", frames[0])
	}
}

// TestEmitChatStatus_DoesNotStageAMergedDescription pins the raw-versus-merged split. The
// turn and the wire answer different questions: Turn.statusDesc is what the agent declared
// during THIS turn, so feeding it the merge would put a previous turn's words in this
// turn's push body.
func TestEmitChatStatus_DoesNotStageAMergedDescription(t *testing.T) {
	rt, _, _ := newTestHub()
	rt.bus.chatStatus.Merge("c1", vibekit.ChatStatusPayload{
		Status:      vibekit.ChatStatusWaitingOnUser,
		Description: "d1",
	})
	// A source UserAnswered() excludes, so the retention survives the open.
	if epoch := rt.StartTurn(t.Context(), "c1", vibekit.TurnSourceWorkflowStep); epoch == 0 {
		t.Fatal("the fixture could not open a step turn")
	}

	rt.bus.Broadcast(t.Context(), vibekit.NewEvent(vibekit.EventChatStatus, "c1",
		vibekit.ChatStatusPayload{Status: "in_progress"}))

	// Read the field the way statusDescription does. Not through claimOpen/claimEpoch:
	// both end in claimLocked, which moves the chat into turnFinalizing and changes the
	// state under assertion.
	lc, ok := rt.coord.turns.lookup("c1")
	if !ok {
		t.Fatal("no chat lifecycle for c1")
	}
	lc.mu.Lock()
	staged := lc.cur.statusDesc
	lc.mu.Unlock()

	if staged != "" {
		t.Errorf("staged description = %q, want empty: the declaration carried none, so the merge must not reach the turn", staged)
	}
}

// chatStatusFrames decodes the chat_status payloads out of a replay slice.
func chatStatusFrames(t *testing.T, events []sse.ReplayEvent) []vibekit.ChatStatusPayload {
	t.Helper()
	var out []vibekit.ChatStatusPayload
	for _, e := range events {
		var msg struct {
			Type    vibekit.EventType         `json:"type"`
			Payload vibekit.ChatStatusPayload `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &msg); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if msg.Type == vibekit.EventChatStatus {
			out = append(out, msg.Payload)
		}
	}
	return out
}

// TestStartTurn_DischargesTheWaitingRetention covers the counterpart to
// ClearAtTurnEnd's retention: waiting_on_user outlives its turn on purpose, so
// something has to end that window or the amber dot describes a question the user
// answered hours ago. A prompt IS that answer; a run's step turn is not.
func TestStartTurn_DischargesTheWaitingRetention(t *testing.T) {
	waiting := vibekit.ChatStatusPayload{
		Status:      vibekit.ChatStatusWaitingOnUser,
		Description: "waiting on the user to disposition both proposals",
	}

	t.Run("a prompt clears it", func(t *testing.T) {
		rt, _, _ := newTestHub()
		rt.bus.chatStatus.Merge("c1", waiting)
		_, head := rt.bus.fanout.Bounds()

		if epoch := rt.StartTurn(t.Context(), "c1", vibekit.TurnSourcePrompt); epoch == 0 {
			t.Fatal("the fixture could not open a prompt turn")
		}
		if got := rt.bus.chatStatus.Get("c1"); got.Status != "" {
			t.Errorf("status %q survived the prompt that answered it, so a reconnect repaints the dot", got.Status)
		}
		// A second device converges on the frame rather than on its next message_chunk.
		if got := chatStatusFrames(t, bufferedSince(rt, head)); len(got) != 1 {
			t.Errorf("the prompt published %d chat_status frames, want 1: %+v", len(got), got)
		}
	})

	t.Run("a workflow step does not", func(t *testing.T) {
		rt, _, _ := newTestHub()
		rt.bus.chatStatus.Merge("c1", waiting)

		if epoch := rt.StartTurn(t.Context(), "c1", vibekit.TurnSourceWorkflowStep); epoch == 0 {
			t.Fatal("the fixture could not open a step turn")
		}
		got := rt.bus.chatStatus.Get("c1")
		if got.Status != vibekit.ChatStatusWaitingOnUser {
			t.Errorf("status is %q, want %q: a run's step is not the user answering", got.Status, vibekit.ChatStatusWaitingOnUser)
		}
		if got.Description != waiting.Description {
			t.Errorf("description is %q, want %q", got.Description, waiting.Description)
		}
	})
}
