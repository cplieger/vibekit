package tabs

import (
	"context"
	"fmt"
	"slices"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// state is the pair a mutation works on: the ordered set and the version it
// describes. They travel together because they are ONE fact — a caller that got
// one without the other could pair a stale set with a fresh version, which is the
// defect an SSE head watermark had and this collection version does not.
type state struct {
	tabs    []vibekit.TabSubject
	version uint64
}

// mutate is the ONE write path, and it exists so the lock ordering is written
// once rather than repeated in five methods that could each get it wrong. The
// sequence is the package doc's, verbatim.
//
// apply reports whether it CHANGED anything. False means no version bump, no
// write and no publish, which is what makes an idempotent Open, a repeated pin
// and a second Close of the same id cost nothing and emit nothing — the version
// increments only on a real state change.
//
// On error nothing is applied and the returned version is 0, which is not a
// version any state ever carries. A caller must not read it as "the state is at
// 0"; the error is the whole answer.
//
// A persist failure needs no rollback, and that is a property of the ORDER rather
// than care taken here: the clone is what was mutated, so a failed write leaves
// the published state and the version exactly where they were. internal/uistate
// has to decrement its revision by hand for the same case because it mutates
// before it writes.
func (s *Store) mutate(ctx context.Context, apply func(st *state) (bool, error)) (uint64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	next := s.snapshot()
	changed, err := apply(&next)
	if err != nil {
		return 0, err
	}
	if !changed {
		return next.version, nil
	}
	next.version++
	if err := s.persist(ctx, &next); err != nil {
		return 0, err
	}
	s.publish(&next)
	return next.version, nil
}

// snapshot clones the state under stateMu. The clone is SHALLOW because
// vibekit.TabSubject holds no reference type; a field of slice or map type would
// make this a deep clone, and the same sentence is on List.
func (s *Store) snapshot() state {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return state{tabs: slices.Clone(s.tabs), version: s.version}
}

// publish installs a mutated clone. Called only after the clone is durable, so
// what a reader sees is always what is on disk.
func (s *Store) publish(st *state) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.tabs, s.version = st.tabs, st.version
}

// Open adds a tab for spec, or returns the one already open for its
// (Kind, Ref).
//
// created is false for the already-open case, which mutates nothing, bumps no
// version and therefore emits no event. That flag closes a real hole rather than
// reporting a detail: with the event as the only render path, an open that emits
// nothing would leave the caller waiting forever, so the caller resolves from the
// response when created is false.
//
// The id is minted here (see newID) and the position is the client's insertSpec
// rule: a top-level tab at the end, a child immediately after its parent's
// existing children. The REF is not minted here — a chat id belongs to whoever
// created the chat — which is what keeps this from being a create in disguise.
// A spec naming a parent that is not open is PROMOTED to top level, so the
// returned subject's Parent may be empty when spec.Parent was not: the caller
// should use the returned subject rather than its own spec.
//
// Returns ErrBadKind, ErrBadRef or ErrTooMany. On any error nothing is applied
// and the version is 0.
//
// The results are named because three of the four are easy to mix up at a call
// site that only wants one of them.
func (s *Store) Open(ctx context.Context, spec vibekit.OpenTab) (subject vibekit.TabSubject, created bool, version uint64, err error) {
	err = checkSubject(spec.Kind, spec.Ref)
	if err != nil {
		return vibekit.TabSubject{}, false, 0, err
	}
	version, err = s.mutate(ctx, func(st *state) (bool, error) {
		if i := indexOfSubject(st.tabs, spec.Kind, spec.Ref); i >= 0 {
			subject = st.tabs[i]
			return false, nil
		}
		if len(st.tabs) >= MaxOpenTabs {
			return false, fmt.Errorf("%w: %d open, limit %d", ErrTooMany, len(st.tabs), MaxOpenTabs)
		}
		subject = vibekit.TabSubject{
			ID:     newID(),
			Kind:   spec.Kind,
			Ref:    spec.Ref,
			Parent: spec.Parent,
			Owns:   spec.Owns,
		}
		st.tabs = insert(st.tabs, &subject)
		created = true
		return true, nil
	})
	if err != nil {
		return vibekit.TabSubject{}, false, 0, err
	}
	return subject, created, version, nil
}

// Close removes the tab and its descendants as ONE mutation, returning everything
// it removed so the caller can emit one aggregate event naming every id.
//
// An id that is not open yields an empty slice and NO error: closing twice is not
// a failure — two devices can close the same tab — and len(closed) == 0 already
// says nothing happened, which is why there is no second return value for it.
// That case bumps no version and so emits nothing.
//
// One mutation, one version bump, whatever the size of the subtree. Returns no
// sentinel of its own; the only error it can report is a failed write.
func (s *Store) Close(ctx context.Context, id string) ([]vibekit.TabSubject, uint64, error) {
	var closed []vibekit.TabSubject
	version, err := s.mutate(ctx, func(st *state) (bool, error) {
		doomed := closure(st.tabs, id)
		if len(doomed) == 0 {
			return false, nil
		}
		closed = make([]vibekit.TabSubject, 0, len(doomed))
		for _, t := range st.tabs {
			if _, gone := doomed[t.ID]; gone {
				closed = append(closed, t)
			}
		}
		st.tabs = slices.DeleteFunc(st.tabs, func(t vibekit.TabSubject) bool {
			_, gone := doomed[t.ID]
			return gone
		})
		return true, nil
	})
	if err != nil {
		return nil, 0, err
	}
	return closed, version, nil
}

