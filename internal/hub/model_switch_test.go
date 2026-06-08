package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// --- switch_model ---

func TestSwitchModel_MissingChatID(t *testing.T) {
	h, _, _ := newTestHub()
	rec := postCmd(t, h, api.ClientCommand{Type: "switch_model", RequestID: "r1"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestSwitchModel_ChatNotFound(t *testing.T) {
	h, _, _ := newTestHub()
	rec := postCmd(t, h, api.ClientCommand{
		Type: "switch_model", RequestID: "r1", ChatID: "nope",
	})
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
}

// Fast path: session/load succeeds, context preserved, no priming.
// The command carries a new model so isSwitch=true; without an
// override the command is a bare-restart and does NOT emit a
// model_switched event (see TestSwitchModel_BareRestart_NoEvent).
func TestSwitchModel_FastPath_SessionLoadSucceeds(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.ACPSessionID = "old-acp"
		c.Model = "m-old"
		return true
	})

	rec := postCmd(t, h, api.ClientCommand{
		Type: "switch_model", RequestID: "r1", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"m-new"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	c, _ := cs.Get(context.Background(), "c1")
	// session/load succeeded: acp_session_id is preserved (same session
	// reloaded with new model), bridge is primed (no transcript replay).
	if c.ACPSessionID == "" {
		t.Errorf("acp_session_id was cleared, want preserved for fast path")
	}
	if len(c.Messages) != 1 || c.Messages[0].EventKind != api.EventModelSwitched {
		t.Errorf("expected model_switched event, got %+v", c.Messages)
	}
	sb := h.coord.GetBridge("c1")
	if sb == nil {
		t.Fatal("no bridge after switch")
	}
	if !sb.primed {
		t.Errorf("bridge.primed = false, want true (session/load restores context)")
	}
}

func TestSwitchModel_WithModelOverride(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "claude-opus"
		c.ACPSessionID = "old-acp"
		return true
	})

	rec := postCmd(t, h, api.ClientCommand{
		Type: "switch_model", RequestID: "r1", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"claude-sonnet"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	c, _ := cs.Get(context.Background(), "c1")
	// The model on the chat should have changed from the override.
	if c.Model == "claude-opus" {
		t.Errorf("chat.Model = %q, want it to change from claude-opus", c.Model)
	}
}

// TestSwitchModel_PreservesContextSize: switching to a new model
// preserves the chat's context_size while resetting credit counters.
// Uses an explicit model override so isSwitch=true (no-op same-model
// commands don't touch Usage — see TestSwitchModel_BareRestart_NoEvent).
func TestSwitchModel_PreservesContextSize(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.ACPSessionID = "old"
		c.Model = "m-old"
		c.Usage = api.Usage{
			ContextSize: 200000, ContextPct: 80, Credits: 1.23, TurnCount: 12,
		}
		return true
	})

	_ = postCmd(t, h, api.ClientCommand{
		Type: "switch_model", RequestID: "r1", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"m-new"}`),
	})

	c, _ := cs.Get(context.Background(), "c1")
	if c.Usage.ContextSize != 200000 {
		t.Errorf("context_size = %d, want 200000 (preserved)", c.Usage.ContextSize)
	}
	if c.Usage.ContextPct != 0 || c.Usage.Credits != 0 || c.Usage.TurnCount != 0 {
		t.Errorf("counters not reset: %+v", c.Usage)
	}
}

// TestSwitchModel_BareRestart_NoEvent: switch_model with no model or
// the same model restarts the bridge (wedged-session recovery) but
// must NOT emit a model_switched event and must NOT reset the Usage
// counters. Those side effects are reserved for a real model change.
func TestSwitchModel_BareRestart_NoEvent(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.ACPSessionID = "old"
		c.Model = "m-same"
		c.Usage = api.Usage{
			ContextSize: 200000, ContextPct: 80, Credits: 1.23, TurnCount: 12,
		}
		return true
	})

	// Empty payload: bare restart.
	rec := postCmd(t, h, api.ClientCommand{
		Type: "switch_model", RequestID: "r1", ChatID: "c1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	c, _ := cs.Get(context.Background(), "c1")
	if len(c.Messages) != 0 {
		t.Errorf("bare restart should not emit model_switched event, got %+v", c.Messages)
	}
	if c.Usage.Credits != 1.23 || c.Usage.TurnCount != 12 {
		t.Errorf("bare restart reset counters: %+v", c.Usage)
	}

	// Same-model payload: also bare restart, same invariants.
	rec2 := postCmd(t, h, api.ClientCommand{
		Type: "switch_model", RequestID: "r2", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"m-same"}`),
	})
	if rec2.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec2.Code, rec2.Body.String())
	}
	c2, _ := cs.Get(context.Background(), "c1")
	if len(c2.Messages) != 0 {
		t.Errorf("same-model restart should not emit model_switched event, got %+v", c2.Messages)
	}
	if c2.Usage.Credits != 1.23 || c2.Usage.TurnCount != 12 {
		t.Errorf("same-model restart reset counters: %+v", c2.Usage)
	}
}

// TestSwitchModel_RejectsInvalidModel: the model field is validated
// at the command boundary; a bad value returns 400 without mutating
// chat state. Previously a bad model landed in chat.Model via
// persistModelSwitch before bridge.Start rejected it downstream.
func TestSwitchModel_RejectsInvalidModel(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		return true
	})

	rec := postCmd(t, h, api.ClientCommand{
		Type: "switch_model", RequestID: "r1", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"bad<script>"}`),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}

	c, _ := cs.Get(context.Background(), "c1")
	if c.Model != "m-old" {
		t.Errorf("chat.Model = %q, want unchanged m-old", c.Model)
	}
	if len(c.Messages) != 0 {
		t.Errorf("no event should be persisted on validation failure, got %+v", c.Messages)
	}
}

// Fast path: session/set_model succeeds, bridge stays alive.
func TestSwitchModel_FastPath_SetModelSucceeds(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "old-model"
		return true
	})
	// Create a bridge first so the fast path has something to call.
	sb, err := h.coord.GetOrCreateBridge(context.Background(), "c1", "", "old-model")
	if err != nil {
		t.Fatalf("getOrCreateBridge: %v", err)
	}
	fb := sb.bridge.(*fakeBridge)
	origSessionID := fb.SessionID()

	rec := postCmd(t, h, api.ClientCommand{
		Type: "switch_model", RequestID: "r1", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"new-model"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	// The bridge should still be the same instance (no restart).
	sb2 := h.coord.GetBridge("c1")
	if sb2 == nil {
		t.Fatal("bridge gone after fast-path switch")
	}
	fb2 := sb2.bridge.(*fakeBridge)
	if fb2.SessionID() != origSessionID {
		t.Errorf("session id changed: %q → %q (bridge was restarted, fast path failed)",
			origSessionID, fb2.SessionID())
	}
	// The fake bridge should have received a session/set_model call.
	fb2.mu.Lock()
	calls := append([]string(nil), fb2.calls...)
	fb2.mu.Unlock()
	found := false
	for _, c := range calls {
		if c == "session/set_model" {
			found = true
		}
	}
	if !found {
		t.Errorf("session/set_model not called on bridge; calls = %v", calls)
	}
	// Chat model should be updated.
	c, _ := cs.Get(context.Background(), "c1")
	if c.Model != "new-model" {
		t.Errorf("chat.Model = %q, want new-model", c.Model)
	}
}
