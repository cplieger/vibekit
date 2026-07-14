package hub

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
	"pgregory.net/rapid"
)

func seedPending(h *Hub, reqID int64, chatID api.ChatID) {
	h.sse.pendingPerms.Add(reqID, api.ServerEvent{
		Type:   "permission_needed",
		ChatID: chatID,
		Payload: api.PermissionNeededPayload{
			RequestID: reqID,
			Title:     "approve?",
			Options:   []api.PermissionOption{{OptionID: "allow", Name: "Allow"}},
		},
	})
}

// collectPendingPerms runs the pending-permission replay through the
// writeFn seam and returns the concatenated JSON payloads.
func collectPendingPerms(t *testing.T, h *Hub, filter api.ChatID) string {
	t.Helper()
	var sb strings.Builder
	if err := h.replayPendingPermissions(func(evt api.ServerEvent) error {
		data, err := json.Marshal(evt)
		if err != nil {
			return err
		}
		sb.Write(data)
		sb.WriteByte('\n')
		return nil
	}, filter); err != nil {
		t.Fatalf("replayPendingPermissions: %v", err)
	}
	return sb.String()
}

func TestReplayPendingPermissions_noFilter_sendsAll(t *testing.T) {
	h, _, _ := newTestHub()
	seedPending(h, 1, "c1")
	seedPending(h, 2, "c2")

	body := collectPendingPerms(t, h, "")
	if !strings.Contains(body, `"request_id":1`) {
		t.Errorf("body missing request 1: %q", body)
	}
	if !strings.Contains(body, `"request_id":2`) {
		t.Errorf("body missing request 2: %q", body)
	}
}

func TestReplayPendingPermissions_withFilter(t *testing.T) {
	h, _, _ := newTestHub()
	seedPending(h, 10, "c1")
	seedPending(h, 20, "c2")

	body := collectPendingPerms(t, h, "c1")
	if !strings.Contains(body, `"request_id":10`) {
		t.Errorf("filtered replay missing c1 perm: %q", body)
	}
	if strings.Contains(body, `"request_id":20`) {
		t.Errorf("filtered replay leaked c2 perm: %q", body)
	}
}

func TestReplayPendingPermissions_globalEventsAlwaysSent(t *testing.T) {
	h, _, _ := newTestHub()
	// Empty ChatID: a global permission. The filter rule keeps
	// globals visible to chat-filtered clients.
	seedPending(h, 99, "")

	if body := collectPendingPerms(t, h, "c1"); !strings.Contains(body, `"request_id":99`) {
		t.Errorf("global perm dropped by chat filter: %q", body)
	}
}

func TestReplayPendingPermissions_emptyQueue_justFlushes(t *testing.T) {
	h, _, _ := newTestHub()
	if body := collectPendingPerms(t, h, ""); body != "" {
		t.Errorf("empty queue wrote body: %q", body)
	}
}

// --- tarch-b10-c5-p2: Property-based test for pendingPermsTracker ---

// TestPendingPermsTracker_Property uses pgregory.net/rapid to verify
// Add/Remove/ClearForChat invariants under random operation sequences:
//   - After Add(id, evt), List("") includes evt
//   - After Remove(id), List("") no longer includes it
//   - ClearForChat(chatID) removes exactly entries with matching ChatID
func TestPendingPermsTracker_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		tracker := newPendingPermsTracker()
		// Model: track expected state.
		model := make(map[int64]string) // id -> chatID

		ops := rapid.IntRange(1, 200).Draw(t, "ops")
		for range ops {
			op := rapid.IntRange(0, 2).Draw(t, "op")
			switch op {
			case 0: // Add
				id := rapid.Int64Range(1, 100).Draw(t, "addID")
				chatID := rapid.StringMatching(`^c[1-5]$`).Draw(t, "chatID")
				tracker.Add(id, api.ServerEvent{
					Type:   "permission_needed",
					ChatID: api.ChatID(chatID),
					Payload: api.PermissionNeededPayload{
						RequestID: id,
						Title:     "test",
						Options:   []api.PermissionOption{{OptionID: "allow", Name: "Allow"}},
					},
				})
				model[id] = chatID
			case 1: // Remove
				id := rapid.Int64Range(1, 100).Draw(t, "removeID")
				tracker.Remove(id)
				delete(model, id)
			case 2: // ClearForChat
				chatID := rapid.StringMatching(`^c[1-5]$`).Draw(t, "clearChat")
				tracker.ClearForChat(api.ChatID(chatID))
				for id, c := range model {
					if c == chatID {
						delete(model, id)
					}
				}
			}
		}

		// Invariant: List("") returns exactly the model's entries.
		got := tracker.List("")
		if len(got) != len(model) {
			t.Fatalf("List(\"\") len = %d, model len = %d", len(got), len(model))
		}
		gotIDs := make(map[int64]bool, len(got))
		for _, evt := range got {
			p, ok := evt.Payload.(api.PermissionNeededPayload)
			if !ok {
				t.Fatalf("unexpected payload type: %T", evt.Payload)
			}
			gotIDs[p.RequestID] = true
		}
		for id := range model {
			if !gotIDs[id] {
				t.Fatalf("model has id=%d but List does not", id)
			}
		}
	})
}
