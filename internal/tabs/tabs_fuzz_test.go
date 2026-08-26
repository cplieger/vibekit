package tabs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// FuzzNewStore is the untrusted-input boundary of this package: tabs.json is
// written by a previous build and editable by anyone with a shell in the
// container, and it is read on the BOOT path, so a panic here is a container that
// does not come up.
//
// The invariants are the store's own claims rather than crash-only, because
// crash-only would pass for a load path that happily published a subject with an
// unknown kind — which reaches every connected client as a switch with no case for
// it. So every state NewStore publishes is checked against the rules Open enforces
// at the door: bounded, unique by id, unique by subject, every kind one of the
// eight, every ref fitting its kind. The store must also still WORK, since warn
// and start empty is the whole posture.
//
// It stands alone: no sibling test has to have run first, which is what the weekly
// fuzz run's `-run='^$'` invocation requires.
func FuzzNewStore(f *testing.F) {
	f.Add([]byte(`{"tabs":[{"id":"a1","kind":"chat","ref":"c-1"}],"version":3}`))
	f.Add([]byte(`{"tabs":[],"version":0}`))
	f.Add([]byte(`{"tabs":null,"version":18446744073709551615}`))
	f.Add([]byte(`{"tabs":[{"id":"a","kind":"plan","ref":"x"}],"version":1}`))
	f.Add([]byte(`{"tabs":[{"id":"a","kind":"settings","ref":"nope"}],"version":1}`))
	f.Add([]byte(`{"tabs":[{"id":"a","kind":"chat","ref":"c-1"},{"id":"a","kind":"chat","ref":"c-2"}],"version":1}`))
	f.Add([]byte(`{"tabs":[{"id":"a","kind":"chat","ref":"c-1"},{"id":"b","kind":"chat","ref":"c-1"}],"version":1}`))
	f.Add([]byte(`{"tabs":[{"id":"a","kind":"run","ref":"w","parent":"b"},{"id":"b","kind":"run","ref":"x","parent":"a"}],"version":2}`))
	f.Add([]byte(`{"tabs":[{"id":"","kind":"chat","ref":"c-1"}],"version":1}`))
	f.Add([]byte(`{"tabs":[{"id":"a","kind":"editor"}],"version":1}`))
	f.Add([]byte(`{"tabs":[{"id":"a","kind":"chat","ref":"c-1","pinned":true,"owns":true}],"version":-1}`))
	f.Add([]byte("nonsense"))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, doc []byte) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, FileName), doc, fileMode); err != nil {
			t.Fatalf("Setup: write %s: %v", FileName, err)
		}

		// The error is the caller's warning, not a verdict: a store always comes
		// back and it always has to be safe to use.
		s, _ := NewStore(dir)
		if s == nil {
			t.Fatal("NewStore returned a nil store; the caller has nothing to run on")
		}

		tabs, _ := s.List()
		if len(tabs) > MaxTabs {
			t.Fatalf("NewStore published %d tabs, over the MaxTabs decode bound of %d", len(tabs), MaxTabs)
		}
		ids := make(map[string]struct{}, len(tabs))
		subjects := make(map[subjectKey]struct{}, len(tabs))
		for _, tab := range tabs {
			if tab.ID == "" || len(tab.ID) > maxIDBytes {
				t.Fatalf("NewStore published id %q (%d bytes), want a non-empty id of at most %d", tab.ID, len(tab.ID), maxIDBytes)
			}
			if err := checkSubject(tab.Kind, tab.Ref); err != nil {
				t.Fatalf("NewStore published a subject Open would refuse: %v", err)
			}
			if _, dup := ids[tab.ID]; dup {
				t.Fatalf("NewStore published id %q twice; every state it publishes must be unique by id", tab.ID)
			}
			key := subjectKey{kind: tab.Kind, ref: tab.Ref}
			if _, dup := subjects[key]; dup {
				t.Fatalf("NewStore published (%s, %q) twice; an idempotent Open depends on one tab per subject", tab.Kind, tab.Ref)
			}
			ids[tab.ID] = struct{}{}
			subjects[key] = struct{}{}
		}

		// Usable afterwards: the one thing warn-and-start-empty promises.
		if _, _, _, err := s.Open(t.Context(), chatSpec("c-after-load")); err != nil && !errors.Is(err, ErrTooMany) {
			t.Fatalf("Open after loading this document = %v, want success or ErrTooMany", err)
		}
	})
}