// Reorder replaces the order. ids must name every open tab EXACTLY ONCE — same
// length, every id open, no duplicates — or ErrOrderMismatch is returned and
// nothing is applied. The exact-set check is the whole precondition and it is
// sufficient: an order that names the set the server holds cannot have been
// derived from a set it does not hold.
//
// There is deliberately NO base-version precondition. Making the version a
// precondition would discard a perfectly valid drag whenever any unrelated
// mutation landed first, and a pin elsewhere bumps the version without changing
// the order.
//
// An order identical to the current one changes nothing, so it bumps no version
// and emits nothing: a tab dragged back where it started is not news.
//
// The pinned partition is NOT applied here. Pinned-ahead-of-unpinned is a
// rendering rule the client owns (applyPinOrder), and silently rearranging the
// order a gesture committed would contradict the exact-set contract above.
func (s *Store) Reorder(ctx context.Context, ids []string) (uint64, error) {
	return s.mutate(ctx, func(st *state) (bool, error) {
		if len(ids) != len(st.tabs) {
			return false, fmt.Errorf("%w: %d ids for %d open tabs", ErrOrderMismatch, len(ids), len(st.tabs))
		}
		open := make(map[string]vibekit.TabSubject, len(st.tabs))
		for _, t := range st.tabs {
			open[t.ID] = t
		}
		next := make([]vibekit.TabSubject, 0, len(ids))
		seen := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			if _, dup := seen[id]; dup {
				return false, fmt.Errorf("%w: %q appears twice", ErrOrderMismatch, id)
			}
			t, isOpen := open[id]
			if !isOpen {
				return false, fmt.Errorf("%w: %q is not open", ErrOrderMismatch, id)
			}
			seen[id] = struct{}{}
			next = append(next, t)
		}
		if slices.Equal(st.tabs, next) {
			return false, nil
		}
		st.tabs = next
		return true, nil
	})
}

// SetPinned pins or unpins one tab, and is idempotent in both directions: a tab
// already in that state bumps no version and emits nothing.
//
// An id that is not open is NOT an error, for Close's reason: the only way to
// reach it is a pin racing a close, and the close event is what the client acts
// on. Nothing is applied and the current version is returned.
//
// Returns no sentinel of its own; the only error it can report is a failed write.
func (s *Store) SetPinned(ctx context.Context, id string, pinned bool) (uint64, error) {
	return s.mutate(ctx, func(st *state) (bool, error) {
		i := indexOfID(st.tabs, id)
		if i < 0 || st.tabs[i].Pinned == pinned {
			return false, nil
		}
		st.tabs[i].Pinned = pinned
		return true, nil
	})
}

// List returns the set in order plus the version it reflects, captured in ONE
// critical section so a caller cannot pair a stale set with a fresh version.
//
// The result is a copy. vibekit.TabSubject holds no reference type, so a shallow
// clone suffices; a field of slice or map type — an Owns list, a per-tab
// attribute map — would make this a deep clone, because a shallow one would hand
// a caller the store's own backing array to reorder through.
func (s *Store) List() (tabs []vibekit.TabSubject, version uint64) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return slices.Clone(s.tabs), s.version
}

// Subtree returns the tab with this id plus every descendant, in the set's
// order, or nil when id is not open. It is the SAME walk Close removes —
// closure — exposed as a read, so a caller deciding what a close will take out
// (the membership coordinator's retention escalation) cannot disagree with what
// the close then takes.
//
// A read under stateMu like List, and a copy for List's reason: the subjects
// hold no reference type, so element copies are the whole isolation.
func (s *Store) Subtree(id string) []vibekit.TabSubject {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	doomed := closure(s.tabs, id)
	if len(doomed) == 0 {
		return nil
	}
	out := make([]vibekit.TabSubject, 0, len(doomed))
	for _, t := range s.tabs {
		if _, gone := doomed[t.ID]; gone {
			out = append(out, t)
		}
	}
	return out
}

