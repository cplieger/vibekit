// Pure state machine derived from the event log. No I/O — operates
// entirely on event structs and returns computed values. Independently
// testable with dense unit tests; Manager synchronizes access via m.mu.

package checkpoint

import (
	"log/slog"
	"slices"
	"strconv"

	chktypes "vibekit/internal/checkpoint/types"
)

// state is the reconstructed view of a chat's checkpoint history at
// a point in time. Derived by replaying events; never persisted.
// After the initial replay, Manager calls apply() to fold each new
// event in without touching disk — the old "invalidate on every
// write + re-replay from disk" pattern was O(N) per write.
//
// Not safe for concurrent use. Every caller of apply / tagList /
// oldestTag / contentAtTag / filesTouchedBetween / referencesBlob
// must synchronize externally; Manager does that via m.mu.
type state struct {
	tags           map[string]int
	tagFiles       map[string][]string
	fileHistory    map[string][]fileObservation
	blobRefs       map[string]struct{}
	pendingRestore string
	latestTag      string
	orderedTags    []string
	conflicts      conflictRing
	turn           int
	toolsInTurn    int
}

// maxInMemoryConflicts caps the number of ConflictPayload entries
// Manager.Conflicts will hold in memory per chat. An adversarial /
// buggy setup with two agents in a drift loop can otherwise grow
// the slice without bound (one entry per Snapshot on a drifted
// file). 500 is luxurious for any realistic workflow; over the
// cap we drop oldest-first.
const maxInMemoryConflicts = 500

// fileObservation is one row in fileHistory[path]. Each Snapshot
// event produces one observation. A missing `beforeSHA` means the
// file didn't exist at snapshot time (the write that followed
// created it).
type fileObservation struct {
	tag       string
	beforeSHA string
	afterSHA  string
}

// newState returns a zero-valued state ready for apply().
func newState() *state {
	return &state{
		tags:        map[string]int{},
		tagFiles:    map[string][]string{},
		fileHistory: map[string][]fileObservation{},
		blobRefs:    map[string]struct{}{},
	}
}

// apply folds one event into the derived state. Used by both
// replay() (bulk replay during Open) and by Manager.Append after
// each incremental write. Pure dispatcher — each kind has its own
// helper so the invariants for one event kind aren't entangled with
// another.
func (s *state) apply(e *event) {
	switch e.Kind {
	case kindTurnStart:
		s.turn = e.Turn
		s.toolsInTurn = 0
	case kindSnapshot:
		s.applySnapshot(e)
	case kindRestore, kindRestoreCommitted:
		// restore (legacy single-event) and restore_committed
		// both mean "restore landed cleanly". Clear the pending
		// watermark if any, and track the latest tag so the
		// next snapshot in the current turn gets K+1.
		s.pendingRestore = ""
		s.latestTag = e.Tag
	case kindRestoreStarted:
		// Phase 2 about to run. If we ever see a replay end at
		// this event (no matching committed), recovery fires.
		s.pendingRestore = e.Tag
	case kindConflict:
		s.applyConflict(e)
	default:
		slog.Error("checkpoint: unknown event kind during replay",
			"kind", string(e.Kind))
	}
}

// applySnapshot records a file snapshot: metadata bookkeeping,
// fileHistory entry, blob-ref set, tag allocation tracker, ordered-
// tag insert.
func (s *state) applySnapshot(e *event) {
	s.tags[e.Tag] = e.MessageCount
	s.tagFiles[e.Tag] = append(s.tagFiles[e.Tag], e.Path)
	s.fileHistory[e.Path] = append(s.fileHistory[e.Path], fileObservation{
		tag:       e.Tag,
		beforeSHA: e.BeforeSHA,
		afterSHA:  e.AfterSHA,
	})
	if e.BeforeSHA != "" {
		s.blobRefs[e.BeforeSHA] = struct{}{}
	}
	if e.AfterSHA != "" {
		s.blobRefs[e.AfterSHA] = struct{}{}
	}
	s.latestTag = e.Tag
	if e.Turn > s.turn {
		s.turn = e.Turn
	}
	if e.Turn == s.turn && e.Tool >= s.toolsInTurn {
		s.toolsInTurn = e.Tool + 1
	}
	s.insertOrderedTag(e.Tag)
}

