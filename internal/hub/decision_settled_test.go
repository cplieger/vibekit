package hub

// decision_settled is what closes a card on the surfaces that did NOT answer.
// Two things are pinned: the winning claim announces itself with the kind and
// the attribution intact, and a losing claim announces nothing — an event per
// rejected attempt would retire cards for a decision that attempt never settled.

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/sse"
)

// settledEvents decodes the decision_settled payloads emitted after sinceID.
func settledEvents(t *testing.T, events []sse.ReplayEvent) []vibekit.DecisionSettledPayload {
	t.Helper()
	var out []vibekit.DecisionSettledPayload
	for _, e := range events {
		var envelope struct {
			Type    vibekit.EventType              `json:"type"`
			ChatID  vibekit.ChatID                 `json:"chat_id"`
			Payload vibekit.DecisionSettledPayload `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &envelope); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if envelope.Type != vibekit.EventDecisionSettled {
			continue
		}
		if envelope.ChatID != "c1" {
			t.Errorf("event chat_id = %q, want c1 (the chat the ask was raised on)", envelope.ChatID)
		}
		out = append(out, envelope.Payload)
	}
	return out
}

func TestTakePendingPerm_AnnouncesTheSettledDecision(t *testing.T) {
	cases := []struct {
		name      string
		event     vibekit.EventType
		wantKind  vibekit.DecisionKind
		settledBy vibekit.SettledBy
	}{
		{
			name:      "permission answered by a person",
			event:     vibekit.EventPermissionNeeded,
			wantKind:  vibekit.DecisionKindPermission,
			settledBy: vibekit.SettledByUser,
		},
		{
			name:      "elicitation answered by a person",
			event:     vibekit.EventElicitationNeeded,
			wantKind:  vibekit.DecisionKindElicitation,
			settledBy: vibekit.SettledByUser,
		},
		{
			// The kind travels from the TRACKED event, so a question and a
			// permission cannot be reported as each other.
			name:      "question answered by the unattended floor",
			event:     vibekit.EventUserInputNeeded,
			wantKind:  vibekit.DecisionKindUserInput,
			settledBy: vibekit.SettledByUnattended,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newTestHub()
			_, head := h.replayBounds()
			h.sse.pendingPerms.Add(9, vibekit.NewEvent(tc.event, "c1", vibekit.PermissionNeededPayload{RequestID: 9}))

			if !h.sse.TakePendingPerm(9, tc.settledBy) {
				t.Fatal("TakePendingPerm refused a pending request")
			}

			got := settledEvents(t, bufferedSince(h, head))
			if len(got) != 1 {
				t.Fatalf("emitted %d decision_settled events, want 1", len(got))
			}
			want := vibekit.DecisionSettledPayload{RequestID: 9, Kind: tc.wantKind, SettledBy: tc.settledBy}
			if got[0] != want {
				t.Errorf("payload = %+v, want %+v", got[0], want)
			}
		})
	}
}

// TestTakePendingPerm_LosingClaimAnnouncesNothing: the second tab's attempt
// settles nothing, so it must not tell every surface that the card is closed —
// and an unanswered request is exactly the one that has to stay on screen.
func TestTakePendingPerm_LosingClaimAnnouncesNothing(t *testing.T) {
	h, _, _ := newTestHub()
	h.sse.pendingPerms.Add(9, vibekit.NewEvent(vibekit.EventPermissionNeeded, "c1",
		vibekit.PermissionNeededPayload{RequestID: 9}))
	if !h.sse.TakePendingPerm(9, vibekit.SettledByUser) {
		t.Fatal("first claim refused")
	}

	_, head := h.replayBounds()
	if h.sse.TakePendingPerm(9, vibekit.SettledByUser) {
		t.Error("second claim on one request id succeeded, want refused")
	}
	if got := settledEvents(t, bufferedSince(h, head)); len(got) != 0 {
		t.Errorf("a losing claim emitted %d events, want 0: %+v", len(got), got)
	}
}
