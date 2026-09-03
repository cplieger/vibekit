// The dashboard's status answer is a SNAPSHOT, not a scan.
//
// /api/git/status-all used to run the whole fan-out inside the request, and the
// client polls it every 15 seconds: on a workspace of ~55 repositories the first
// caller waited up to the whole-scan budget while the rest queued behind one
// singleflight slot. So a scan publishes into a holder and a read answers from what
// the holder has plus its age; only the FIRST read of a process and a
// user-initiated `?fetch=1` still wait.

package git

import (
	"sync"
	"time"
)

// The two snapshot variants, kept apart so a cheap poll never piggybacks a
// fetch-less answer onto a forced refresh (or the reverse): `?fetch=1` runs a
// network fetch per repo and reports ahead/behind against the refreshed ref,
// which is a different answer from the poll's.
const (
	statusKeyPoll  = "status-all"
	statusKeyFetch = "status-all-fetch"
)

// statusSnapshot is one completed scan and the instant it completed.
type statusSnapshot struct {
	at    time.Time
	repos []allRepoStatus
}

// statusCache holds the newest completed scan per variant and admits ONE refresh
// at a time for each.
//
// The one-at-a-time rule is the singleflight's, kept for its reason: boot fires
// several concurrent callers (the changes tab plus the badge poll), and N scans
// would be N times the subprocesses for one answer. What changed is who waits.
type statusCache struct {
	slots map[string]*statusSlot
	mu    sync.Mutex
}

// statusSlot is one variant's snapshot plus the refresh in flight for it.
type statusSlot struct {
	snap *statusSnapshot
	// done is closed when the refresh in flight publishes, and nil when none is.
	// A fresh channel per refresh, so a waiter always holds the one it was handed
	// rather than one a later refresh replaced.
	done chan struct{}
}

// read returns the newest snapshot for key, nil when no scan has completed yet,
// and the channel of the refresh in flight, nil when none is.
func (c *statusCache) read(key string) (snap *statusSnapshot, running chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	slot := c.slots[key]
	if slot == nil {
		return nil, nil
	}
	return slot.snap, slot.done
}

// claim reserves the refresh slot for key. started is false when a refresh is
// already in flight; the returned channel is the one to wait on either way, and
// it closes when that refresh publishes.
func (c *statusCache) claim(key string) (done chan struct{}, started bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.slots == nil {
		c.slots = make(map[string]*statusSlot, 2)
	}
	slot := c.slots[key]
	if slot == nil {
		slot = &statusSlot{}
		c.slots[key] = slot
	}
	if slot.done != nil {
		return slot.done, false
	}
	slot.done = make(chan struct{})
	return slot.done, true
}

// publish stores a completed scan for key and releases the refresh slot, waking
// every caller that joined it. The caller of claim owns this call — including for a
// scan that produced nothing, or the slot stays claimed and the variant never
// refreshes again.
func (c *statusCache) publish(key string, repos []allRepoStatus) {
	c.mu.Lock()
	slot := c.slots[key]
	if slot == nil {
		slot = &statusSlot{}
		c.slots[key] = slot
	}
	slot.snap = &statusSnapshot{at: time.Now(), repos: repos}
	done := slot.done
	slot.done = nil
	c.mu.Unlock()
	if done != nil {
		close(done)
	}
}

// stale reports whether a snapshot is old enough to refresh behind the answer.
// A missing snapshot is stale by definition.
func (s *statusSnapshot) stale(maxAge time.Duration) bool {
	return s == nil || time.Since(s.at) > maxAge
}

// age is how old the snapshot is, and zero when there is none.
func (s *statusSnapshot) age() time.Duration {
	if s == nil {
		return 0
	}
	return time.Since(s.at)
}

// rows is the snapshot's repositories, never nil: the wire contract is a
// non-nullable array, and the client's `for (const r of repos)` throws on null.
func (s *statusSnapshot) rows() []allRepoStatus {
	if s == nil || s.repos == nil {
		return []allRepoStatus{}
	}
	return s.repos
}
