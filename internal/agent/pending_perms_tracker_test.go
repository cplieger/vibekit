package agent

import (
	"slices"
	"strconv"
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
	// One pass can satisfy an order-random List by luck, so each pass is its own
	// subtest: a single unlucky pass reports without hiding the rest.
	for pass := range 8 {
		t.Run("pass_"+strconv.Itoa(pass), func(t *testing.T) {
			if got := listIDs(t, tracker.List("")); !slices.Equal(got, want) {
				t.Errorf("List order = %v, want %v", got, want)
			}
		})
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
	// Fatal, and the reason is the per-kind subtests below: they index got, so a
	// short replay would panic the test binary instead of reporting a failure.
	if len(got) != len(wantIDs) {
		t.Fatalf("List returned %d events, want %d: %v", len(got), len(wantIDs), listIDs(t, got))
	}
	if ids := listIDs(t, got); !slices.Equal(ids, wantIDs) {
		t.Errorf("List order = %v, want %v", ids, wantIDs)
	}
	for i, id := range wantIDs {
		t.Run(string(kinds[id]), func(t *testing.T) {
			if got[i].Type != kinds[id] {
				t.Errorf("id %d replayed as %q, want %q", id, got[i].Type, kinds[id])
			}
		})
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
		t.Run(string(chatID), func(t *testing.T) {
			if got := listIDs(t, tracker.List(chatID)); !slices.Equal(got, want) {
				t.Errorf("List(%q) order = %v, want %v", chatID, got, want)
			}
		})
	}
}

// ClearForChat drops the closing chat's unresolved cards and only those. Both
// halves matter and they fail in opposite directions: keeping the closing chat's
// entries leaves a card the user can never answer, and dropping another chat's
// entries makes TakeIfPresent refuse an answer that chat is still waiting to
// give.
func TestPendingPermsTracker_ClearForChat_DropsOnlyThatChat(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	owners := map[int64]vibekit.ChatID{1: "chat-1", 2: "chat-2", 3: "chat-1", 4: "chat-2"}
	for id, chatID := range owners {
		tracker.Add(id, vibekit.NewEvent(vibekit.EventPermissionNeeded, chatID,
			vibekit.PermissionNeededPayload{RequestID: id}))
	}

	tracker.ClearForChat("chat-1")

	if got := listIDs(t, tracker.List("chat-1")); len(got) != 0 {
		t.Errorf("List(\"chat-1\") = %v after ClearForChat(\"chat-1\"), want none", got)
	}
	want := []int64{2, 4}
	if got := listIDs(t, tracker.List("chat-2")); !slices.Equal(got, want) {
		t.Errorf("List(\"chat-2\") = %v after ClearForChat(\"chat-1\"), want %v", got, want)
	}
	// The surviving chat's answers are still accepted, which is what the entries
	// are for.
	if _, ok := tracker.TakeIfPresent("chat-2", 2); !ok {
		t.Error(`TakeIfPresent("chat-2", 2) = false: another chat's clear took chat-2's entry`)
	}
}

// An empty chat id clears nothing. It is not a wildcard: the one caller that
// could pass it is a close path with no chat, and treating it as "every chat"
// would drop every open dialog in the process.
func TestPendingPermsTracker_ClearForChat_EmptyChatIDClearsNothing(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	for _, id := range []int64{1, 2} {
		tracker.Add(id, vibekit.NewEvent(vibekit.EventPermissionNeeded, "chat-1",
			vibekit.PermissionNeededPayload{RequestID: id}))
	}

	tracker.ClearForChat("")

	want := []int64{1, 2}
	if got := listIDs(t, tracker.List("")); !slices.Equal(got, want) {
		t.Errorf("List(\"\") = %v after ClearForChat(\"\"), want %v", got, want)
	}
}

// TestPendingPermsTracker_TwoChatsMayHoldTheSameRequestID is the identity space
// the tracker is keyed on, and the case an id-only map could not represent.
//
// Request ids are minted per BRIDGE (`nextID atomic.Int64`, one bridge per chat,
// every one starting at zero), so two live chats holding request 7 at the same
// time is ordinary rather than a race. Keyed on the id alone the second Add
// silently overwrote the first chat's card, so: chat 1's dialog was replayed to
// chat 2 on reconnect, an answer from EITHER chat retired the single surviving
// entry, and whichever request lost had no answer path left at all while the
// engine still held it open — a turn wedged for the whole life of the bridge.
func TestPendingPermsTracker_TwoChatsMayHoldTheSameRequestID(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	const shared = int64(7)
	tracker.Add(shared, vibekit.NewEvent(vibekit.EventPermissionNeeded, "chat-1",
		vibekit.PermissionNeededPayload{RequestID: shared, Title: "chat-1 asked"}))
	tracker.Add(shared, vibekit.NewEvent(vibekit.EventUserInputNeeded, "chat-2",
		vibekit.UserInputNeededPayload{RequestID: shared, Question: "chat-2 asked"}))

	// Each chat's replay carries its OWN card, not the other's.
	for _, tc := range []struct {
		chat vibekit.ChatID
		want vibekit.EventType
	}{
		{chat: "chat-1", want: vibekit.EventPermissionNeeded},
		{chat: "chat-2", want: vibekit.EventUserInputNeeded},
	} {
		got := tracker.List(tc.chat)
		if len(got) != 1 {
			t.Fatalf("List(%q) returned %d cards, want 1", tc.chat, len(got))
		}
		if got[0].Type != tc.want {
			t.Errorf("List(%q) card type = %q, want %q: the other chat's request "+
				"overwrote this one", tc.chat, got[0].Type, tc.want)
		}
	}

	// chat-1 answers. chat-2's request must still be answerable: it was never
	// asked, and nothing else can answer it for that chat.
	evt, ok := tracker.TakeIfPresent("chat-1", shared)
	if !ok {
		t.Fatal(`TakeIfPresent("chat-1", 7) refused a pending request`)
	}
	if evt.ChatID != "chat-1" {
		t.Errorf("chat-1's claim returned chat %q's event", evt.ChatID)
	}
	if _, ok := tracker.TakeIfPresent("chat-2", shared); !ok {
		t.Error(`TakeIfPresent("chat-2", 7) = false after chat-1 answered: chat-2's ` +
			"turn now waits forever for a response nothing can send")
	}
	// And a claim naming the wrong chat resolves nothing at all.
	tracker.Add(shared, vibekit.NewEvent(vibekit.EventPermissionNeeded, "chat-3",
		vibekit.PermissionNeededPayload{RequestID: shared}))
	if _, ok := tracker.TakeIfPresent("chat-4", shared); ok {
		t.Error(`TakeIfPresent("chat-4", 7) succeeded against chat-3's request`)
	}
}

// requestIDOf reads one replayed card's request id, whichever of the three kinds
// it is. `listIDs` above cannot serve: it fails the test on any payload that is
// not a permission, and this table mixes all three deliberately.
func requestIDOf(t *testing.T, evt vibekit.ServerEvent) int64 {
	t.Helper()
	switch p := evt.Payload.(type) {
	case vibekit.PermissionNeededPayload:
		return p.RequestID
	case vibekit.ElicitationNeededPayload:
		return p.RequestID
	case vibekit.UserInputNeededPayload:
		return p.RequestID
	default:
		t.Fatalf("replayed event carries payload %T, want one of the three *_needed payloads", evt.Payload)
		return 0
	}
}

// TestClearForRun_DropsOnlyTheNamedRunsDecisions pins the run-scoped clear's
// predicate: the run comes off the PAYLOAD, because the key does not carry one.
//
// The permission and elicitation rows are the load-bearing ones. All four kinds a
// step can raise are run-scoped — `permission_needed` and `elicitation_needed`
// carry a `RunID` exactly as `user_input_needed` does — so a clear that named only
// the question kind would leave two thirds of the population behind.
func TestClearForRun_DropsOnlyTheNamedRunsDecisions(t *testing.T) {
	t.Parallel()
	const launching vibekit.ChatID = "c-parent"
	entries := []struct {
		id      int64
		name    string
		payload any
		// survives says the entry must still be replayable after ClearForRun("wf_1").
		survives bool
	}{
		{1, "a step's question", vibekit.UserInputNeededPayload{RequestID: 1, RunID: "wf_1"}, false},
		{2, "a step's permission", vibekit.PermissionNeededPayload{RequestID: 2, RunID: "wf_1"}, false},
		{3, "a step's elicitation", vibekit.ElicitationNeededPayload{RequestID: 3, RunID: "wf_1"}, false},
		// A SIBLING run launched from the same chat shares the launching chat's
		// entries, so the clear has to separate them by run rather than by chat.
		{4, "a sibling run's question", vibekit.UserInputNeededPayload{RequestID: 4, RunID: "wf_2"}, true},
		// An ordinary chat ask carries no run at all, and it still blocks a live
		// turn with a card that can answer it.
		{5, "the chat's own permission", vibekit.PermissionNeededPayload{RequestID: 5}, true},
	}
	kindOf := map[int64]vibekit.EventType{
		1: vibekit.EventUserInputNeeded, 2: vibekit.EventPermissionNeeded,
		3: vibekit.EventElicitationNeeded, 4: vibekit.EventUserInputNeeded,
		5: vibekit.EventPermissionNeeded,
	}
	tracker := newPendingPermsTracker()
	for _, e := range entries {
		tracker.Add(e.id, vibekit.NewEvent(kindOf[e.id], launching, e.payload))
	}

	tracker.ClearForRun("wf_1")

	left := map[int64]bool{}
	for _, evt := range tracker.List("") {
		left[requestIDOf(t, evt)] = true
	}
	for _, e := range entries {
		t.Run(e.name, func(t *testing.T) {
			if left[e.id] != e.survives {
				verb := "survived the run's end"
				if e.survives {
					verb = "was swept by another run's end"
				}
				t.Errorf("request %d (%s) %s", e.id, e.name, verb)
			}
		})
	}
}

// TestClearForRun_RefusesAnEmptyRunID: `RunID` is empty on every ordinary chat
// ask, so an empty argument would match the whole tracker rather than one run —
// and the id arrives off a wire frame.
func TestClearForRun_RefusesAnEmptyRunID(t *testing.T) {
	t.Parallel()
	tracker := newPendingPermsTracker()
	tracker.Add(1, vibekit.NewEvent(vibekit.EventPermissionNeeded, "c1",
		vibekit.PermissionNeededPayload{RequestID: 1}))

	tracker.ClearForRun("")

	if got := len(tracker.List("")); got != 1 {
		t.Errorf("an empty run id left %d cards, want the chat's own 1", got)
	}
}
