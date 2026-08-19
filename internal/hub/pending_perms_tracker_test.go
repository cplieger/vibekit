package hub

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// listIDs reads the request ids back off a List snapshot, which is the only way
// to assert the order the replay will write the cards in.
func listIDs(t *testing.T, evts []vibekit.ServerEvent) []int64 {
	t.Helper()
	ids := make([]int64, 0, len(evts))
	for _, evt := range evts {
		p, ok := evt.Payload.(vibekit.PermissionNeededPayload)
		if !ok {
			t.Fatalf("replayed event carries payload %T, want vibekit.PermissionNeededPayload", evt.Payload)
		}
		ids = append(ids, p.RequestID)
	}
	return ids
}

// TestPendingPermsTracker_List_OrdersByRequestID pins the connect-time replay's
// ordering contract: ascending request id, which is ask order because the
// JSON-RPC boundary assigns ids monotonically.
//
// The ids are added out of order on purpose. Reading the map directly returned
// Go's randomized order, so this test would have been flaky-green rather than
// failing outright — which is why the assertion is the full sequence rather than
// a spot check on the first element.
func TestPendingPermsTracker_List_OrdersByRequestID(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	for _, id := range []int64{7, 2, 9, 1, 5} {
		tracker.Add(id, vibekit.NewEvent(vibekit.EventPermissionNeeded, "chat-1",
			vibekit.PermissionNeededPayload{RequestID: id}))
	}

	want := []int64{1, 2, 5, 7, 9}
	for range 8 { // repeated because the defect it replaces was order-random
		got := listIDs(t, tracker.List(""))
		if len(got) != len(want) {
			t.Fatalf("List returned %d events, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("List order = %v, want %v", got, want)
			}
		}
	}
}

// TestPendingPermsTracker_List_OrdersAcrossKinds covers the three card kinds
// tracked in the one id space. Ordering is the QUEUE's, not each kind's: an
// elicitation asked between two permissions replays between them, because a
// reader's question is "what was I asked, in what order" and not "what were the
// permissions".
func TestPendingPermsTracker_List_OrdersAcrossKinds(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	kinds := map[int64]vibekit.EventType{
		31: vibekit.EventPermissionNeeded,
		12: vibekit.EventElicitationNeeded,
		20: vibekit.EventUserInputNeeded,
	}
	for id, kind := range kinds {
		tracker.Add(id, vibekit.NewEvent(kind, "chat-1", vibekit.PermissionNeededPayload{RequestID: id}))
	}

	got := tracker.List("chat-1")
	wantIDs := []int64{12, 20, 31}
	ids := listIDs(t, got)
	for i := range wantIDs {
		if ids[i] != wantIDs[i] {
			t.Fatalf("List order = %v, want %v", ids, wantIDs)
		}
		if got[i].Type != kinds[wantIDs[i]] {
			t.Errorf("id %d replayed as %q, want %q", wantIDs[i], got[i].Type, kinds[wantIDs[i]])
		}
	}
}

// TestPendingPermsTracker_List_FiltersByChatAndStaysOrdered checks the filter
// did not become an ordering exception: a filtered replay is the same sequence
// with the other chats' cards removed, never a re-sort.
func TestPendingPermsTracker_List_FiltersByChatAndStaysOrdered(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	owners := map[int64]vibekit.ChatID{4: "chat-1", 8: "chat-2", 1: "chat-1", 6: "chat-2", 3: "chat-1"}
	for id, chatID := range owners {
		tracker.Add(id, vibekit.NewEvent(vibekit.EventPermissionNeeded, chatID,
			vibekit.PermissionNeededPayload{RequestID: id}))
	}

	for chatID, want := range map[vibekit.ChatID][]int64{
		"chat-1": {1, 3, 4},
		"chat-2": {6, 8},
	} {
		got := listIDs(t, tracker.List(chatID))
		if len(got) != len(want) {
			t.Fatalf("List(%q) returned %v, want %v", chatID, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("List(%q) order = %v, want %v", chatID, got, want)
			}
		}
	}
}
