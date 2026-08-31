package agent

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// --- switch_model ---

func TestSwitchModel_MissingChatID(t *testing.T) {
	h, _, _ := newTestHub()
	rec := postCmd(t, h, vibekit.ClientCommand{Type: "switch_model"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestSwitchModel_ChatNotFound(t *testing.T) {
	h, _, _ := newTestHub()
	rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "switch_model", ChatID: "nope",
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
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.ACPSessionID = "old-acp"
		c.Model = "m-old"
		return true
	})

	rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"m-new"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	c, _ := cs.Get(t.Context(), "c1")
	// session/load succeeded: acp_session_id is preserved (same session
	// reloaded with new model), bridge is primed (no transcript replay).
	if c.ACPSessionID == "" {
		t.Errorf("acp_session_id was cleared, want preserved for fast path")
	}
	if len(c.Messages) != 1 || c.Messages[0].EventKind != vibekit.EventModelSwitched {
		t.Errorf("expected model_switched event, got %+v", c.Messages)
	}
	sb := h.coord.Bridge("c1")
	if sb == nil {
		t.Fatal("no bridge after switch")
	}
	if !sb.primed {
		t.Errorf("bridge.primed = false, want true (session/load restores context)")
	}
}

func TestSwitchModel_WithModelOverride(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "claude-opus"
		c.ACPSessionID = "old-acp"
		return true
	})

	rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"claude-sonnet"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	c, _ := cs.Get(t.Context(), "c1")
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
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.ACPSessionID = "old"
		c.Model = "m-old"
		c.Usage = vibekit.Usage{
			ContextSize: 200000, ContextPct: 80, Credits: 1.23, TurnCount: 12,
		}
		return true
	})

	_ = postCmd(t, h, vibekit.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"m-new"}`),
	})

	c, _ := cs.Get(t.Context(), "c1")
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
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.ACPSessionID = "old"
		c.Model = "m-same"
		c.Usage = vibekit.Usage{
			ContextSize: 200000, ContextPct: 80, Credits: 1.23, TurnCount: 12,
		}
		return true
	})

	// Empty payload: bare restart.
	rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "switch_model", ChatID: "c1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	c, _ := cs.Get(t.Context(), "c1")
	if len(c.Messages) != 0 {
		t.Errorf("bare restart should not emit model_switched event, got %+v", c.Messages)
	}
	if c.Usage.Credits != 1.23 || c.Usage.TurnCount != 12 {
		t.Errorf("bare restart reset counters: %+v", c.Usage)
	}

	// Same-model payload: also bare restart, same invariants.
	rec2 := postCmd(t, h, vibekit.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"m-same"}`),
	})
	if rec2.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec2.Code, rec2.Body.String())
	}
	c2, _ := cs.Get(t.Context(), "c1")
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
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		return true
	})

	rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"bad<script>"}`),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}

	c, _ := cs.Get(t.Context(), "c1")
	if c.Model != "m-old" {
		t.Errorf("chat.Model = %q, want unchanged m-old", c.Model)
	}
	if len(c.Messages) != 0 {
		t.Errorf("no event should be persisted on validation failure, got %+v", c.Messages)
	}
}

// Fast path: in-session model switch (set_config_option) succeeds, bridge stays alive.
func TestSwitchModel_FastPath_SetModelSucceeds(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "old-model"
		return true
	})
	// Create a bridge first so the fast path has something to call.
	sb, err := h.coord.OpenBridge(t.Context(), "c1", "old-model")
	if err != nil {
		t.Fatalf("getOrCreateBridge: %v", err)
	}
	fb := sb.bridge.(*fakeBridge)
	origSessionID := fb.SessionID()

	rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"new-model"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	// The bridge should still be the same instance (no restart).
	sb2 := h.coord.Bridge("c1")
	if sb2 == nil {
		t.Fatal("bridge gone after fast-path switch")
	}
	fb2 := sb2.bridge.(*fakeBridge)
	if fb2.SessionID() != origSessionID {
		t.Errorf("session id changed: %q → %q (bridge was restarted, fast path failed)",
			origSessionID, fb2.SessionID())
	}
	// The fake bridge should have received an in-session model switch
	// (v3 session/set_config_option, configId "model").
	fb2.mu.Lock()
	calls := append([]string(nil), fb2.calls...)
	fb2.mu.Unlock()
	found := false
	for _, c := range calls {
		if c == "session/set_config_option" {
			found = true
		}
	}
	if !found {
		t.Errorf("session/set_config_option not called on bridge; calls = %v", calls)
	}
	// Chat model should be updated.
	c, _ := cs.Get(t.Context(), "c1")
	if c.Model != "new-model" {
		t.Errorf("chat.Model = %q, want new-model", c.Model)
	}
}

// TestSwitchModel_RefusesAModelTheAccountDoesNotServe pins the entitlement gate.
//
// kiro-cli accepts a model id it cannot serve: the set_config_option succeeds and
// only the SERVICE rejects it, mid-prompt, on every later turn. Before this gate
// the id reached the wire, the fast path failed, and the fallback then tore down a
// working bridge to respawn on the same rejected id.
func TestSwitchModel_RefusesAModelTheAccountDoesNotServe(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		c.ServedModelIDs = []string{"m-old", "m-other"}
		return true
	})

	rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"m-unentitled"}`),
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409; body = %s", rec.Code, rec.Body.String())
	}

	// Nothing changed: the refusal must not persist the model, and must not tear
	// down or spawn a bridge on the way to failing.
	c, _ := cs.Get(t.Context(), "c1")
	if c.Model != "m-old" {
		t.Errorf("chat.Model = %q, want the previous model preserved", c.Model)
	}
	if sb := h.coord.Bridge("c1"); sb != nil {
		t.Error("a bridge was created for a refused switch")
	}
}

