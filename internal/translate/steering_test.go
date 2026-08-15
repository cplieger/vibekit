package translate

// The three steering sub-kinds are the one place the session_info_update cascade
// dispatches on the KIND STRING rather than on which sub-block is present, so the
// wire shape is what these tests pin: flat fields beside `kind`, because KAS's
// buildSessionInfoUpdate spreads the update object straight into `_meta.kiro` and
// emits no legacy nested block for any of them.

import (
	"maps"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// steerFrame builds a session_info_update whose steering fields sit FLAT beside
// `kind`, which is how KAS sends them.
func steerFrame(t *testing.T, kind string, fields map[string]any) []byte {
	t.Helper()
	kiro := map[string]any{"kind": kind}
	maps.Copy(kiro, fields)
	return mustJSON(t, map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta":         map[string]any{"kiro": kiro},
	})
}

func TestSteeringQueued_BroadcastsTheWaitingSteer(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(deps).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId": "steer-1",
			"content":   "use tabs",
		}), "")

	if len(*events) != 1 {
		t.Fatalf("broadcast %d events, want 1", len(*events))
	}
	e := (*events)[0]
	if e.Type != api.EventSteerQueued {
		t.Fatalf("type = %q, want %q", e.Type, api.EventSteerQueued)
	}
	p, ok := e.Payload.(api.SteerQueuedPayload)
	if !ok {
		t.Fatalf("payload type = %T", e.Payload)
	}
	if p.SteerID != "steer-1" || p.Text != "use tabs" {
		t.Errorf("payload = %+v, want steer-1 / use tabs", p)
	}
	// Absent severity must stay absent: a non-empty value is what tells the client
	// the message did NOT come from the user.
	if p.Severity != "" {
		t.Errorf("severity = %q, want empty for a user steer", p.Severity)
	}
}

func TestSteeringQueued_CarriesTheNotificationSeverity(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(deps).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId":            "notify-1",
			"content":              "[notification/error] a step failed",
			"notificationSeverity": "error",
		}), "")

	p, ok := (*events)[0].Payload.(api.SteerQueuedPayload)
	if !ok {
		t.Fatalf("payload type = %T", (*events)[0].Payload)
	}
	if p.Severity != "error" {
		t.Errorf("severity = %q, want error", p.Severity)
	}
}

func TestSteeringInjected_BroadcastsTheRead(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(deps).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_injected", map[string]any{
			"messageId": "steer-1",
			"content":   "use tabs",
		}), "")

	if len(*events) != 1 || (*events)[0].Type != api.EventSteerInjected {
		t.Fatalf("events = %+v, want one steer_injected", *events)
	}
	p, ok := (*events)[0].Payload.(api.SteerInjectedPayload)
	if !ok {
		t.Fatalf("payload type = %T", (*events)[0].Payload)
	}
	if p.SteerID != "steer-1" {
		t.Errorf("steer id = %q, want steer-1", p.SteerID)
	}
}

func TestSteeringCleared_BroadcastsTheDroppedIDs(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(deps).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_cleared", map[string]any{
			"messageIds": []string{"steer-1", "steer-2"},
		}), "")

	if len(*events) != 1 || (*events)[0].Type != api.EventSteerCleared {
		t.Fatalf("events = %+v, want one steer_cleared", *events)
	}
	p, ok := (*events)[0].Payload.(api.SteerClearedPayload)
	if !ok {
		t.Fatalf("payload type = %T", (*events)[0].Payload)
	}
	if len(p.SteerIDs) != 2 || p.SteerIDs[0] != "steer-1" || p.SteerIDs[1] != "steer-2" {
		t.Errorf("ids = %v, want both", p.SteerIDs)
	}
}

// KAS clears its buffer at EVERY turn boundary, so an empty list is the normal
// case on the vast majority of turns. Broadcasting it would put one dead event on
// the wire per turn for every chat.
func TestSteeringCleared_EmptyListIsNotBroadcast(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(deps).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_cleared", map[string]any{"messageIds": []string{}}), "")

	if len(*events) != 0 {
		t.Errorf("broadcast %d events for an empty clear, want 0", len(*events))
	}
}

// A steer with no id is unaddressable: every later event keys on it, so a chip
// built from this could never be resolved or cleared. Dropped rather than
// forwarded — but still CONSUMED, so it does not fall through to the
// unknown-kind warning.
func TestSteering_IDlessFramesAreDroppedNotForwarded(t *testing.T) {
	for _, kind := range []string{"steering_queued", "steering_injected"} {
		t.Run(kind, func(t *testing.T) {
			deps, events, _ := depsWithStore(t, "c1")
			New(deps).HandleSessionInfoUpdate(t.Context(), "c1",
				steerFrame(t, kind, map[string]any{"content": "orphan"}), "")

			if len(*events) != 0 {
				t.Errorf("broadcast %d events for an id-less %s, want 0", len(*events), kind)
			}
		})
	}
}

// The steering dispatch runs BEFORE the parent-only gate, and this is why: a
// steer belongs to the CHAT — the user typed it there — but it is consumed by
// whichever execution is running, which may be a subagent's. Gating on
// attribution would drop the injected signal exactly when the agent delegated,
// leaving a chip that says "waiting" over a message the model has read.
func TestSteering_SurvivesSubagentAttribution(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(deps).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_injected", map[string]any{
			"messageId": "steer-1",
			"content":   "use tabs",
		}), "sub-session-7")

	if len(*events) != 1 || (*events)[0].Type != api.EventSteerInjected {
		t.Fatalf("events = %+v — a steer consumed inside a subagent must still be reported", *events)
	}
}
