package agent

// The steering buffer and its connect replay.
//
// The tracker's own subject is which steers are still WAITING — the set a
// reconnecting client's dock has to come back holding — and the reason it exists at
// all is that nothing can read KAS's buffer back: `_session/steer` and
// `_session/steer/clear` are the whole verb set, so a client that missed a frame
// lost the row while the message was still queued.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func queued(id, text string) vibekit.SteerQueuedPayload {
	return vibekit.SteerQueuedPayload{SteerID: id, Text: text, Origin: vibekit.SteerOriginUser}
}

// steerIDsOf reads the ids off a List result, which is the shape the replay writes.
func steerIDsOf(t *testing.T, evts []vibekit.ServerEvent) []string {
	t.Helper()
	out := make([]string, 0, len(evts))
	for _, e := range evts {
		if e.Type != vibekit.EventSteerQueued {
			t.Fatalf("event type = %q, want %q", e.Type, vibekit.EventSteerQueued)
		}
		p, ok := e.Payload.(vibekit.SteerQueuedPayload)
		if !ok {
			t.Fatalf("payload type = %T, want SteerQueuedPayload", e.Payload)
		}
		out = append(out, p.SteerID)
	}
	return out
}

func TestSteerBuffer_ListsWhatIsStillWaiting(t *testing.T) {
	b := newSteerBuffer()
	b.SteerWaiting("c1", queued("steer-1", "use tabs"))
	b.SteerWaiting("c1", queued("steer-2", "and rename it"))

	got := b.List("")
	if want := []string{"steer-1", "steer-2"}; !equalStrings(steerIDsOf(t, got), want) {
		t.Fatalf("ids = %v, want %v", steerIDsOf(t, got), want)
	}
	// The PAYLOAD travels whole, not just the id: the replay must be
	// indistinguishable from the live frame, so the text and the resolved origin
	// have to survive the round trip.
	p, ok := got[0].Payload.(vibekit.SteerQueuedPayload)
	if !ok {
		t.Fatalf("payload type = %T", got[0].Payload)
	}
	if p.Text != "use tabs" || p.Origin != vibekit.SteerOriginUser {
		t.Errorf("payload = %+v, want the text and the origin carried", p)
	}
	if got[0].ChatID != "c1" {
		t.Errorf("chat = %q, want c1", got[0].ChatID)
	}
}

// The model read it, so replaying it would offer a delivered message back to the
// dock. A SIBLING stays behind, or an empty-set assertion would pass for a tracker
// that recorded nothing at all.
func TestSteerBuffer_ReadDropsOnlyThatSteer(t *testing.T) {
	b := newSteerBuffer()
	b.SteerWaiting("c1", queued("steer-1", "one"))
	b.SteerWaiting("c1", queued("steer-2", "two"))

	b.SteerRead("c1", "steer-1")

	if got := steerIDsOf(t, b.List("")); !equalStrings(got, []string{"steer-2"}) {
		t.Fatalf("ids = %v, want [steer-2]", got)
	}
}

// A turn boundary reports exactly which ids it cleared, so the removal is by name.
func TestSteerBuffer_ForgottenDropsEachNamedSteer(t *testing.T) {
	b := newSteerBuffer()
	for _, id := range []string{"steer-1", "steer-2", "steer-3"} {
		b.SteerWaiting("c1", queued(id, "text of "+id))
	}

	b.SteerForgotten("c1", []string{"steer-1", "steer-3"})

	if got := steerIDsOf(t, b.List("")); !equalStrings(got, []string{"steer-2"}) {
		t.Fatalf("ids = %v, want [steer-2]", got)
	}
}

// An empty list is the frame that says nothing was outstanding, so it must not be
// read as drop-everything — that is the shape a whole chat's dock disappears in.
func TestSteerBuffer_ForgottenWithNoIDsDropsNothing(t *testing.T) {
	b := newSteerBuffer()
	b.SteerWaiting("c1", queued("steer-1", "one"))

	b.SteerForgotten("c1", nil)

	if got := steerIDsOf(t, b.List("")); !equalStrings(got, []string{"steer-1"}) {
		t.Fatalf("ids = %v, want the steer kept", got)
	}
}

