package hub

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/command"
)

// Which model served a turn.
//
// The trap this closes: Model lives on the Chat, not the Message, so a footer
// that read the session's CURRENT model at render time would relabel every
// historical turn the moment the user switched models. The value is therefore
// latched when the turn opens and stamped onto the finished message, and these
// tests pin both halves plus the absent case.

// turnEndedModel returns the model on the last turn_ended event, and whether the
// payload carried the field at all.
func turnEndedModel(t *testing.T, h *Hub) (model string, present bool) {
	t.Helper()
	for _, e := range bufferedSince(h, 0) {
		var msg struct {
			Type    api.EventType `json:"type"`
			Payload struct {
				Model *string `json:"model"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &msg); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if msg.Type != api.EventTurnEnded {
			continue
		}
		if msg.Payload.Model == nil {
			return "", false
		}
		return *msg.Payload.Model, true
	}
	t.Fatal("no turn_ended event was broadcast")
	return "", false
}

func endTurn(t *testing.T, h *Hub, chatID api.ChatID) {
	t.Helper()
	h.EmitTurnEndedWithStats(t.Context(), chatID,
		&api.RPCResponse{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}, command.TurnStats{})
}

func TestTurnModel_StampedOnThePersistedTurnAndOnTheSSE(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "sonnet-4"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	h.translateACPEvent("c1", newChunkMsg(t, "hello"))
	endTurn(t, h, "c1")

	c, _ := cs.Get(t.Context(), "c1")
	if len(c.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(c.Messages))
	}
	// Persisted, because the footer has to survive a reload: turn_ended is not
	// replayed, so a live-only value would vanish on refresh.
	if got := c.Messages[0].TurnModel; got != "sonnet-4" {
		t.Errorf("persisted TurnModel = %q, want %q", got, "sonnet-4")
	}
	// And live, because the footer renders before anything is re-fetched.
	model, present := turnEndedModel(t, h)
	if !present || model != "sonnet-4" {
		t.Errorf("turn_ended model = %q (present=%v), want %q", model, present, "sonnet-4")
	}
}

func TestTurnModel_AbsentWhenTheChatNamesNoModel(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A" // no Model: the session took the backend default
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	h.translateACPEvent("c1", newChunkMsg(t, "hello"))
	endTurn(t, h, "c1")

	c, _ := cs.Get(t.Context(), "c1")
	if got := c.Messages[0].TurnModel; got != "" {
		t.Errorf("TurnModel = %q, want empty for an unknowable model", got)
	}
	// omitempty: the field must be ABSENT rather than "", so the client renders
	// nothing instead of an empty attribution.
	if _, present := turnEndedModel(t, h); present {
		t.Error("turn_ended carried a model field for a chat that names no model")
	}
}

// TestTurnModel_LatchedAtTurnStartNotAtTurnEnd is the whole reason the value
// lives on the buffer. Reading the chat at turn END would attribute a turn to
// whatever model happened to be current when it finished, which is exactly the
// relabelling the persisted field exists to prevent — one level down.
func TestTurnModel_LatchedAtTurnStartNotAtTurnEnd(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "sonnet-4"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	h.translateACPEvent("c1", newChunkMsg(t, "half an answer"))
	// A switch lands mid-turn (the fast in-session path does exactly this).
	if err := cs.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool {
		c.Model = "opus-4"
		return true
	}); err != nil {
		t.Fatalf("switch model: %v", err)
	}
	h.translateACPEvent("c1", newChunkMsg(t, " continued"))
	endTurn(t, h, "c1")

	c, _ := cs.Get(t.Context(), "c1")
	if got := c.Messages[0].TurnModel; got != "sonnet-4" {
		t.Errorf("TurnModel = %q, want %q — the model that was running when the "+
			"turn opened, not the one current when it ended", got, "sonnet-4")
	}
}

// TestTurnModel_SwitchBeforeTheFirstFrameKeepsTheDispatchedModel is the half the
// test above cannot reach: it switches AFTER a chunk, so the buffer has already
// latched by the time the model changes.
//
// A turn opens on its first assistant FRAME, which can arrive seconds after the
// prompt was dispatched, and the fast switch path needs neither the turn nor the
// prompt slot. So a switch landing inside that window reached the chat record
// first and the frame-time read stamped the newly selected model onto an answer
// the previous one produced — persisted, and silent, because every assertion
// about a mid-turn switch was written for a switch that arrives after a chunk.
//
// Driven end to end rather than by poking the latch, because the fix IS the call
// site: the prompt blocks inside the bridge Call (which is where a real turn
// spends its time), the switch lands while it is blocked, and only then does the
// first frame arrive.
func TestTurnModel_SwitchBeforeTheFirstFrameKeepsTheDispatchedModel(t *testing.T) {
	h, cs, br := newTestHub()
	// The session reports the model it is running, and spawnBridge writes that
	// back onto the chat — so the fixture has to agree with itself or the chat's
	// model at dispatch is the fake's placeholder rather than the seeded one.
	br.modelID = "sonnet-4"
	if err := cs.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "sonnet-4"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	// Hold the prompt inside the bridge Call so the switch and the first frame
	// land in the window a real turn spends waiting on the model.
	unblock := make(chan struct{})
	br.blockOn = map[string]chan struct{}{api.MethodPrompt: unblock}

	done := make(chan struct{})
	go func() {
		defer close(done)
		postCmd(t, h, api.ClientCommand{
			Type: api.CmdPrompt, RequestID: "r-prompt", ChatID: "c1",
			Payload: json.RawMessage(`{"text":"hi","message_id":"m-1"}`),
		})
	}()
	waitForCall(t, br, api.MethodPrompt)

	// The fast in-session switch: no turn needed, no prompt slot taken.
	if rec := postCmd(t, h, api.ClientCommand{
		Type: api.CmdSwitchModel, RequestID: "r-switch", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"opus-4"}`),
	}); rec.Code != http.StatusOK {
		t.Fatalf("switch_model = %d, body %s", rec.Code, rec.Body.String())
	}
	if c, _ := cs.Get(t.Context(), "c1"); c.Model != "opus-4" {
		t.Fatalf("setup: chat model = %q, want the switch to have landed", c.Model)
	}

	// Only now does the model that is actually answering emit its first frame.
	h.translateACPEvent("c1", newChunkMsg(t, "the previous model's answer"))
	close(unblock)
	<-done

	c, _ := cs.Get(t.Context(), "c1")
	var found bool
	for i := range c.Messages {
		if c.Messages[i].Role != api.RoleAssistant {
			continue
		}
		found = true
		if got := c.Messages[i].TurnModel; got != "sonnet-4" {
			t.Errorf("TurnModel = %q, want %q — the model the prompt was DISPATCHED "+
				"under, not the one a switch installed before the first frame",
				got, "sonnet-4")
		}
	}
	if !found {
		t.Fatal("no assistant message was persisted for the turn")
	}
}

