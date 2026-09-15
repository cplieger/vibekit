package push

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/liveness"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// deferredOn holds the switch on for one test, whatever its default, and shrinks
// the re-judge poll so a held delivery is observed in milliseconds. Serial: both
// are package vars.
func deferredOn(t *testing.T) {
	t.Helper()
	prevOn, prevPoll := deferSuppressedSends, deferPoll
	deferSuppressedSends, deferPoll = true, 5*time.Millisecond
	t.Cleanup(func() { deferSuppressedSends, deferPoll = prevOn, prevPoll })
}

// deferredOff holds the switch off for one test: the plain drop.
func deferredOff(t *testing.T) {
	t.Helper()
	prev := deferSuppressedSends
	deferSuppressedSends = false
	t.Cleanup(func() { deferSuppressedSends = prev })
}

// payloadHandler records the decrypted-side envelope size is not observable, so it
// records arrivals and the request count; the payload identity rides the title
// through a plaintext side channel the test does not have. It answers 201.
type payloadHandler struct {
	mu   sync.Mutex
	hits int
}

func (h *payloadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	h.mu.Lock()
	h.hits++
	h.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
}

func (h *payloadHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits
}

// waitFor polls cond until it holds or the deadline passes, failing closed with
// the diagnostic.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// As shipped, a suppressed send is held: the hold is the default, not a test mode.
func TestDeferred_DefaultHoldsASuppressedSend(t *testing.T) {
	h := &payloadHandler{}
	s, _ := filteredService(t, h)
	s.Unsubscribe(goneEP)
	s.presence.Observe(connected(TagOf(presentEP)))

	s.Send(t.Context(), "title", "body", vibekit.PushKindPermission, vibekit.ChatSubject("c1"))

	if n := s.heldCount(); n != 1 {
		t.Errorf("held deliveries with the default switch = %d, want 1", n)
	}
	if h.count() != 0 {
		t.Error("a held delivery landed while the profile still read present")
	}
}

// With the switch off a suppressed send holds nothing: the plain drop.
func TestDeferred_OffHoldsNothing(t *testing.T) {
	deferredOff(t)
	h := &payloadHandler{}
	s, _ := filteredService(t, h)
	s.Unsubscribe(goneEP)
	s.presence.Observe(connected(TagOf(presentEP)))

	s.Send(t.Context(), "title", "body", vibekit.PushKindPermission, vibekit.ChatSubject("c1"))

	if n := s.heldCount(); n != 0 {
		t.Errorf("held deliveries with the switch off = %d, want 0", n)
	}
}

func TestDeferred_HeldDeliveryLandsWhenTheProfileFlipsToGone(t *testing.T) {
	deferredOn(t)
	h := &payloadHandler{}
	s, clock := filteredService(t, h)
	s.Unsubscribe(goneEP)
	s.presence.Observe(connected(TagOf(presentEP)))

	s.Send(t.Context(), "title", "body", vibekit.PushKindPermission, vibekit.ChatSubject("c1"))
	if n := s.heldCount(); n != 1 {
		t.Fatalf("held deliveries = %d, want 1", n)
	}
	time.Sleep(4 * deferPoll)
	if h.count() != 0 {
		t.Fatal("a held delivery landed while the profile still read present")
	}

	clock.Advance(liveness.AliveWindow + time.Millisecond)
	waitFor(t, "the held delivery", func() bool { return h.count() == 1 })
	if n := s.heldCount(); n != 0 {
		t.Errorf("held deliveries after the release = %d, want 0", n)
	}
}

func TestDeferred_HeldDeliveryIsDroppedAtItsTTL(t *testing.T) {
	deferredOn(t)
	h := &payloadHandler{}
	s, clock := filteredService(t, h)
	s.Unsubscribe(goneEP)
	s.presence.Observe(connected(TagOf(presentEP)))

	s.Send(t.Context(), "title", "body", vibekit.PushKindPermission, vibekit.ChatSubject("c1"))
	// Past the permission TTL the profile also reads gone; the TTL is judged first.
	clock.Advance(ttlPermission + time.Millisecond)
	waitFor(t, "the held set to empty", func() bool { return s.heldCount() == 0 })
	time.Sleep(4 * deferPoll)
	if h.count() != 0 {
		t.Error("a delivery past its TTL was sent")
	}
}

func TestDeferred_RetractionBeforeTheFlipDeliversNothing(t *testing.T) {
	deferredOn(t)
	h := &payloadHandler{}
	s, clock := filteredService(t, h)
	s.Unsubscribe(goneEP)
	s.presence.Observe(connected(TagOf(presentEP)))

	s.Send(t.Context(), "title", "body", vibekit.PushKindPermission, vibekit.ChatSubject("c1"))
	s.Retract(vibekit.ChatSubject("c1"))
	if n := s.heldCount(); n != 0 {
		t.Fatalf("held deliveries after the retraction = %d, want 0", n)
	}
	clock.Advance(liveness.AliveWindow + time.Millisecond)
	time.Sleep(4 * deferPoll)
	if h.count() != 0 {
		t.Error("a retracted delivery was sent")
	}
}

// One entry per (tag, kind, subject): a second event replaces the held payload, so
// what lands is the latest, and the queue cannot grow past the subscription count
// per kind and subject.
func TestDeferred_ASecondEventReplacesTheHeldPayload(t *testing.T) {
	deferredOn(t)
	h := &payloadHandler{}
	s, clock := filteredService(t, h)
	s.Unsubscribe(goneEP)
	s.presence.Observe(connected(TagOf(presentEP)))

	s.Send(t.Context(), "first", "body", vibekit.PushKindPermission, vibekit.ChatSubject("c1"))
	resetDebounce(s)
	s.Send(t.Context(), "second", "body", vibekit.PushKindPermission, vibekit.ChatSubject("c1"))
	if n := s.heldCount(); n != 1 {
		t.Fatalf("held deliveries after two events on one key = %d, want 1", n)
	}
	key := heldKey{tag: TagOf(presentEP), kind: vibekit.PushKindPermission, subject: "c1"}
	s.deferred.mu.Lock()
	held := s.deferred.held[key]
	s.deferred.mu.Unlock()
	var p pushPayload
	if err := json.Unmarshal(held.payload, &p); err != nil {
		t.Fatalf("held payload is not the envelope: %v", err)
	}
	if p.Title != "second" {
		t.Errorf("held title = %q, want the later event's", p.Title)
	}

	clock.Advance(liveness.AliveWindow + time.Millisecond)
	waitFor(t, "the held delivery", func() bool { return h.count() == 1 })
}
