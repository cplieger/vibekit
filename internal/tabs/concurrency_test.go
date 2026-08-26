package tabs

import (
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestOpen_ConcurrentOpensSurviveInMemoryAndOnDisk IS THE TEST THE LOCK ORDERING
// EXISTS FOR, and it is the one to run before believing any change to mutate.
//
// The defect it detects: with the write lock taken AFTER the clone — "clone under
// stateMu, release it, then mutate and persist under writeMu", which is what an
// earlier revision of this design specified — two opens clone the same state S0,
// the first persists S0+A, the second persists its stale S0+B, and A is gone from
// memory AND from disk after having returned success. Both would also report the
// same next version, so a client's gap check could not even detect it.
//
// Three assertions, and each catches the defect on its own: every returned subject
// is still in the published set, every one is in the FILE, and the version equals
// the number of opens (two mutations sharing a version is the signature).
//
// It is a probabilistic detector, so it is deliberately generous: eight opens race
// across a real fsync, over four rounds on four fresh stores. RED-CHECKED by
// reverting mutate to the broken order, where it fails on the first round every
// time.
func TestOpen_ConcurrentOpensSurviveInMemoryAndOnDisk(t *testing.T) {
	const opens = 8
	for round := range 4 {
		s, dir := newTestStore(t)
		got := make([]vibekit.TabSubject, opens)
		errs := make([]error, opens)

		var wg sync.WaitGroup
		for i := range opens {
			wg.Go(func() {
				sub, _, _, err := s.Open(t.Context(), chatSpec("c-"+strconv.Itoa(i)))
				got[i], errs[i] = sub, err
			})
		}
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d: Open(chat c-%d): %v", round, i, err)
			}
		}
		tabs, version := s.List()
		inMemory := idsOf(tabs)
		onFile := idsOf(onDisk(t, dir).Tabs)
		for i, sub := range got {
			if !slices.Contains(inMemory, sub.ID) {
				t.Errorf("round %d: Open(chat c-%d) returned success for %q, which is not in List(): a committed write was lost in memory",
					round, i, sub.ID)
			}
			if !slices.Contains(onFile, sub.ID) {
				t.Errorf("round %d: Open(chat c-%d) returned success for %q, which is not in tabs.json: a committed write was lost on disk",
					round, i, sub.ID)
			}
		}
		if version != opens {
			t.Errorf("round %d: %d concurrent opens left version %d, want %d: every commit takes its own version",
				round, opens, version, opens)
		}
		if len(tabs) != opens {
			t.Errorf("round %d: List() = %d tabs, want %d", round, len(tabs), opens)
		}
	}
}

// TestOpen_AgainstReorderLosesNothing races the two mutations that disagree about
// the whole slice: one inserts, the other replaces. A Reorder derived from a list
// the opener has since grown is REFUSED (its set is short by one), and that is a
// correct outcome rather than a flake — what must never happen is a tab
// disappearing because a reorder was applied to a set that no longer existed.
func TestOpen_AgainstReorderLosesNothing(t *testing.T) {
	const opens = 12
	s, dir := newTestStore(t)
	opened := make([]vibekit.TabSubject, opens)

	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range opens {
			sub, _, _, err := s.Open(t.Context(), chatSpec("c-"+strconv.Itoa(i)))
			if err != nil {
				t.Errorf("Open(chat c-%d): %v", i, err)
				return
			}
			opened[i] = sub
		}
	})
	wg.Go(func() {
		for range opens * 3 {
			tabs, _ := s.List()
			if len(tabs) < 2 {
				continue
			}
			ids := idsOf(tabs)
			slices.Reverse(ids)
			if _, err := s.Reorder(t.Context(), ids); err != nil && !errors.Is(err, ErrOrderMismatch) {
				t.Errorf("Reorder(%d ids) = %v, want either success or ErrOrderMismatch", len(ids), err)
			}
		}
	})
	wg.Wait()

	tabs, version := s.List()
	if len(tabs) != opens {
		t.Errorf("List() = %d tabs after %d opens against a reordering reader, want %d", len(tabs), opens, opens)
	}
	inMemory := idsOf(tabs)
	onFile := idsOf(onDisk(t, dir).Tabs)
	for i, sub := range opened {
		if sub.ID == "" {
			continue // its Open already reported a failure above
		}
		if !slices.Contains(inMemory, sub.ID) {
			t.Errorf("chat c-%d (%q) is not in List(): a reorder was applied to a set that no longer existed", i, sub.ID)
		}
		if !slices.Contains(onFile, sub.ID) {
			t.Errorf("chat c-%d (%q) is not in tabs.json", i, sub.ID)
		}
	}
	if len(onFile) != len(inMemory) {
		t.Errorf("tabs.json holds %d tabs and memory holds %d; the last publish and the last write disagree", len(onFile), len(inMemory))
	}
	if version < opens {
		t.Errorf("version = %d after %d opens, want at least %d", version, opens, opens)
	}
}

// TestList_PairsTheSetWithItsOwnVersion is the property a second critical section
// cannot hold. One mutator opens one tab per mutation from an empty store, so
// version N means exactly N tabs, forever — and any List that read the set and the
// version in two sections could return a stale set with a fresh version (the
// snapshot-versus-watermark defect, in miniature) or the reverse.
//
// The readers use t.Errorf and return, never t.Fatal: FailNow off the test's own
// goroutine ends the WRONG goroutine and can hang the test.
func TestList_PairsTheSetWithItsOwnVersion(t *testing.T) {
	const (
		opens   = 40
		readers = 4
	)
	s, _ := newTestStore(t)
	done := make(chan struct{})

	var wg sync.WaitGroup
	wg.Go(func() {
		defer close(done)
		for i := range opens {
			if _, _, _, err := s.Open(t.Context(), chatSpec("c-"+strconv.Itoa(i))); err != nil {
				t.Errorf("Open(chat c-%d): %v", i, err)
				return
			}
		}
	})
	for range readers {
		wg.Go(func() {
			reads := 0
			for {
				select {
				case <-done:
					if reads == 0 {
						t.Error("a reader observed nothing; the race window closed before it started")
					}
					return
				default:
				}
				tabs, version := s.List()
				reads++
				if uint64(len(tabs)) != version {
					t.Errorf("List() = %d tabs at version %d, want them to agree: this store's Nth mutation is its Nth tab, so a mismatch is a set paired with a version it does not describe",
						len(tabs), version)
					return
				}
			}
		})
	}
	wg.Wait()

	if tabs, version := s.List(); len(tabs) != opens || version != opens {
		t.Errorf("List() = %d tabs at version %d, want %d and %d", len(tabs), version, opens, opens)
	}
}