// waitForCall blocks until the bridge has recorded a Call to method. A
// deadline-bounded poll rather than a sleep: it fails closed with a diagnostic
// and cannot flake into a false pass by asserting against a call that never
// happened.
func waitForCall(t *testing.T, br *fakeBridge, method string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if slices.Contains(br.callLog(), method) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("bridge never received %s; calls = %v", method, br.callLog())
		}
		time.Sleep(time.Millisecond)
	}
}

// TestTurnModel_AbandonedTurnCarriesItToo covers the SECOND caller of
// assistantTurnMessage. The constructor was extracted precisely so the
// interrupted path cannot drift from the normal one, so every field it stamps
// needs a case on both paths or the extraction stops earning its keep.
func TestTurnModel_AbandonedTurnCarriesItToo(t *testing.T) {
	h, cs, _ := newTestHub()
	if err := cs.Mutate(t.Context(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "sonnet-4"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	h.translateACPEvent("c1", newChunkMsg(t, "the model got this far"))
	h.AbandonInFlightTurn(t.Context(), "c1")

	c, _ := cs.Get(t.Context(), "c1")
	var found bool
	for i := range c.Messages {
		if c.Messages[i].Role == api.RoleAssistant {
			found = true
			if got := c.Messages[i].TurnModel; got != "sonnet-4" {
				t.Errorf("abandoned turn TurnModel = %q, want %q", got, "sonnet-4")
			}
		}
	}
	if !found {
		t.Fatal("no assistant message was persisted for the abandoned turn")
	}
}