// The KEY is the pair, never the id alone: one session per chat means two live
// chats holding one id is ordinary, and collapsing them would let one chat's turn
// boundary empty another's dock.
func TestSteerBuffer_KeysByChatAndID(t *testing.T) {
	b := newSteerBuffer()
	b.SteerWaiting("c1", queued("steer-1", "in c1"))
	b.SteerWaiting("c2", queued("steer-1", "in c2"))

	b.SteerRead("c1", "steer-1")

	got := b.List("")
	if len(got) != 1 || got[0].ChatID != "c2" {
		t.Fatalf("remaining = %+v, want c2's row alone", got)
	}
}

// A client subscribed to one chat is served that chat's rows only, exactly as the
// permission replay filters.
func TestSteerBuffer_ListHonoursTheChatFilter(t *testing.T) {
	b := newSteerBuffer()
	b.SteerWaiting("c1", queued("steer-1", "one"))
	b.SteerWaiting("c2", queued("steer-2", "two"))

	if got := steerIDsOf(t, b.List("c2")); !equalStrings(got, []string{"steer-2"}) {
		t.Fatalf("ids = %v, want [steer-2]", got)
	}
}

// Idempotent by key, which a reconnect requires: the queued frame is replayed for a
// steer still waiting, and a second row for one message would be a duplicate chip.
func TestSteerBuffer_WaitingIsIdempotentByID(t *testing.T) {
	b := newSteerBuffer()
	b.SteerWaiting("c1", queued("steer-1", "first text"))
	b.SteerWaiting("c1", queued("steer-1", "corrected text"))

	got := b.List("")
	if len(got) != 1 {
		t.Fatalf("entries = %d, want 1", len(got))
	}
	p, ok := got[0].Payload.(vibekit.SteerQueuedPayload)
	if !ok {
		t.Fatalf("payload type = %T", got[0].Payload)
	}
	if p.Text != "corrected text" {
		t.Errorf("text = %q, want the newer frame's", p.Text)
	}
}

func TestSteerBuffer_IgnoresAnEmptyID(t *testing.T) {
	b := newSteerBuffer()
	b.SteerWaiting("c1", queued("", "nowhere"))
	if got := b.List(""); len(got) != 0 {
		t.Fatalf("entries = %+v, want none", got)
	}
}

// A steer's lifetime is one turn, so a chat that is gone can only hold ids no frame
// will arrive for. A sibling chat's rows must survive, or one chat closing empties
// every other dock.
func TestSteerBuffer_ClearForChatLeavesOtherChatsAlone(t *testing.T) {
	b := newSteerBuffer()
	b.SteerWaiting("c1", queued("steer-1", "one"))
	b.SteerWaiting("c2", queued("steer-2", "two"))

	b.ClearForChat("c1")

	if got := steerIDsOf(t, b.List("")); !equalStrings(got, []string{"steer-2"}) {
		t.Fatalf("ids = %v, want [steer-2]", got)
	}
	// An empty id would match the whole tracker rather than nothing.
	b.ClearForChat("")
	if got := steerIDsOf(t, b.List("")); !equalStrings(got, []string{"steer-2"}) {
		t.Fatalf("ids after clearing \"\" = %v, want [steer-2]", got)
	}
}

