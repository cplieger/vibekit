package push

import (
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/liveness"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// deferSuppressedSends is the one switch over what the presence filter does with a
// push it suppresses. On, the default: the delivery is HELD and lands when the
// profile flips to gone inside the kind's TTL, so an ask raised while a locked
// phone still reads present reaches it once the alive window closes on the lock.
// Off is the plain drop: not pushed to that profile, then or later, and the person
// learns of it from the pending snapshot at unlock. A var only so its tests can
// turn it off; production never reassigns it.
var deferSuppressedSends = true

// deferPoll is how often the held set is re-judged against the presence table.
// One beat: presence itself moves at that granularity. A var so a test can shrink
// the wait rather than the window.
var deferPoll = liveness.Keepalive

// heldKey bounds the queue at one entry per profile, kind and subject: a second
// event for the same triple replaces the held payload, so what eventually lands is
// the latest.
type heldKey struct {
	tag     string
	subject string
	kind    vibekit.PushKind
}

// heldPush is one suppressed delivery waiting for its profile to read gone.
type heldPush struct {
	expiresAt time.Time
	sub       vibekit.PushSubscription
	payload   []byte
}

// deferred is the per-service held set. Its clock is the presence table's, so the
// verdict and the TTL move together.
type deferred struct {
	held  map[heldKey]heldPush
	timer *time.Timer
	mu    sync.Mutex
}

// holdForLater records a suppressed delivery when the variant is on, and arms the
// re-judge if none is armed. With the switch off it is a no-op, which is the drop.
func (s *Service) holdForLater(
	sub vibekit.PushSubscription, kind vibekit.PushKind, subject vibekit.PushSubject, payload []byte,
) {
	if !deferSuppressedSends || s.presence == nil {
		return
	}
	key := heldKey{tag: TagOf(sub.Endpoint), kind: kind, subject: debounceKey(kind, subject).subject}
	s.deferred.mu.Lock()
	defer s.deferred.mu.Unlock()
	if s.deferred.held == nil {
		s.deferred.held = make(map[heldKey]heldPush)
	}
	s.deferred.held[key] = heldPush{
		sub:       sub,
		payload:   payload,
		expiresAt: s.presence.now().Add(ttlDuration(kind)),
	}
	if s.deferred.timer == nil {
		s.deferred.timer = time.AfterFunc(deferPoll, s.releaseHeld)
	}
}

// Retract drops every held delivery about subject: the ask was answered on some
// surface, so a nudge about it has nothing left to say. A no-op with the variant
// off, since nothing is ever held.
func (s *Service) Retract(subject vibekit.PushSubject) {
	if !deferSuppressedSends {
		return
	}
	want := debounceKey("", subject).subject
	s.deferred.mu.Lock()
	defer s.deferred.mu.Unlock()
	for key := range s.deferred.held {
		if key.subject == want {
			delete(s.deferred.held, key)
		}
	}
}

// releaseHeld re-judges every held delivery: past its TTL it is dropped, a profile
// now gone gets its push, a profile still present waits another poll. Re-arms
// itself while anything is held; the service lifetime ending stops the sends
// through the fan-out's context.
func (s *Service) releaseHeld() {
	if s.lifetime.Err() != nil {
		return
	}
	now := s.presence.now()
	var due []struct {
		key heldKey
		h   heldPush
	}
	s.deferred.mu.Lock()
	for key, h := range s.deferred.held {
		switch {
		case now.After(h.expiresAt):
			delete(s.deferred.held, key)
			slog.Debug("push: held delivery expired unsent", "kind", string(key.kind), "tag", key.tag)
		case s.presence.Gone(key.tag):
			delete(s.deferred.held, key)
			due = append(due, struct {
				key heldKey
				h   heldPush
			}{key, h})
		}
	}
	if len(s.deferred.held) == 0 {
		s.deferred.timer = nil
	} else {
		s.deferred.timer = time.AfterFunc(deferPoll, s.releaseHeld)
	}
	s.deferred.mu.Unlock()
	for i := range due {
		d := &due[i]
		slog.Debug("push: held delivery released, profile gone", "kind", string(d.key.kind), "tag", d.key.tag)
		s.fanOut(s.lifetime, []vibekit.PushSubscription{d.h.sub}, d.h.payload, d.key.kind)
	}
}

// stop disarms the re-judge timer at Close; a poll already running sees the
// cancelled lifetime and sends nothing.
func (d *deferred) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}

// heldCount is the number of deliveries waiting; the tests read it.
func (s *Service) heldCount() int {
	s.deferred.mu.Lock()
	defer s.deferred.mu.Unlock()
	return len(s.deferred.held)
}