// conflictRing is a fixed-size ring buffer for ConflictPayload.
// Append is O(1) unconditionally; oldest entries are silently
// overwritten when the buffer is full.
type conflictRing struct {
	buf  [maxInMemoryConflicts]ConflictPayload
	head int  // index of oldest entry
	size int  // number of valid entries (0..maxInMemoryConflicts)
	full bool // true once we've wrapped at least once
}

// append adds a payload to the ring. O(1). Takes a pointer to avoid
// copying ConflictPayload (88 bytes) by value at every call site.
func (r *conflictRing) append(p *ConflictPayload) {
	idx := (r.head + r.size) % maxInMemoryConflicts
	if r.size < maxInMemoryConflicts {
		r.buf[idx] = *p
		r.size++
	} else {
		// Overwrite oldest, advance head.
		r.buf[r.head] = *p
		r.head = (r.head + 1) % maxInMemoryConflicts
		if !r.full {
			r.full = true
		}
	}
}

// slice returns a copy of all entries in chronological order.
func (r *conflictRing) slice() []ConflictPayload {
	if r.size == 0 {
		return nil
	}
	out := make([]ConflictPayload, r.size)
	for i := range r.size {
		out[i] = r.buf[(r.head+i)%maxInMemoryConflicts]
	}
	return out
}

// applyConflict records a cross-chat conflict event in-state so
// Manager.Conflicts can return the list without re-reading the log
// on every HTTP hit. Uses a ring buffer so append is O(1)
// unconditionally — the old copy-shift was O(N) per overflow.
func (s *state) applyConflict(e *event) {
	payload := ConflictPayload{
		Path:        e.Path,
		OtherChat:   e.OtherChat,
		ExpectedSHA: e.ExpectedSHA,
		ActualSHA:   e.BeforeSHA,
		Tag:         e.Tag,
		TS:          e.TS,
	}
	if s.conflicts.size == maxInMemoryConflicts && !s.conflicts.full {
		slog.Warn("checkpoint: conflict ring full, overwriting oldest",
			"cap", maxInMemoryConflicts,
			"path", e.Path)
	}
	s.conflicts.append(&payload)
}

// insertOrderedTag keeps s.orderedTags sorted under (turn, tool)
// ascending when a new tag is appended. Uses binary search so the
// insert is O(log N) index-find + O(N) slice insert; for the tag
// counts we care about (up to maybe 10k entries on a pathological
// multi-day chat) that's microseconds.
func (s *state) insertOrderedTag(tag string) {
	idx, exists := findSorted(s.orderedTags, tag)
	if exists {
		// Duplicates are impossible by construction (allocateTag
		// increments toolsInTurn on every call) but we protect
		// against a corrupted log replaying the same tag twice.
		return
	}
	s.orderedTags = slices.Insert(s.orderedTags, idx, tag)
}

// replay rebuilds state from an event slice. The input is assumed to
// be in append order (as written), which is what eventLog.Read
// produces.
func replay(events []event) *state {
	s := newState()
	for i := range events {
		s.apply(&events[i])
	}
	return s
}

// allocateTag returns the tag the next snapshot should use, given
// the current state. Turn 0 never produces an unsuffixed tag — that
// slot is reserved for the initial-state marker. First snapshot in
// turn N is "N"; subsequent are "N.1", "N.2", ...
func (s *state) allocateTag() string {
	if s.turn == 0 {
		return "0." + strconv.Itoa(s.toolsInTurn)
	}
	if s.toolsInTurn == 0 {
		return strconv.Itoa(s.turn)
	}
	return strconv.Itoa(s.turn) + "." + strconv.Itoa(s.toolsInTurn)
}

// ErrTagNotFound signals that a requested tag isn't in the event log.
// Separate from a generic error so the HTTP layer can map it to 404.
var ErrTagNotFound = chktypes.ErrTagNotFound
