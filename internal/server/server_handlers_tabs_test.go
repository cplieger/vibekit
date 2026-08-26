package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// newTabsServer is a server wired to a real tab store over a temp dir. A real
// store rather than a double, because the property this endpoint exists to hold —
// the set and the version come from ONE critical section — is the store's own.
func newTabsServer(t *testing.T) (*Server, *tabs.Store) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	return &Server{tabs: st}, st
}

// getTabs drives the handler and decodes its body.
func getTabs(t *testing.T, s *Server) (vibekit.TabList, int) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleTabs(rec, httptest.NewRequest(http.MethodGet, "/api/tabs", http.NoBody))
	var out vibekit.TabList
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
	}
	return out, rec.Code
}

func TestTabs_GetReturnsTheSetAndItsVersion(t *testing.T) {
	s, st := newTabsServer(t)
	first, _, _, err := st.Open(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-a"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, _, _, err := st.Open(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindSettings}); err != nil {
		t.Fatalf("open: %v", err)
	}

	got, code := getTabs(t, s)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(got.Tabs) != 2 {
		t.Fatalf("tabs = %+v, want 2", got.Tabs)
	}
	if got.Tabs[0].ID != first.ID {
		t.Errorf("first tab = %q, want the one opened first %q: the slice IS the order", got.Tabs[0].ID, first.ID)
	}
	if got.Version != 2 {
		t.Errorf("version = %d, want 2: two mutations of an empty collection", got.Version)
	}
}

// TestTabs_AnEmptySetIsAnArray. `[]` rather than `null`, because the field is not
// optional and a client decoding null where it expects an array fails on the boot
// path.
func TestTabs_AnEmptySetIsAnArray(t *testing.T) {
	s, _ := newTabsServer(t)
	rec := httptest.NewRecorder()

	s.handleTabs(rec, httptest.NewRequest(http.MethodGet, "/api/tabs", http.NoBody))

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if string(raw["tabs"]) != "[]" {
		t.Errorf("tabs = %s, want []", raw["tabs"])
	}
	if string(raw["version"]) != "0" {
		t.Errorf("version = %s, want 0", raw["version"])
	}
}

// TestTabs_AnUnwiredStoreAnswersTheEmptyCollection, for the reason the ui-state
// handler answers an empty document: a client that cannot read the arrangement
// must still boot.
func TestTabs_AnUnwiredStoreAnswersTheEmptyCollection(t *testing.T) {
	s := &Server{}

	got, code := getTabs(t, s)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 rather than 404", code)
	}
	if len(got.Tabs) != 0 || got.Version != 0 {
		t.Errorf("body = %+v, want the empty collection at version 0", got)
	}
}

func TestTabs_RejectsEveryMethodButGET(t *testing.T) {
	s, _ := newTabsServer(t)
	// Every mutation rides POST /api/command (invariant 1), so this endpoint has no
	// write verb to add and each one is a 405 rather than a 404.
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()

			s.handleTabs(rec, httptest.NewRequest(method, "/api/tabs", http.NoBody))

			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", rec.Code)
			}
		})
	}
}

// movingTabs is a tab set that ADVANCES on every List: call n returns n tabs at
// version n.
//
// It exists to make one property deterministic that the -race case below can only
// sample: a handler that reads the set and the version in two separate calls
// composes a set from one moment with a version from another. Against this double
// that is a guaranteed mismatch (1 tab stamped version 2), where against the real
// store it is a nanosecond window nobody can schedule.
type movingTabs struct {
	calls uint64
}

func (m *movingTabs) List() ([]vibekit.TabSubject, uint64) {
	m.calls++
	out := make([]vibekit.TabSubject, 0, m.calls)
	for i := range m.calls {
		out = append(out, vibekit.TabSubject{ID: "t" + strconv.FormatUint(i, 10), Kind: vibekit.TabKindSettings})
	}
	return out, m.calls
}

// TestTabs_TheSetAndTheVersionComeFromONECall is the paired-read contract, held to
// deterministically.
//
// The number the handler must report is the one this store answered with, and
// against a store that moves on every call there is exactly one way to get it:
// call once. Two calls compose 1 tab with version 2, which is precisely the shape
// that lets a client discard the event its snapshot was missing.
func TestTabs_TheSetAndTheVersionComeFromONECall(t *testing.T) {
	moving := &movingTabs{}
	s := &Server{tabs: moving}

	got, code := getTabs(t, s)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if moving.calls != 1 {
		t.Errorf("the handler called List %d times, want exactly 1: the set and the version are ONE fact", moving.calls)
	}
	if uint64(len(got.Tabs)) != got.Version {
		t.Errorf("answered %d tabs at version %d; this store answers n tabs at version n, so a mismatch "+
			"means the two came from different calls", len(got.Tabs), got.Version)
	}
}

// TestTabs_AReadRacingAMutationPairsTheVersionWithItsOwnSet is the same contract
//
// TestTabs_AReadRacingAMutationPairsTheVersionWithItsOwnSet is the same contract
// against the REAL store and a real concurrent writer, which is what the double
// above cannot cover: that tabs.Store.List is itself atomic.
//
// The fixture makes the invariant exact: each mutation opens exactly one tab, so
// version N describes a set of N tabs, always. Sampling rather than proving —
// TestTabs_TheSetAndTheVersionComeFromONECall is the deterministic half.
func TestTabs_AReadRacingAMutationPairsTheVersionWithItsOwnSet(t *testing.T) {
	s, st := newTabsServer(t)
	const opens = 60

	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range opens {
			if _, _, _, err := st.Open(t.Context(), vibekit.OpenTab{
				Kind: vibekit.TabKindEditor,
				Ref:  "/workspace/f" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".go",
			}); err != nil {
				return // the store's own limit; the reads are what this asserts
			}
		}
	})

	for range 400 {
		got, code := getTabs(t, s)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if uint64(len(got.Tabs)) != got.Version {
			t.Fatalf("read %d tabs at version %d; each mutation opens exactly one tab, so a set and a "+
				"version that disagree means they were captured separately", len(got.Tabs), got.Version)
		}
	}
	wg.Wait()

	// And once more after the writer is done, so the case cannot pass by only ever
	// having observed the empty collection.
	got, _ := getTabs(t, s)
	if got.Version == 0 {
		t.Fatal("the writer produced no mutation, so nothing was actually raced")
	}
}
