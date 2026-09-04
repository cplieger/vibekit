// The dashboard's status answer is a SNAPSHOT, not a scan.
//
// /api/git/status-all used to run the whole fan-out inside the request: on a
// workspace of ~55 repositories the first caller waited up to the whole-scan budget
// while the rest queued behind one singleflight slot. So a scan publishes into a
// holder and a read answers from what the holder has plus its age; only the FIRST
// read of a process and a user-initiated `?fetch=1` still wait.

package git

import (
	"maps"
	"slices"
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
// The one-at-a-time rule is the singleflight's, kept for its reason: N scans would
// be N times the subprocesses for one answer. What changed is who waits — and it
// matters more now than it did under the client's old timer, because reads arrive in
// BURSTS, several a turn, whenever something writes to the tree.
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
	// pending is the repositories named by scoped reads that arrived while the
	// refresh in flight was running, and it is what stops the one-at-a-time rule
	// LOSING work: a scoped read joining a scan of a different repo would
	// otherwise return without its own repo ever being looked at, and its rows
	// would stay stale until the next full scan. The running refresh drains this
	// and keeps going.
	pending map[string]struct{}
}

// slot returns key's slot, creating it. Callers hold c.mu.
func (c *statusCache) slot(key string) *statusSlot {
	if c.slots == nil {
		c.slots = make(map[string]*statusSlot, 2)
	}
	s := c.slots[key]
	if s == nil {
		s = &statusSlot{}
		c.slots[key] = s
	}
	return s
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
	slot := c.slot(key)
	if slot.done != nil {
		return slot.done, false
	}
	slot.done = make(chan struct{})
	return slot.done, true
}

// claimScoped reserves the refresh slot for a SCOPED scan of `only`. When a
// refresh is already in flight those repositories are recorded as pending instead,
// so whoever owns that refresh rescans them rather than this read's work being
// dropped — the in-flight scan may be scoped to a different repository entirely.
//
// A pending set is unnecessary against a FULL scan, which covers every repo; its
// publish clears the set for that reason.
func (c *statusCache) claimScoped(key string, only map[string]struct{}) (done chan struct{}, started bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	slot := c.slot(key)
	if slot.done != nil {
		if slot.pending == nil {
			slot.pending = make(map[string]struct{}, len(only))
		}
		maps.Copy(slot.pending, only)
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
	slot := c.slot(key)
	slot.snap = &statusSnapshot{at: time.Now(), repos: repos}
	done := slot.done
	slot.done = nil
	// A full scan looked at every repository, so anything a scoped read asked for
	// while it ran is already answered.
	slot.pending = nil
	c.mu.Unlock()
	if done != nil {
		close(done)
	}
}

// mergeScoped folds a PARTIAL scan into key's snapshot — rows replace their
// namesakes, an unknown repo is appended, every other row survives — and returns
// the scope that accumulated while the scan ran. Owned by claimScoped's caller.
//
// A NON-EMPTY return means the slot stays claimed under a fresh channel and the
// caller must scan again; an empty one releases it. Either way the old channel's
// waiters are woken, because the snapshot they wait on moved. `at` does NOT move:
// it answers when the WHOLE tree was last known.
func (c *statusCache) mergeScoped(key string, rows []allRepoStatus) map[string]struct{} {
	c.mu.Lock()
	slot := c.slot(key)
	if slot.snap == nil {
		// No snapshot to merge into: this is the case statusScope refuses to
		// produce, and publishing a partial scan as a whole one is the harm it
		// refuses it for.
		slot.snap = &statusSnapshot{at: time.Now()}
	}
	merged := slices.Clone(slot.snap.repos)
	for _, row := range rows {
		if i := slices.IndexFunc(merged, func(have allRepoStatus) bool {
			return have.Repo == row.Repo
		}); i >= 0 {
			merged[i] = row
			continue
		}
		merged = append(merged, row)
	}
	slot.snap = &statusSnapshot{at: slot.snap.at, repos: merged}
	done := slot.done
	next := slot.pending
	slot.pending = nil
	if len(next) == 0 {
		slot.done = nil
	} else {
		// Still claimed, under a channel the next pass will close. Swapping it
		// under the lock is what stops a read arriving between the two from
		// starting a second concurrent scan.
		slot.done = make(chan struct{})
	}
	c.mu.Unlock()
	if done != nil {
		close(done)
	}
	return next
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
