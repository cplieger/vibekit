package hub

import (
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"

	"pgregory.net/rapid"
)

// flushRecorder is a ResponseWriter+Flusher that captures output so
// replayPendingPermissions can be driven without a real SSE connection.
type flushRecorder struct {
	hdr     http.Header
	body    strings.Builder
	flushed int
	status  int
}

func (r *flushRecorder) Header() http.Header {
	if r.hdr == nil {
		r.hdr = make(http.Header)
	}
	return r.hdr
}

func (r *flushRecorder) Write(p []byte) (int, error) { return r.body.Write(p) }
func (r *flushRecorder) WriteHeader(code int)        { r.status = code }
func (r *flushRecorder) Flush()                      { r.flushed++ }

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

func TestReplayPendingPermissions_noFilter_sendsAll(t *testing.T) {
	h, _, _ := newTestHub()
	seedPending(h, 1, "c1")
	seedPending(h, 2, "c2")

	rec := &flushRecorder{}
	h.replayPendingPermissions(rec, rec, "")

	body := rec.body.String()
	if !strings.Contains(body, `"request_id":1`) {
		t.Errorf("body missing request 1: %q", body)
	}
	if !strings.Contains(body, `"request_id":2`) {
		t.Errorf("body missing request 2: %q", body)
	}
	if rec.flushed == 0 {
		t.Error("flusher was never called")
	}
}

func TestReplayPendingPermissions_withFilter(t *testing.T) {
	h, _, _ := newTestHub()
	seedPending(h, 10, "c1")
	seedPending(h, 20, "c2")

	rec := &flushRecorder{}
	h.replayPendingPermissions(rec, rec, "c1")

	body := rec.body.String()
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

	rec := &flushRecorder{}
	h.replayPendingPermissions(rec, rec, "c1")
	if !strings.Contains(rec.body.String(), `"request_id":99`) {
		t.Errorf("global perm dropped by chat filter: %q", rec.body.String())
	}
}

func TestReplayPendingPermissions_emptyQueue_justFlushes(t *testing.T) {
	h, _, _ := newTestHub()
	rec := &flushRecorder{}
	h.replayPendingPermissions(rec, rec, "")
	if rec.body.Len() != 0 {
		t.Errorf("empty queue wrote body: %q", rec.body.String())
	}
	if rec.flushed != 1 {
		t.Errorf("empty queue: flushed %d times, want 1", rec.flushed)
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
