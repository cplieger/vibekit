package translate

// The three steering sub-kinds are the one place the session_info_update cascade
// dispatches on the KIND STRING rather than on which sub-block is present, so the
// wire shape is what these tests pin: flat fields beside `kind`, because KAS's
// buildSessionInfoUpdate spreads the update object straight into `_meta.kiro` and
// emits no legacy nested block for any of them.

import (
	"maps"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
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
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId": "steer-1",
			"content":   "use tabs",
		}), FrameAttribution{})

	if len(*events) != 1 {
		t.Fatalf("broadcast %d events, want 1", len(*events))
	}
	e := (*events)[0]
	if e.Type != vibekit.EventSteerQueued {
		t.Fatalf("type = %q, want %q", e.Type, vibekit.EventSteerQueued)
	}
	p, ok := e.Payload.(vibekit.SteerQueuedPayload)
	if !ok {
		t.Fatalf("payload type = %T", e.Payload)
	}
	if p.SteerID != "steer-1" || p.Text != "use tabs" {
		t.Errorf("payload = %+v, want steer-1 / use tabs", p)
	}
}

// The agent's own notice leaves as its own event, never as a steer. KAS delivers
// it through the same buffer and the severity is the only thing distinguishing
// it, so the split has to happen here: forwarding it as a steer put the agent's
// words on the composer's chip row as though the user had typed them.
func TestSteeringQueued_AgentNoticeLeavesAsItsOwnEvent(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId":            "notify-1",
			"content":              "[notification/error] a step failed",
			"notificationSeverity": "error",
		}), FrameAttribution{})

	if len(*events) != 1 {
		t.Fatalf("broadcast %d events, want 1", len(*events))
	}
	e := (*events)[0]
	if e.Type != vibekit.EventAgentNotice {
		t.Fatalf("type = %q, want %q", e.Type, vibekit.EventAgentNotice)
	}
	p, ok := e.Payload.(vibekit.AgentNoticePayload)
	if !ok {
		t.Fatalf("payload type = %T", e.Payload)
	}
	if p.Severity != "error" {
		t.Errorf("severity = %q, want error", p.Severity)
	}
	if p.Text != "[notification/error] a step failed" {
		t.Errorf("text = %q, want the notice verbatim", p.Text)
	}
}

func TestSteeringInjected_BroadcastsTheRead(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_injected", map[string]any{
			"messageId": "steer-1",
			"content":   "use tabs",
		}), FrameAttribution{})

	if len(*events) != 1 || (*events)[0].Type != vibekit.EventSteerInjected {
		t.Fatalf("events = %+v, want one steer_injected", *events)
	}
	p, ok := (*events)[0].Payload.(vibekit.SteerInjectedPayload)
	if !ok {
		t.Fatalf("payload type = %T", (*events)[0].Payload)
	}
	if p.SteerID != "steer-1" {
		t.Errorf("steer id = %q, want steer-1", p.SteerID)
	}
}

func TestSteeringCleared_BroadcastsTheDroppedIDs(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_cleared", map[string]any{
			"messageIds": []string{"steer-1", "steer-2"},
		}), FrameAttribution{})

	if len(*events) != 1 || (*events)[0].Type != vibekit.EventSteerCleared {
		t.Fatalf("events = %+v, want one steer_cleared", *events)
	}
	p, ok := (*events)[0].Payload.(vibekit.SteerClearedPayload)
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
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_cleared", map[string]any{"messageIds": []string{}}), FrameAttribution{})

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
			New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
				steerFrame(t, kind, map[string]any{"content": "orphan"}), FrameAttribution{})

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
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_injected", map[string]any{
			"messageId": "steer-1",
			"content":   "use tabs",
		}), FrameAttribution{SubSessionID: "sub-session-7"})

	if len(*events) != 1 || (*events)[0].Type != vibekit.EventSteerInjected {
		t.Fatalf("events = %+v — a steer consumed inside a subagent must still be reported", *events)
	}
}

// --- Origin: whose words the steer carries -------------------------------
//
// The one field on these payloads that is not decoded from the frame. Nothing on
// the wire separates the user's own correction from a workflow reporting into the
// same buffer — measured on the live store, all three producers persist
// identically as `{"type":"user","source":"steer"}` — so the answer comes from
// the ledger of what THIS server sent.

func TestSteeringQueued_OriginIsUserForASteerThisServerSent(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	deps.userSteers = map[string]bool{"steer-m-1": true}
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId": "steer-m-1",
			"content":   "use tabs",
		}), FrameAttribution{})

	p, ok := (*events)[0].Payload.(vibekit.SteerQueuedPayload)
	if !ok {
		t.Fatalf("payload type = %T", (*events)[0].Payload)
	}
	if p.Origin != vibekit.SteerOriginUser {
		t.Errorf("origin = %q, want %q for an id the ledger holds", p.Origin, vibekit.SteerOriginUser)
	}
}

// An id the ledger does not hold is the agent's, and this is the reported
// defect: a workflow's report rendered as something the user had typed.
func TestSteeringQueued_OriginIsAgentForAnIDTheLedgerDoesNotHold(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId": "notify-wf-9",
			"content":   "A workflow you launched completed.",
		}), FrameAttribution{})

	p, ok := (*events)[0].Payload.(vibekit.SteerQueuedPayload)
	if !ok {
		t.Fatalf("payload type = %T", (*events)[0].Payload)
	}
	if p.Origin != vibekit.SteerOriginAgent {
		t.Errorf("origin = %q, want %q", p.Origin, vibekit.SteerOriginAgent)
	}
}

// The injected frame carries it too, and that is load-bearing rather than
// symmetric: both agent injection paths append straight to KAS's buffer and only
// `_session/steer` broadcasts a queued frame, so for the case Origin exists to
// name, the injected frame is the ONLY one the client ever sees.
func TestSteeringInjected_CarriesTheOrigin(t *testing.T) {
	for _, tc := range []struct {
		name  string
		known bool
		want  vibekit.SteerOrigin
	}{
		{"the user's own", true, vibekit.SteerOriginUser},
		{"a workflow's report", false, vibekit.SteerOriginAgent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, events, _ := depsWithStore(t, "c1")
			if tc.known {
				deps.userSteers = map[string]bool{"steer-1": true}
			}
			New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
				steerFrame(t, "steering_injected", map[string]any{
					"messageId": "steer-1",
					"content":   "use tabs",
				}), FrameAttribution{})

			p, ok := (*events)[0].Payload.(vibekit.SteerInjectedPayload)
			if !ok {
				t.Fatalf("payload type = %T", (*events)[0].Payload)
			}
			if p.Origin != tc.want {
				t.Errorf("origin = %q, want %q", p.Origin, tc.want)
			}
		})
	}
}

// A frame carrying a severity still leaves as an agent_notice and produces no
// steer at all, so Origin never has to answer for one. The severity gate and the
// ledger answer different questions: the gate catches the one shape KAS marks,
// and the ledger covers everything else the agent injects.
func TestSteeringQueued_ASeverityStillPreemptsTheSteerEntirely(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		steerFrame(t, "steering_queued", map[string]any{
			"messageId":            "notify-1",
			"content":              "[notification/warning] a step is waiting",
			"notificationSeverity": "warning",
		}), FrameAttribution{})

	if len(*events) != 1 || (*events)[0].Type != vibekit.EventAgentNotice {
		t.Fatalf("events = %+v, want one agent_notice and no steer", *events)
	}
}
