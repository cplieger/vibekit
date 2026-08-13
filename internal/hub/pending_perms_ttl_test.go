package hub

// The TTL's whole job is to stop offering a decision the agent server has
// stopped waiting for. So the assertions are about what an EXPIRED entry can no
// longer do — be replayed to a reconnecting client, or be answered — plus the
// bounded-growth half, which is why the sweep sits in Add.

import (
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// backdate inserts an entry that expired ago. It writes the map directly rather
// than injecting a clock: the expiry instant IS the fixture, and a production
// clock seam would exist only for this test.
func backdate(t *testing.T, tracker *pendingPermsTracker, id int64, chatID api.ChatID, ago time.Duration) {
	t.Helper()
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.perms[id] = pendingPerm{
		evt:     api.NewEvent(api.EventPermissionNeeded, chatID, api.PermissionNeededPayload{RequestID: id}),
		expires: time.Now().Add(-ago),
	}
}

func TestPendingPermsTracker_AddAppliesTheTTL(t *testing.T) {
	tracker := newPendingPermsTracker()
	before := time.Now()
	tracker.Add(1, api.NewEvent(api.EventPermissionNeeded, "c1", api.PermissionNeededPayload{RequestID: 1}))

	tracker.mu.Lock()
	got := tracker.perms[1].expires
	tracker.mu.Unlock()

	// A window rather than an instant: the only claim is that Add dates the
	// entry from its own arrival plus the TTL, not that it read the clock at any
	// particular nanosecond.
	earliest := before.Add(pendingPermTTL)
	latest := time.Now().Add(pendingPermTTL)
	if got.Before(earliest) || got.After(latest) {
		t.Errorf("expires = %v, want within [%v, %v]", got, earliest, latest)
	}
}

// TestPendingPermsTracker_ExpiredIsNeitherReplayedNorTakeable is the pair of
// consequences that matter: the connect-time replay reads List, and every answer
// path reads TakeIfPresent, so an entry past the TTL has to be invisible to both
// or the two surfaces disagree about whether the question is still live.
func TestPendingPermsTracker_ExpiredIsNeitherReplayedNorTakeable(t *testing.T) {
	tracker := newPendingPermsTracker()
	backdate(t, tracker, 1, "c1", time.Second)

	if got := tracker.List(""); len(got) != 0 {
		t.Errorf("List() replayed %d expired entries, want 0: %+v", len(got), got)
	}
	if got := tracker.List("c1"); len(got) != 0 {
		t.Errorf("List(chat) replayed %d expired entries, want 0: %+v", len(got), got)
	}
	if _, ok := tracker.TakeIfPresent(1); ok {
		t.Error("TakeIfPresent claimed an expired request, want refused")
	}
}

func TestPendingPermsTracker_LiveEntrySurvives(t *testing.T) {
	// The inverse of the case above, so an expiry rule that hides everything
	// cannot pass. Same fixture shape, dated forward instead of back.
	tracker := newPendingPermsTracker()
	backdate(t, tracker, 1, "c1", -pendingPermTTL)

	if got := tracker.List(""); len(got) != 1 {
		t.Fatalf("List() returned %d live entries, want 1", len(got))
	}
	if _, ok := tracker.TakeIfPresent(1); !ok {
		t.Error("TakeIfPresent refused a live request")
	}
}

// TestPendingPermsTracker_AddSweepsExpired covers the bounded-growth half. A
// request nobody ever answers (a bridge that died mid-request) is only removed
// by this sweep, and Add is where it runs because Add is the only operation that
// grows the map.
func TestPendingPermsTracker_AddSweepsExpired(t *testing.T) {
	tracker := newPendingPermsTracker()
	for id := range int64(5) {
		backdate(t, tracker, id, "c1", time.Minute)
	}
	live := int64(99)
	backdate(t, tracker, live, "c1", -pendingPermTTL)

	tracker.Add(100, api.NewEvent(api.EventPermissionNeeded, "c2", api.PermissionNeededPayload{RequestID: 100}))

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if len(tracker.perms) != 2 {
		t.Errorf("map holds %d entries after the sweep, want 2 (the live one and the new one)", len(tracker.perms))
	}
	if _, ok := tracker.perms[live]; !ok {
		t.Error("the sweep dropped a live entry")
	}
	if _, ok := tracker.perms[100]; !ok {
		t.Error("Add did not record the new entry")
	}
}