// TestSwitchModel_AllowsWhenEntitlementIsUnknowable pins both fail-open cases,
// which are the ones that would turn this gate into an outage: a backend that
// advertises no catalog must behave exactly as it did before the gate existed.
func TestSwitchModel_AllowsWhenEntitlementIsUnknowable(t *testing.T) {
	cases := []struct {
		name   string
		served []string
	}{
		{"no advertised set at all", nil},
		{"an empty advertised set", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
				c.Name = "A"
				c.Model = "m-old"
				c.ServedModelIDs = tc.served
				return true
			})
			rec := postCmd(t, h, vibekit.ClientCommand{
				Type: "switch_model", ChatID: "c1",
				Payload: json.RawMessage(`{"model":"m-anything"}`),
			})
			if rec.Code != http.StatusOK {
				t.Fatalf("code = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestSwitchModel_AllowsADeprecatedModelTheAccountStillServes is the case that
// decides whether this gate was safe to add at all.
//
// The picker's list (chat.AvailableModels, Bridge.Models) drops [Deprecated] and
// [Legacy] entries for display. Validating against THAT list would refuse a model
// the account can still run, converting a working session into a client-side
// refusal — worse than the defect the gate prevents. The gate must read the
// unfiltered served set, so a deprecated id present there is allowed.
func TestSwitchModel_AllowsADeprecatedModelTheAccountStillServes(t *testing.T) {
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		// The display list omits it; the served set does not. That divergence is
		// exactly what applyModelConfigOptionLocked produces.
		c.AvailableModels = []vibekit.SessionModel{{ID: "m-old", Name: "Old"}}
		c.ServedModelIDs = []string{"m-old", "m-deprecated"}
		return true
	})

	rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"m-deprecated"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (a deprecated model is hidden, not unentitled); body = %s",
			rec.Code, rec.Body.String())
	}
}

// TestSwitchModel_TheLiveSessionsSetOutranksTheChatsRecord pins which evidence the
// entitlement gate believes when both exist, in both directions.
//
// The chat's recorded set is a snapshot from whenever it was last written, so it
// goes stale the moment the account's entitlements change — a model added to the
// plan would stay refused until the record caught up. The live session's set is the
// current answer, and it only counts when the session actually advertised one:
// treating an empty advertisement as authoritative would replace the gate with a
// pass-through, which is the outage the fail-open case exists to avoid.
func TestSwitchModel_TheLiveSessionsSetOutranksTheChatsRecord(t *testing.T) {
	cases := []struct {
		name     string
		recorded []string
		live     []string
		model    string
		wantCode int
	}{
		{
			name:     "a live session's newer set admits a model the record has not seen",
			recorded: []string{"m-old"},
			live:     []string{"m-old", "m-new"},
			model:    "m-new",
			wantCode: http.StatusOK,
		},
		{
			name:     "a session advertising nothing leaves the record in charge",
			recorded: []string{"m-old"},
			live:     nil,
			model:    "m-unentitled",
			wantCode: http.StatusConflict,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, br := newTestHub()
			br.servedModels = tc.live
			if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
				c.Name = "A"
				c.Model = "m-old"
				c.ServedModelIDs = tc.recorded
				return true
			}); err != nil {
				t.Fatalf("seed the chat: %v", err)
			}
			// A LIVE bridge is what makes this the two-evidence case at all.
			h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})

			rec := postCmd(t, h, vibekit.ClientCommand{
				Type: "switch_model", ChatID: "c1",
				Payload: json.RawMessage(`{"model":"` + tc.model + `"}`),
			})
			if rec.Code != tc.wantCode {
				t.Errorf("switch to %q with recorded=%v live=%v: code = %d, want %d; body = %s",
					tc.model, tc.recorded, tc.live, rec.Code, tc.wantCode, rec.Body.String())
			}
		})
	}
}

