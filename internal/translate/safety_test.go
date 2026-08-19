package translate

import (
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// safetyStatusPayloads collects every EventSafetyStatus payload broadcast.
func safetyStatusPayloads(t *testing.T, events *[]vibekit.ServerEvent) []vibekit.SafetyStatusPayload {
	t.Helper()
	var got []vibekit.SafetyStatusPayload
	for _, e := range *events {
		if e.Type != vibekit.EventSafetyStatus {
			continue
		}
		p, ok := e.Payload.(vibekit.SafetyStatusPayload)
		if !ok {
			t.Fatalf("EventSafetyStatus payload type = %T, want vibekit.SafetyStatusPayload", e.Payload)
		}
		got = append(got, p)
	}
	return got
}

// safetyPropsPayloads collects every EventSafetyProperties payload broadcast.
func safetyPropsPayloads(t *testing.T, events *[]vibekit.ServerEvent) []vibekit.SafetyPropertiesPayload {
	t.Helper()
	var got []vibekit.SafetyPropertiesPayload
	for _, e := range *events {
		if e.Type != vibekit.EventSafetyProperties {
			continue
		}
		p, ok := e.Payload.(vibekit.SafetyPropertiesPayload)
		if !ok {
			t.Fatalf("EventSafetyProperties payload type = %T, want vibekit.SafetyPropertiesPayload", e.Payload)
		}
		got = append(got, p)
	}
	return got
}

// TestHandleSafetyStatusChanged_Blocked pins that a blocked status translates
// to one safety_status event carrying the status, detail, tool id, and the
// violated properties.
func TestHandleSafetyStatusChanged_Blocked(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps)

	tr.HandleSafetyStatusChanged(t.Context(), "c1", &vibekit.RPCResponse{Params: mustJSON(t, map[string]any{
		"status":            "blocked",
		"detail":            "\U0001F6E1\uFE0F fs_write blocked",
		"toolId":            "fs_write",
		"blockedProperties": []string{"no public S3 buckets"},
	})})

	got := safetyStatusPayloads(t, events)
	if len(got) != 1 {
		t.Fatalf("safety_status count = %d, want 1", len(got))
	}
	p := got[0]
	if p.Status != vibekit.SafetyStatusBlocked {
		t.Errorf("Status = %q, want blocked", p.Status)
	}
	if p.ToolID != "fs_write" {
		t.Errorf("ToolID = %q, want fs_write", p.ToolID)
	}
	if len(p.BlockedProperties) != 1 || p.BlockedProperties[0] != "no public S3 buckets" {
		t.Errorf("BlockedProperties = %+v, want one entry", p.BlockedProperties)
	}
}

// TestHandleSafetyStatusChanged_IdleForwarded pins that idle is forwarded too
// (the client uses it to clear a stale banner).
func TestHandleSafetyStatusChanged_IdleForwarded(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps)

	tr.HandleSafetyStatusChanged(t.Context(), "c1", &vibekit.RPCResponse{Params: mustJSON(t, map[string]any{
		"status": "idle",
	})})

	got := safetyStatusPayloads(t, events)
	if len(got) != 1 || got[0].Status != vibekit.SafetyStatusIdle {
		t.Fatalf("want one idle safety_status, got %+v", got)
	}
}

// TestHandleSafetyStatusChanged_UnknownDropped pins that an unrecognized
// status is dropped rather than surfaced as a mystery banner.
func TestHandleSafetyStatusChanged_UnknownDropped(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps)

	tr.HandleSafetyStatusChanged(t.Context(), "c1", &vibekit.RPCResponse{Params: mustJSON(t, map[string]any{
		"status": "quantum-entangled",
	})})

	if got := safetyStatusPayloads(t, events); len(got) != 0 {
		t.Fatalf("want no broadcast for unknown status, got %+v", got)
	}
}

// TestHandleSafetyStatusChanged_MalformedNoop pins defensive decode.
func TestHandleSafetyStatusChanged_MalformedNoop(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps)
	tr.HandleSafetyStatusChanged(t.Context(), "c1", &vibekit.RPCResponse{Params: []byte("{")})
	if got := safetyStatusPayloads(t, events); len(got) != 0 {
		t.Fatalf("want no broadcast for malformed params, got %+v", got)
	}
}

