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
	// pending and pendingFull are what reads asked for while the refresh in flight
	// was running, and they are what stop the one-at-a-time rule LOSING work rather
	// than merely delaying it. Both kinds of read can join both kinds of scan:
	//
	//	scoped joins scoped	pending      - the scan running may cover another repo
	//	scoped joins full	(nothing)    - answered: a full scan covers every repo
	//	full   joins scoped	pendingFull  - NOT answered: a scoped scan leaves `at`
	//	                                       fixed, so the whole-tree read is served
	//	                                       a snapshot that stays stale and the
	//	                                       scan it asked for never happens
	//	full   joins full	(nothing)    - answered by the scan already running
	//
	// The refresh in flight drains both and keeps going.
	pending     map[string]struct{}
	pendingFull bool
	// full says the refresh in flight covers every repository, which is what decides
	// whether a joining read needs recording at all and whether the pass publishes
	// or merges.
	full bool
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

// claim reserves the refresh slot for a scan of `only`, nil meaning the whole tree
// — the same contract scanRepos takes. started is false when a refresh is already
// in flight; the returned channel is the one to wait on either way, and it closes
// when that refresh publishes.
//
// A joining read's intent is RECORDED for the refresh in flight to drain, so the
// one-at-a-time rule delays work instead of dropping it. Nothing is recorded
// against a full scan, which answers every read by covering every repository; the
// statusSlot comment carries all four directions.
func (c *statusCache) claim(key string, only map[string]struct{}) (done chan struct{}, started bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	slot := c.slot(key)
	if slot.done != nil {
		switch {
		case slot.full:
		case only == nil:
			slot.pendingFull = true
		default:
			if slot.pending == nil {
				slot.pending = make(map[string]struct{}, len(only))
			}
			maps.Copy(slot.pending, only)
		}
		return slot.done, false
	}
	slot.done = make(chan struct{})
	slot.full = only == nil
	return slot.done, true
}

// finish stores a completed scan for key and returns the pass its caller must run
// next: run false released the slot and ends the chain, and a nil scope with run
// true is a FULL pass. Owned by claim's caller — including for a scan that produced
// nothing, or the slot stays claimed and the variant never refreshes again.
//
// A full pass REPLACES the snapshot and moves `at`; a scoped one merges and leaves
// `at` alone. Either way the old channel's waiters are woken, because the snapshot
// they were waiting on moved. A recorded full intent wins over a recorded scope,
// which it subsumes.
func (c *statusCache) finish(key string, rows []allRepoStatus) (next map[string]struct{}, run bool) {
	c.mu.Lock()
	slot := c.slot(key)
	if slot.full {
		slot.snap = &statusSnapshot{at: time.Now(), repos: rows}
		// A full scan looked at every repository, so anything a read asked for while
		// it ran is already answered.
		slot.pending, slot.pendingFull = nil, false
	} else {
		slot.mergeRows(rows)
	}
	switch {
	case slot.pendingFull:
		slot.full, run = true, true
	case len(slot.pending) > 0:
		slot.full, next, run = false, slot.pending, true
	default:
		slot.full = false
	}
	slot.pending, slot.pendingFull = nil, false
	done := slot.done
	if run {
		// Still claimed, under a channel the next pass will close. Swapping it
		// under the lock is what stops a read arriving between the two passes
		// starting a second concurrent scan.
		slot.done = make(chan struct{})
	} else {
		slot.done = nil
	}
	c.mu.Unlock()
	if done != nil {
		close(done)
	}
	return next, run
}

// mergeRows folds a PARTIAL scan into the slot's snapshot: rows replace their
// namesakes, an unknown repo is appended, every other row survives.
//
// `at` does NOT move. It answers when the WHOLE tree was last known, which is the
// question age_ms and stale() ask, so freshening it on a scoped merge would claim
// every other repository had been rescanned AND would suppress the full refresh for
// as long as edits kept arriving. Callers hold c.mu.
func (s *statusSlot) mergeRows(rows []allRepoStatus) {
	if s.snap == nil {
		// No snapshot to merge into: this is the case statusScope refuses to
		// produce, and publishing a partial scan as a whole one is the harm it
		// refuses it for.
		s.snap = &statusSnapshot{at: time.Now()}
	}
	merged := slices.Clone(s.snap.repos)
	for _, row := range rows {
		if i := slices.IndexFunc(merged, func(have allRepoStatus) bool {
			return have.Repo == row.Repo
		}); i >= 0 {
			merged[i] = row
			continue
		}
		merged = append(merged, row)
	}
	s.snap = &statusSnapshot{at: s.snap.at, repos: merged}
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
