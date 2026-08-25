package uistate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStore_RoundTripsAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Get(); got.Revision != 0 || len(got.TabOrder) != 0 {
		t.Fatalf("fresh store = %+v, want revision 0 and no tabs", got)
	}

	want := State{
		TabOrder:    []string{"__git__", "chat-a", "editor:/workspace/x.go"},
		PinnedTabs:  []string{"chat-a"},
		EditorFiles: []string{"/workspace/x.go"},
		FBPath:      "/workspace",
		Theme:       "system",
		TurnFolds:   map[string]map[string]bool{"chat-a": {"m-1": false}},
	}
	doc, err := s.Put(t.Context(), &want, 0)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Revision != 1 {
		t.Errorf("Revision after first write = %d, want 1", doc.Revision)
	}

	// The whole point of the store: another process reads the same arrangement.
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Get()
	if got.Revision != 1 {
		t.Errorf("Revision after reopen = %d, want 1", got.Revision)
	}
	if strings.Join(got.TabOrder, ",") != strings.Join(want.TabOrder, ",") {
		t.Errorf("TabOrder = %v, want %v", got.TabOrder, want.TabOrder)
	}
	if got.Theme != "system" || got.FBPath != "/workspace" {
		t.Errorf("scalars = %+v", got.State)
	}
	if got.TurnFolds["chat-a"]["m-1"] != false {
		t.Errorf("TurnFolds = %+v", got.TurnFolds)
	}
}

// The revision is the whole concurrency story, so a stale writer must be refused
// rather than silently winning: a phone that loaded an hour ago must not erase
// every tab opened since.
func TestStore_StaleWriteIsRefusedAndReturnsCurrent(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(t.Context(), &State{TabOrder: []string{"a"}}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(t.Context(), &State{TabOrder: []string{"a", "b"}}, 1); err != nil {
		t.Fatal(err)
	}

	// A device still holding revision 1 tries to write.
	doc, err := s.Put(t.Context(), &State{TabOrder: []string{"stale"}}, 1)
	if !errors.Is(err, ErrStale) {
		t.Fatalf("stale Put err = %v, want ErrStale", err)
	}
	// It is handed the CURRENT document so it can re-apply without a second
	// round trip.
	if doc.Revision != 2 || strings.Join(doc.TabOrder, ",") != "a,b" {
		t.Errorf("stale Put returned %+v, want revision 2 and a,b", doc)
	}
	if live := s.Get(); strings.Join(live.TabOrder, ",") != "a,b" {
		t.Errorf("store was mutated by a refused write: %v", live.TabOrder)
	}
}

// Get hands out a deep copy. TabOrder is exactly the field a client reorders in
// place, so a shallow copy would let a caller rearrange the store's own state.
func TestStore_GetIsADeepCopy(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(t.Context(), &State{
		TabOrder:  []string{"a", "b"},
		TurnFolds: map[string]map[string]bool{"c": {"t": true}},
	}, 0); err != nil {
		t.Fatal(err)
	}

	got := s.Get()
	got.TabOrder[0] = "hijacked"
	got.TurnFolds["c"]["t"] = false
	got.TurnFolds["new"] = map[string]bool{"x": true}

	live := s.Get()
	if live.TabOrder[0] != "a" {
		t.Errorf("TabOrder[0] = %q, want a (the caller mutated the store)", live.TabOrder[0])
	}
	if live.TurnFolds["c"]["t"] != true {
		t.Error("TurnFolds value was mutated through the returned copy")
	}
	if _, added := live.TurnFolds["new"]; added {
		t.Error("a key added to the returned copy reached the store")
	}
}

// A malformed file starts EMPTY and reports why, rather than failing the app.
// Invariant 6: a broken state must be able to heal itself, and refusing to start
// over a tab arrangement takes the whole app down for cosmetic state.
func TestNewStore_MalformedFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(filepath.Join(dir, FileName), "{not json"); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(dir)
	if err == nil {
		t.Error("NewStore err = nil, want the parse failure reported")
	}
	if s == nil {
		t.Fatal("NewStore returned a nil store; the app must still boot")
	}
	if got := s.Get(); got.Revision != 0 || len(got.TabOrder) != 0 {
		t.Errorf("store after malformed file = %+v, want empty", got)
	}
	// And it is writable, so the user can heal it by using the app.
	if _, err := s.Put(t.Context(), &State{TabOrder: []string{"a"}}, 0); err != nil {
		t.Errorf("Put after malformed load: %v", err)
	}
}

func TestSanitize(t *testing.T) {
	long := strings.Repeat("x", MaxStringLen+1)
	cases := map[string]struct {
		in    State
		check func(*testing.T, State)
	}{
		"drops empty and oversized ids": {
			in: State{TabOrder: []string{"a", "", long, "b"}},
			check: func(t *testing.T, s State) {
				if strings.Join(s.TabOrder, ",") != "a,b" {
					t.Errorf("TabOrder = %v", s.TabOrder)
				}
			},
		},
		"dedups while preserving display order": {
			in: State{TabOrder: []string{"b", "a", "b"}},
			check: func(t *testing.T, s State) {
				if strings.Join(s.TabOrder, ",") != "b,a" {
					t.Errorf("TabOrder = %v, want b,a", s.TabOrder)
				}
			},
		},
		"caps the tab list": {
			in: State{TabOrder: manyIDs(MaxTabs + 50)},
			check: func(t *testing.T, s State) {
				if len(s.TabOrder) != MaxTabs {
					t.Errorf("len(TabOrder) = %d, want %d", len(s.TabOrder), MaxTabs)
				}
			},
		},
		"drops a pin for a tab that is not open": {
			in: State{TabOrder: []string{"a"}, PinnedTabs: []string{"a", "ghost"}},
			check: func(t *testing.T, s State) {
				if strings.Join(s.PinnedTabs, ",") != "a" {
					t.Errorf("PinnedTabs = %v, want a", s.PinnedTabs)
				}
			},
		},
		"keeps system as a real theme choice": {
			in: State{Theme: "system"},
			check: func(t *testing.T, s State) {
				if s.Theme != "system" {
					t.Errorf("Theme = %q, want system", s.Theme)
				}
			},
		},
		"drops an unknown theme": {
			in: State{Theme: "solarized"},
			check: func(t *testing.T, s State) {
				if s.Theme != "" {
					t.Errorf("Theme = %q, want empty", s.Theme)
				}
			},
		},
		"drops a fold entry with no turns": {
			in: State{TurnFolds: map[string]map[string]bool{"a": {}, "b": {"t": true}}},
			check: func(t *testing.T, s State) {
				if _, empty := s.TurnFolds["a"]; empty {
					t.Error("kept a chat with no fold entries")
				}
				if s.TurnFolds["b"]["t"] != true {
					t.Errorf("TurnFolds = %+v", s.TurnFolds)
				}
			},
		},
		"empty collections normalize to nil, so the JSON stays small": {
			in: State{TabOrder: []string{}, TurnFolds: map[string]map[string]bool{}},
			check: func(t *testing.T, s State) {
				if s.TabOrder != nil || s.TurnFolds != nil {
					t.Errorf("state = %+v, want nil collections", s)
				}
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tc.check(t, Sanitize(&tc.in))
		})
	}
}

func manyIDs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "tab-" + string(rune('a'+i%26)) + strings.Repeat("z", i%7) + itoa(i)
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}
