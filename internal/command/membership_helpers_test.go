package command

// Test construction for the membership coordinator.
//
// Two shapes, because the two halves of the coordinator are independently
// interesting. Most command tests care only about the CHAT half — what a create
// writes, what a refusal leaves behind — and get a coordinator with no tab store,
// which is the same unwired state a build with no config dir has. The tests that
// are about MEMBERSHIP get a real tabs.Store over a temp dir, because the
// ordering, the versions and the events are what they assert and a fake store
// would be asserting against the fake.

import (
	"context"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// newTestMembership builds a coordinator over a chat store and NO tab store.
//
// Bus, Teardown and CloseChat are deliberately absent: a coordinator with no bus
// emits nothing (which these tests do not read) and the two teardown seams are
// only reached by the delete and close paths, which have their own tests with
// their own doubles. Leaving them nil is what keeps this helper from being a
// second wiring table to maintain.
func newTestMembership(t *testing.T, chats ChatStore) *Membership {
	t.Helper()
	return NewMembership(MembershipDeps{Chats: chats})
}

// newTabbedMembership builds a coordinator over a chat store and a REAL tab store
// in a temp dir, plus a recording bus.
//
// The real store rather than a fake, because every property these tests assert —
// the version a mutation produced, the order it left behind, one event per
// committed mutation — is the store's own behaviour under the coordinator's lock.
// A fake would let both sides agree while being wrong together.
func newTabbedMembership(t *testing.T, chats ChatStore) (*Membership, *tabs.Store, *tabBus) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	bus := &tabBus{}
	return NewMembership(MembershipDeps{Chats: chats, Tabs: st, Bus: bus}), st, bus
}

// tabBus records the tabs_changed frames a coordinator emitted, under its own
// mutex.
//
// A second recording bus beside capturingBus rather than reusing it, for one
// reason: these tests run under -race with several goroutines mutating one
// coordinator, and capturingBus appends without a lock. A shared double that is
// only safe for some of its users is worse than two.
type tabBus struct {
	events []vibekit.ServerEvent
	mu     sync.Mutex
}

func (b *tabBus) Broadcast(_ context.Context, evt vibekit.ServerEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, evt)
}

// frames returns the tabs_changed payloads seen so far, in arrival order. The
// type assertion is a Fatalf rather than a skip: a frame of another shape on this
// event type is the bug, not a case to tolerate.
func (b *tabBus) frames(t *testing.T) []vibekit.TabsChangedPayload {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []vibekit.TabsChangedPayload
	for _, evt := range b.events {
		if evt.Type != vibekit.EventTabsChanged {
			continue
		}
		p, ok := evt.Payload.(vibekit.TabsChangedPayload)
		if !ok {
			t.Fatalf("tabs_changed payload = %T, want vibekit.TabsChangedPayload", evt.Payload)
		}
		out = append(out, p)
	}
	return out
}

// removedIDs is every id the bus reported as closed, across every frame. The
// delete paths assert on the union rather than per frame, because a close of a
// parent with children is one frame while a retention sweep of two tabs is two.
func (b *tabBus) removedIDs(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, p := range b.frames(t) {
		out = append(out, p.RemovedIDs...)
	}
	return out
}