// Prune is LOAD-TIME CRASH RECOVERY and NOT the live integrity mechanism. It
// drops a tab whose subject no longer resolves and promotes a tab whose parent is
// absent, in one mutation, and returns what it dropped.
//
// The live mechanism is the membership coordinator: it owns every operation that
// spans the chat store and this one, writes the chat record first on create and
// removes it first on delete, and retries a failed tab close rather than waiting
// for a restart. Prune exists for the crash that lands between those two writes.
// Calling it periodically would be treating recovery as a substitute for
// ordering.
//
// exists is asked about each subject in turn: a chat tab whose chat is gone, an
// editor tab whose file is gone. A nil exists resolves everything, which reduces
// this to the orphan promotion.
//
// A tab whose parent was dropped is PROMOTED rather than dropped with it, the
// same answer Open gives an orphan and the client's insertSpec gives one: a tab
// nobody can see is worse than a tab in the wrong place. Deeper descendants keep
// their parents, because those parents are still open.
func (s *Store) Prune(ctx context.Context, exists func(vibekit.TabSubject) bool) ([]vibekit.TabSubject, uint64, error) {
	var dropped []vibekit.TabSubject
	version, err := s.mutate(ctx, func(st *state) (bool, error) {
		kept, gone := partitionByExistence(st.tabs, exists)
		promoted := promoteOrphans(kept)
		if len(gone) == 0 && promoted == 0 {
			return false, nil
		}
		dropped = gone
		st.tabs = kept
		return true, nil
	})
	if err != nil {
		return nil, 0, err
	}
	return dropped, version, nil
}

// partitionByExistence splits tabs into the ones exists still resolves and the
// ones it does not. A nil exists resolves everything.
func partitionByExistence(tabs []vibekit.TabSubject, exists func(vibekit.TabSubject) bool) (kept, gone []vibekit.TabSubject) {
	if exists == nil {
		return tabs, nil
	}
	kept = make([]vibekit.TabSubject, 0, len(tabs))
	for _, t := range tabs {
		if !exists(t) {
			gone = append(gone, t)
			continue
		}
		kept = append(kept, t)
	}
	return kept, gone
}

// promoteOrphans clears the Parent of every tab whose parent is not in the set,
// and reports how many it promoted so a caller can tell a no-op prune from a real
// one.
//
// One pass, deliberately not transitive: a tab whose own parent survives keeps it,
// even when the GRANDparent went, because that parent is still a tab on the strip.
func promoteOrphans(tabs []vibekit.TabSubject) int {
	open := make(map[string]struct{}, len(tabs))
	for _, t := range tabs {
		open[t.ID] = struct{}{}
	}
	promoted := 0
	for i := range tabs {
		if tabs[i].Parent == "" {
			continue
		}
		if _, ok := open[tabs[i].Parent]; !ok {
			tabs[i].Parent = ""
			promoted++
		}
	}
	return promoted
}

// insert places sub at its canonical position, and promotes it to top level when
// its parent is not open.
//
// The rule is the client's insertSpec, character for character: a top-level tab
// at the end, a child after the CONTIGUOUS run of its parent's existing children.
// Both halves matter. Scanning only the contiguous run means a Reorder that moved
// a child away from its parent is honoured rather than second-guessed — the
// exact-set check permits any permutation, so parent-adjacency is not an
// invariant after a drag. And the promotion is what keeps the promise
// TabSubject.Parent makes: every stored Parent named an open tab at the moment it
// was set.
//
// sub is a pointer because the promotion is a decision about the SUBJECT, and the
// caller returns that subject to a client that will address the tab by it.
func insert(tabs []vibekit.TabSubject, sub *vibekit.TabSubject) []vibekit.TabSubject {
	at := -1
	if sub.Parent != "" {
		at = indexOfID(tabs, sub.Parent)
	}
	if at < 0 {
		sub.Parent = ""
		return append(tabs, *sub)
	}
	at++
	for at < len(tabs) && tabs[at].Parent == sub.Parent {
		at++
	}
	return slices.Insert(tabs, at, *sub)
}

// closure returns id plus every tab that descends from it, as a set, or nil when
// id is not open.
//
// The walk is ITERATIVE and marks as it goes. Iterative because depth is bounded
// only by the number of tabs, and marking because a hand-edited file can contain
// a parent cycle that Open cannot produce — a recursive walk would exhaust the
// stack on one, where this visits each tab at most once.
//
// The child index is built here and thrown away, which is the point: a persistent
// index would be a second representation of the parent pointers, and the two can
// desync.
func closure(tabs []vibekit.TabSubject, id string) map[string]struct{} {
	if indexOfID(tabs, id) < 0 {
		return nil
	}
	children := make(map[string][]string, len(tabs))
	for _, t := range tabs {
		if t.Parent != "" {
			children[t.Parent] = append(children[t.Parent], t.ID)
		}
	}
	out := map[string]struct{}{id: {}}
	pending := []string{id}
	for len(pending) > 0 {
		cur := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for _, kid := range children[cur] {
			if _, seen := out[kid]; seen {
				continue
			}
			out[kid] = struct{}{}
			pending = append(pending, kid)
		}
	}
	return out
}

// indexOfID returns the position of the tab with this id, or -1.
func indexOfID(tabs []vibekit.TabSubject, id string) int {
	return slices.IndexFunc(tabs, func(t vibekit.TabSubject) bool { return t.ID == id })
}

// indexOfSubject returns the position of the tab open for this (Kind, Ref), or
// -1. This is the uniqueness rule that makes Open idempotent, and it is a scan
// rather than a map because Open builds no other index and MaxOpenTabs is 48.
func indexOfSubject(tabs []vibekit.TabSubject, kind vibekit.TabKind, ref string) int {
	return slices.IndexFunc(tabs, func(t vibekit.TabSubject) bool {
		return t.Kind == kind && t.Ref == ref
	})
}