// TestHandleSafetyPropertiesChanged_ObjectForm pins that object-form properties
// ({index, description, enabled}) translate to one safety_properties event.
func TestHandleSafetyPropertiesChanged_ObjectForm(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps)

	tr.HandleSafetyPropertiesChanged(t.Context(), "c1", &vibekit.RPCResponse{Params: mustJSON(t, map[string]any{
		"sessionId": "",
		"reason":    "formalized",
		"properties": []map[string]any{
			{"index": 0, "description": "no public S3 buckets", "enabled": true},
			{"index": 1, "description": "encrypt EBS volumes", "enabled": true},
		},
	})})

	got := safetyPropsPayloads(t, events)
	if len(got) != 1 {
		t.Fatalf("safety_properties count = %d, want 1", len(got))
	}
	if got[0].Reason != "formalized" {
		t.Errorf("Reason = %q, want formalized", got[0].Reason)
	}
	if len(got[0].Properties) != 2 || got[0].Properties[0].Description != "no public S3 buckets" {
		t.Errorf("Properties = %+v, want 2 formalized entries", got[0].Properties)
	}
}

// TestHandleSafetyPropertiesChanged_StringForm pins the tolerant decode: bare
// string properties (the getProperties path) become descriptions with
// Enabled=true.
func TestHandleSafetyPropertiesChanged_StringForm(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps)

	tr.HandleSafetyPropertiesChanged(t.Context(), "c1", &vibekit.RPCResponse{Params: mustJSON(t, map[string]any{
		"properties": []any{"no public S3 buckets", ""},
	})})

	got := safetyPropsPayloads(t, events)
	if len(got) != 1 {
		t.Fatalf("safety_properties count = %d, want 1", len(got))
	}
	if len(got[0].Properties) != 1 {
		t.Fatalf("Properties = %+v, want 1 (empty string dropped)", got[0].Properties)
	}
	if got[0].Properties[0].Description != "no public S3 buckets" || !got[0].Properties[0].Enabled {
		t.Errorf("Property = %+v, want description set + Enabled=true", got[0].Properties[0])
	}
}

// TestHandleSafetyPropertiesChanged_EmptyDropped pins that a notification with
// no usable properties produces no broadcast.
func TestHandleSafetyPropertiesChanged_EmptyDropped(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps)

	tr.HandleSafetyPropertiesChanged(t.Context(), "c1", &vibekit.RPCResponse{Params: mustJSON(t, map[string]any{
		"properties": []any{},
	})})

	if got := safetyPropsPayloads(t, events); len(got) != 0 {
		t.Fatalf("want no broadcast for empty properties, got %+v", got)
	}
}

// TestHandleSafetyPropertiesChanged_SkipsSubagent pins that a subagent-keyed
// copy is skipped (properties belong to the parent chat's surface).
func TestHandleSafetyPropertiesChanged_SkipsSubagent(t *testing.T) {
	t.Run("SubagentSkipped", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		deps.parent = "sess-parent"
		tr := New(deps)
		tr.HandleSafetyPropertiesChanged(t.Context(), "c1", &vibekit.RPCResponse{Params: mustJSON(t, map[string]any{
			"sessionId":  "sess-sub",
			"properties": []any{"no public S3 buckets"},
		})})
		if got := safetyPropsPayloads(t, events); len(got) != 0 {
			t.Fatalf("want no broadcast for subagent-keyed copy, got %+v", got)
		}
	})
	t.Run("ParentProcessed", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		deps.parent = "sess-parent"
		tr := New(deps)
		tr.HandleSafetyPropertiesChanged(t.Context(), "c1", &vibekit.RPCResponse{Params: mustJSON(t, map[string]any{
			"sessionId":  "sess-parent",
			"properties": []any{"no public S3 buckets"},
		})})
		if got := safetyPropsPayloads(t, events); len(got) != 1 {
			t.Fatalf("want one broadcast for parent-keyed copy, got %+v", got)
		}
	})
}