// The cap is PER CHAT, so a producer that never sends a removal cannot grow the map
// without limit and cannot evict a quiet chat's rows either.
func TestSteerBuffer_BoundsOneChatWithoutTouchingAnother(t *testing.T) {
	b := newSteerBuffer()
	b.maxN = 3
	b.SteerWaiting("quiet", queued("steer-q", "still here"))
	for _, id := range []string{"steer-1", "steer-2", "steer-3", "steer-4", "steer-5"} {
		b.SteerWaiting("busy", queued(id, "text of "+id))
	}

	if got := steerIDsOf(t, b.List("busy")); len(got) != 3 {
		t.Fatalf("busy chat holds %v, want 3 entries", got)
	}
	if got := steerIDsOf(t, b.List("quiet")); !equalStrings(got, []string{"steer-q"}) {
		t.Fatalf("quiet chat holds %v, want its own row", got)
	}
}

// ---------------------------------------------------------------------------
// The connect replay, end to end over the real SSE handler.
//
// This is also the WIRING assertion: replayPendingSteers lives in its own file and
// is reached by one call in streamInitialState, so if that call goes missing the
// tracker is still populated, still correct and replayed by nobody — a silent gap
// that reads as wired. This case is what turns that into a red test.
// ---------------------------------------------------------------------------

func TestHandleSSE_ReplaysTheSteersStillWaitingInKASsBuffer(t *testing.T) {
	h, _, _ := newTestHub()
	h.bus.steers.SteerWaiting("c1", queued("steer-1", "actually use tabs"))

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events?chat_id=c1", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `"type":"connected"`) {
		t.Fatalf("no handshake, so the stream never opened: %q", body)
	}
	if !strings.Contains(body, `"type":"steer_queued"`) {
		t.Fatalf("a steer still in KAS's buffer was replayed as nothing, so a reconnecting "+
			"client's dock comes back empty while the message is still queued: %q", body)
	}
	if !strings.Contains(body, "actually use tabs") {
		t.Errorf("the replayed frame carries no text, so the chip has nothing to show: %q", body)
	}
}

// A steer the model READ during the outage must not come back: the client's own
// reconcile refuses it by id, but the server should not be offering it either.
func TestHandleSSE_DoesNotReplayASteerTheModelHasRead(t *testing.T) {
	h, _, _ := newTestHub()
	h.bus.steers.SteerWaiting("c1", queued("steer-read", "delivered"))
	h.bus.steers.SteerWaiting("c1", queued("steer-waiting", "still queued"))
	h.bus.steers.SteerRead("c1", "steer-read")

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events?chat_id=c1", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "still queued") {
		t.Fatalf("the waiting steer was not replayed: %q", body)
	}
	if strings.Contains(body, "delivered") {
		t.Errorf("a steer the model had read was re-offered to the dock: %q", body)
	}
}

// The filter, on the real handler: a client subscribed to one chat must not be sent
// another chat's dock rows.
func TestHandleSSE_ReplaysOnlyTheSubscribedChatsSteers(t *testing.T) {
	h, _, _ := newTestHub()
	h.bus.steers.SteerWaiting("c1", queued("steer-1", "for c1"))
	h.bus.steers.SteerWaiting("c2", queued("steer-2", "for c2"))

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events?chat_id=c1", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	h.handleSSE(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "for c1") {
		t.Fatalf("the subscribed chat's steer was not replayed: %q", body)
	}
	if strings.Contains(body, "for c2") {
		t.Errorf("another chat's steer was replayed: %q", body)
	}
}

// The chat teardown reaches BOTH steer registries. They answer different questions
// — whose words an id carried, and which ids are still waiting — so neither clears
// the other, and a waiting row left behind would be replayed for a chat that is
// gone.
func TestCleanupChatState_ClearsTheWaitingSteers(t *testing.T) {
	h, _, _ := newTestHub()
	h.bus.steers.SteerWaiting("c1", queued("steer-1", "one"))
	h.bus.steers.SteerWaiting("c2", queued("steer-2", "two"))

	h.cleanupChatState(t.Context(), "c1", false)

	if got := steerIDsOf(t, h.bus.steers.List("")); !equalStrings(got, []string{"steer-2"}) {
		t.Fatalf("ids = %v, want only the surviving chat's", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