// The switch-by-restart fallback must land the pick on the RESUMED session.
//
// session/load restores KAS's own persisted model and the session/new door does
// not run on that path, so the swap had to be retried or it silently did not
// happen: PersistModelSwitch wrote the new id onto the chat while the old model
// kept answering, and the load's config_option_update then raced that write back
// to the old id, so the pill snapped back and the pick was lost.
func TestSwitchModel_RestartFallback_AppliesThePickToTheResumedSession(t *testing.T) {
	h, cs, br := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.ACPSessionID = "old-acp"
		c.Model = "m-old"
		// A tier chosen under m-old: it must NOT ride onto m-new (user report,
		// 2026-08-31) — the switch resolves against the TARGET model instead.
		c.Effort = "max"
		c.AvailableModels = []vibekit.SessionModel{{ID: "m-new", DefaultEffortLevel: "high"}}
		return true
	})
	// A LIVE bridge, because the fast path returns early when there is none and
	// would consume no failure: the fallback has to be reached by a swap that was
	// actually refused, not by an absent bridge.
	if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	// One failure: the fast path fails against the live session, the restart
	// reopens over session/load, and the retry on the new bridge succeeds.
	br.mu.Lock()
	br.setModelFailures = 1
	br.effort = ""
	br.mu.Unlock()

	rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"m-new"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	if got := br.ModelID(); got != "m-new" {
		t.Errorf("resumed session model = %q, want %q; the restart fallback did not apply the pick", got, "m-new")
	}
	// And a level rides with it, because a swap can reset it: the TARGET model's
	// own default, never the choice made under the old model.
	if got := br.lastEffort(); got != "high" {
		t.Errorf("resumed session effort = %q, want %q (m-new's default; the m-old choice must not leak)", got, "high")
	}
}

// A fresh session gets no retry: the model and the level already rode _meta.kiro
// on session/new, so re-sending them would be two round trips that change
// nothing.
//
// A chat that has never had a bridge is the only way to reach that branch. Opening
// one stamps its session id onto the record (persistNewSessionMetadata), after
// which every reopen is a session/load.
// A chat that has never had a bridge NOR a session reaches this branch only with
// history on the record (a `!cmd` shell chat): a truly empty chat takes the
// pre-session persist instead (TestSwitchModel_PreSessionPickPersistsWithoutABridge).
func TestSwitchModel_RestartFallback_SendsNoRetryOnAFreshSession(t *testing.T) {
	h, cs, br := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		// Chosen under m-old, so the fresh spawn resolves against the TARGET:
		// its catalog default, never this value.
		c.Effort = "max"
		c.AvailableModels = []vibekit.SessionModel{{ID: "m-new", DefaultEffortLevel: "medium"}}
		// History with no session id: the shell-intercept shape, which is what
		// makes the switch spawn a FRESH session rather than persist-and-return.
		c.Messages = []vibekit.Message{{ID: "m1", Role: vibekit.RoleUser, Content: "!ls"}}
		return true
	})

	rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"m-new"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	if got := br.lastEffort(); got != "" {
		t.Errorf("effort re-applied = %q, want none on a fresh session: the session door already carried it", got)
	}
	// The level still reached the session, on the door rather than on a retry —
	// resolved against the TARGET model (its default), not the old chat choice.
	opts := br.lastStartOpts()
	if opts == nil || opts.Effort != "medium" {
		t.Errorf("StartOpts.Effort = %v, want medium (m-new's default) on the fresh spawn", opts)
	}
}

// A pick on a chat that has never run is a PREFERENCE: it persists on the record
// with no bridge, no session and no event row, so the header echo carries the
// pick and a later set_effort auto-persist cannot clobber it back (user report,
// 2026-08-31: picking an effort after picking a model reverted the model).
func TestSwitchModel_PreSessionPickPersistsWithoutABridge(t *testing.T) {
	h, cs, br := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Model = "m-old"
		// A tier chosen before the model pick was chosen under m-old; the pick
		// clears it so resolution falls to the new model's own default.
		c.Effort = "max"
		return true
	})

	rec := postCmd(t, h, vibekit.ClientCommand{
		Type: "switch_model", ChatID: "c1",
		Payload: json.RawMessage(`{"model":"m-new"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	chat, ok := cs.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat gone after the pick")
	}
	if chat.Model != "m-new" {
		t.Errorf("chat.Model = %q, want m-new persisted on the record", chat.Model)
	}
	if chat.Effort != "" {
		t.Errorf("chat.Effort = %q, want cleared: the tier was chosen under m-old", chat.Effort)
	}
	if n := len(chat.Messages); n != 0 {
		t.Errorf("messages = %d, want 0: a pre-session pick is not a switch event", n)
	}
	if opts := br.lastStartOpts(); opts != nil {
		t.Errorf("a bridge was spawned for a pre-session pick: StartOpts = %+v", opts)
	}
}