// infraBlockMessages returns the persisted RoleEvent messages on chatID whose
// EventKind is infra_safety_blocked.
func infraBlockMessages(t *testing.T, store *testsupport.InMemoryChatStore, chatID vibekit.ChatID) []vibekit.Message {
	t.Helper()
	c, ok := store.Get(t.Context(), chatID)
	if !ok {
		return nil
	}
	var got []vibekit.Message
	for _, m := range c.Messages {
		if m.EventKind == vibekit.EventInfraSafetyBlocked {
			got = append(got, m)
		}
	}
	return got
}

// depsWithStore wires an InMemoryChatStore into event-capturing deps and seeds
// chatID (AppendMessage no-ops on a missing chat, so the chat must exist).
func depsWithStore(t *testing.T, chatID vibekit.ChatID) (*baseDeps, *[]vibekit.ServerEvent, *testsupport.InMemoryChatStore) {
	t.Helper()
	deps, events := newEventCaptureDeps()
	store := testsupport.NewInMemoryChatStore()
	deps.store = store
	if err := store.Mutate(t.Context(), chatID, func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	return deps, events, store
}

// TestHandleSafetyStatusChanged_BlockedPersistsEvent pins the enforce-mode
// surface: a blocked status persists a permanent inline event message
// (EventKind infra_safety_blocked) carrying the violated properties, IN
// ADDITION to the transient safety_status banner SSE. The permanent record is
// what makes the refusal outlive the fleeting banner.
func TestHandleSafetyStatusChanged_BlockedPersistsEvent(t *testing.T) {
	deps, events, store := depsWithStore(t, "c1")
	tr := New(deps)

	tr.HandleSafetyStatusChanged(t.Context(), "c1", &vibekit.RPCResponse{Params: mustJSON(t, map[string]any{
		"status":            "blocked",
		"detail":            "\U0001F6E1\uFE0F fs_write blocked",
		"toolId":            "fs_write",
		"blockedProperties": []string{"no public S3 buckets", "encrypt at rest"},
	})})

	// Transient banner SSE still fires.
	if got := safetyStatusPayloads(t, events); len(got) != 1 || got[0].Status != vibekit.SafetyStatusBlocked {
		t.Fatalf("want one blocked safety_status broadcast, got %+v", got)
	}
	// Permanent record: exactly one block event, role=event, carrying the WHY.
	msgs := infraBlockMessages(t, store, "c1")
	if len(msgs) != 1 {
		t.Fatalf("infra_safety_blocked event count = %d, want 1", len(msgs))
	}
	if msgs[0].Role != vibekit.RoleEvent {
		t.Errorf("Role = %q, want event", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "no public S3 buckets") || !strings.Contains(msgs[0].Content, "encrypt at rest") {
		t.Errorf("Content = %q, want the violated properties", msgs[0].Content)
	}
}

// TestHandleSafetyStatusChanged_BlockedFallsBackToDetail pins that a block with
// no properties records the gate's detail rather than an empty event.
func TestHandleSafetyStatusChanged_BlockedFallsBackToDetail(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	tr := New(deps)

	tr.HandleSafetyStatusChanged(t.Context(), "c1", &vibekit.RPCResponse{Params: mustJSON(t, map[string]any{
		"status": "blocked",
		"detail": "policy violation",
	})})

	msgs := infraBlockMessages(t, store, "c1")
	if len(msgs) != 1 || msgs[0].Content != "policy violation" {
		t.Fatalf("want one block event with detail fallback, got %+v", msgs)
	}
}

// TestHandleSafetyStatusChanged_NonBlockedNoPersist pins that in-progress and
// all-clear statuses only broadcast the transient banner and never persist a
// permanent event (only enforce-mode blocks are durable facts).
func TestHandleSafetyStatusChanged_NonBlockedNoPersist(t *testing.T) {
	for _, status := range []string{"idle", "formalizing", "evaluating", "error"} {
		t.Run(status, func(t *testing.T) {
			deps, _, store := depsWithStore(t, "c1")
			tr := New(deps)

			tr.HandleSafetyStatusChanged(t.Context(), "c1", &vibekit.RPCResponse{Params: mustJSON(t, map[string]any{
				"status": status,
			})})

			if msgs := infraBlockMessages(t, store, "c1"); len(msgs) != 0 {
				t.Fatalf("status %q persisted %d event(s), want 0", status, len(msgs))
			}
		})
	}
}
