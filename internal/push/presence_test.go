package push

import (
	"sync"
	"testing"
	"time"

	"github.com/cplieger/sse"
	"github.com/cplieger/vibekit/internal/liveness"
)

// presenceClock drives a Presence through time without sleeping. Locked, because
// the deferred variant's poll timer reads it off the test's goroutine.
type presenceClock struct {
	at time.Time
	mu sync.Mutex
}

func (c *presenceClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *presenceClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func newPresenceAt(c *presenceClock) *Presence { p := NewPresence(); p.now = c.Now; return p }

func connected(tag string) sse.PresenceEvent {
	return sse.PresenceEvent{Kind: sse.PresenceConnected, Tag: tag}
}

func disconnected(tag string, c sse.PresenceCause) sse.PresenceEvent {
	return sse.PresenceEvent{Kind: sse.PresenceDisconnected, Tag: tag, Cause: c}
}

const beat = liveness.Keepalive

func TestPresence_UnseenTagIsGone(t *testing.T) {
	p := newPresenceAt(&presenceClock{at: time.Unix(1_700_000_000, 0)})
	if !p.Gone("never-seen") {
		t.Error("Gone(unseen) = false, want true")
	}
	if rows := p.Rows(); len(rows) != 0 {
		t.Errorf("Rows() after a Gone read = %v, want none (a read creates no row)", rows)
	}
}

// A fresh connection reads present before its first acknowledgement: the hello
// completed a request round trip, and an event in the beat before the first
// acknowledgement would otherwise be pushed to a present profile.
func TestPresence_ConnectedSeedsTheAcknowledgement(t *testing.T) {
	clock := &presenceClock{at: time.Unix(1_700_000_000, 0)}
	p := newPresenceAt(clock)
	p.Observe(connected("t1"))
	if p.Gone("t1") {
		t.Error("Gone(t1) right after connected = true, want false")
	}
	rows := p.Rows()
	if len(rows) != 1 || rows[0].Connected != 1 || !rows[0].LastAliveAt.Equal(clock.at) {
		t.Fatalf("Rows() = %+v, want one row connected 1 with lastAliveAt seeded at connect", rows)
	}
	clock.Advance(beat)
	if p.Gone("t1") {
		t.Error("Gone(t1) one beat after connected with no acknowledgement = true, want false (inside the window)")
	}
}

func TestPresence_EveryCauseIsOneDeparture(t *testing.T) {
	causes := []sse.PresenceCause{
		sse.PresenceClosed, sse.PresenceDead, sse.PresenceEvicted, sse.PresenceShutdown, sse.PresenceHookFailed,
	}
	for _, cause := range causes {
		t.Run(string(cause), func(t *testing.T) {
			clock := &presenceClock{at: time.Unix(1_700_000_000, 0)}
			p := newPresenceAt(clock)
			p.Observe(connected("t1"))
			p.Observe(connected("t1"))
			p.Observe(disconnected("t1", cause))
			if got := p.Rows()[0].Connected; got != 1 {
				t.Errorf("connected after one %s departure = %d, want 1", cause, got)
			}
			p.Observe(disconnected("t1", cause))
			clock.Advance(liveness.ReconnectDelay)
			if !p.Gone("t1") {
				t.Errorf("Gone(t1) one retry interval after the last %s departure = false, want true", cause)
			}
		})
	}
}

// A reconnecting tab aborts its old connection and opens the new one within the
// retry interval; the departure must not read as an absence in between, and the
// transition counters must not record a pair for a page that never left.
func TestPresence_ReconnectInsideTheRetryIntervalIsNotAnAbsence(t *testing.T) {
	clock := &presenceClock{at: time.Unix(1_700_000_000, 0)}
	p := newPresenceAt(clock)
	p.Observe(connected("t1"))
	p.Observe(disconnected("t1", sse.PresenceEvicted))
	clock.Advance(liveness.ReconnectDelay / 2)
	if p.Gone("t1") {
		t.Fatal("Gone(t1) half a retry interval after the departure = true, want false")
	}
	p.Observe(connected("t1"))
	if p.Gone("t1") {
		t.Error("Gone(t1) after the reconnect = true, want false")
	}
	if alive, expired := p.Transitions(); alive != 1 || expired != 0 {
		t.Errorf("Transitions() = (alive %d, expired %d), want (1, 0): the reconnect records nothing", alive, expired)
	}
}

func TestPresence_DepartureCountsAfterTheRetryInterval(t *testing.T) {
	clock := &presenceClock{at: time.Unix(1_700_000_000, 0)}
	p := newPresenceAt(clock)
	p.Observe(connected("t1"))
	p.Observe(disconnected("t1", sse.PresenceClosed))
	clock.Advance(liveness.ReconnectDelay)
	if !p.Gone("t1") {
		t.Error("Gone(t1) one retry interval after the deliberate close = false, want true")
	}
	if alive, expired := p.Transitions(); alive != 1 || expired != 0 {
		t.Errorf("Transitions() = (alive %d, expired %d), want (1, 0): a departure is not an expiry", alive, expired)
	}
}

// The hidden-for-61-s tab and the acknowledgement-disabled tab are one table
// transition: the socket still reads connected, the acknowledgements stopped.
func TestPresence_ConnectedButSilentExpiresAtTheWindow(t *testing.T) {
	clock := &presenceClock{at: time.Unix(1_700_000_000, 0)}
	p := newPresenceAt(clock)
	p.Observe(connected("t1"))
	clock.Advance(beat)
	p.Alive("t1")
	clock.Advance(liveness.AliveWindow)
	if p.Gone("t1") {
		t.Fatal("Gone(t1) exactly one window after the last acknowledgement = true, want false (the bound is exclusive)")
	}
	clock.Advance(time.Millisecond)
	if !p.Gone("t1") {
		t.Fatal("Gone(t1) past the window with the socket still connected = false, want true")
	}
	if rows := p.Rows(); rows[0].Connected != 1 || !rows[0].Gone {
		t.Errorf("Rows() = %+v, want connected 1 and gone", rows)
	}
	p.Alive("t1")
	if p.Gone("t1") {
		t.Error("Gone(t1) after the next acknowledgement = true, want false")
	}
	if alive, expired := p.Transitions(); alive != 2 || expired != 1 {
		t.Errorf("Transitions() = (alive %d, expired %d), want (2, 1)", alive, expired)
	}
}

// A late acknowledgement after the last departure recreates nothing that leaks:
// the row survives one window and is swept on the next event, not before.
func TestPresence_ZeroCountRowIsSweptOneWindowAfterItsLastAcknowledgement(t *testing.T) {
	clock := &presenceClock{at: time.Unix(1_700_000_000, 0)}
	p := newPresenceAt(clock)
	p.Observe(connected("t1"))
	p.Observe(disconnected("t1", sse.PresenceClosed))
	p.Alive("t1") // in flight when the socket closed
	p.Observe(connected("other"))
	clock.Advance(liveness.AliveWindow)
	p.Alive("other")
	if len(p.Rows()) != 2 {
		t.Fatalf("Rows() exactly one window after t1's last acknowledgement = %+v, want t1 still held", p.Rows())
	}
	clock.Advance(time.Millisecond)
	p.Alive("other")
	rows := p.Rows()
	if len(rows) != 1 || rows[0].Tag != "other" {
		t.Errorf("Rows() past the window = %+v, want only the connected tag", rows)
	}
	if !p.Gone("t1") {
		t.Error("Gone(t1) after the sweep = false, want true")
	}
}

// An acknowledgement that beats the hook's connected creates the row so the
// receipt is not lost, and reads gone until the connection is counted.
func TestPresence_AcknowledgementAloneDoesNotMakeATagPresent(t *testing.T) {
	clock := &presenceClock{at: time.Unix(1_700_000_000, 0)}
	p := newPresenceAt(clock)
	p.Alive("t1")
	if !p.Gone("t1") {
		t.Error("Gone(t1) on an acknowledgement with no connection = false, want true")
	}
	p.Observe(connected("t1"))
	if p.Gone("t1") {
		t.Error("Gone(t1) once the connection is counted = true, want false")
	}
	if rows := p.Rows(); len(rows) != 1 || !rows[0].LastAliveAt.Equal(clock.at) {
		t.Errorf("Rows() = %+v, want the acknowledgement's row adopted by the connect", rows)
	}
}

func TestPresence_UntaggedEventsAreDropped(t *testing.T) {
	p := newPresenceAt(&presenceClock{at: time.Unix(1_700_000_000, 0)})
	p.Observe(connected(""))
	p.Observe(disconnected("", sse.PresenceClosed))
	if rows := p.Rows(); len(rows) != 0 {
		t.Errorf("Rows() after untagged events = %+v, want none", rows)
	}
}
