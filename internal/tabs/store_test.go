package tabs

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestOpen_IsIdempotentOnKindAndRef is the flag the client's open resolution
// depends on. An already-open (Kind, Ref) mutates nothing, so it bumps NO version
// and therefore emits no event — and with the event as the only render path, a
// caller that waited for one would wait forever. created=false is what tells it to
// resolve from the response instead.
func TestOpen_IsIdempotentOnKindAndRef(t *testing.T) {
	s, dir := newTestStore(t)
	first, created, v1, err := s.Open(t.Context(), chatSpec("c-a"))
	if err != nil || !created || v1 != 1 {
		t.Fatalf("Open(chat c-a) = (%+v, created %v, v%d, %v), want a new tab at version 1", first, created, v1, err)
	}

	second, created, v2, err := s.Open(t.Context(), chatSpec("c-a"))
	if err != nil {
		t.Fatalf("Open(chat c-a) again: %v", err)
	}
	if created {
		t.Error("Open(chat c-a) again returned created=true, want false: one tab per (kind, ref)")
	}
	if second.ID != first.ID {
		t.Errorf("Open(chat c-a) again = id %q, want the open tab's id %q", second.ID, first.ID)
	}
	if v2 != v1 {
		t.Errorf("Open(chat c-a) again = version %d, want %d unchanged: nothing was mutated", v2, v1)
	}
	if tabs, _ := s.List(); len(tabs) != 1 {
		t.Errorf("List() = %d tabs, want 1", len(tabs))
	}
	if doc := onDisk(t, dir); doc.Version != 1 || len(doc.Tabs) != 1 {
		t.Errorf("tabs.json = %d tabs at version %d, want 1 tab at version 1: an idempotent open must not rewrite the file", len(doc.Tabs), doc.Version)
	}
}

// TestOpen_IsIdempotentPerKind guards the pair rather than the ref alone: a chat
// and an editor could share a ref string, and they are two subjects.
func TestOpen_IsIdempotentPerKind(t *testing.T) {
	s, _ := newTestStore(t)
	chat := mustOpen(t, s, chatSpec("/workspace/a.ts"))
	editor := mustOpen(t, s, vibekit.OpenTab{Kind: vibekit.TabKindEditor, Ref: "/workspace/a.ts"})
	if chat.ID == editor.ID {
		t.Fatalf("a chat and an editor with the same ref share id %q; the key is (kind, ref)", chat.ID)
	}
	if tabs, version := s.List(); len(tabs) != 2 || version != 2 {
		t.Errorf("List() = %d tabs at version %d, want 2 tabs at version 2", len(tabs), version)
	}
}

// TestOpen_RefusesASpecItCannotHold asserts the SPECIFIC sentinel per case,
// because an outcome assertion cannot separate them: "Open failed" is the same
// observation for a kind nobody declared and a ref that belongs to another kind,
// and the caller answers those two differently.
func TestOpen_RefusesASpecItCannotHold(t *testing.T) {
	cases := []struct {
		desc string
		spec vibekit.OpenTab
		want error
	}{
		{desc: "a kind nobody declared", spec: vibekit.OpenTab{Kind: "sidebar", Ref: "x"}, want: ErrBadKind},
		{desc: "the empty kind", spec: vibekit.OpenTab{Ref: "x"}, want: ErrBadKind},
		{desc: "plan, deleted from the client on 2026-08-25", spec: vibekit.OpenTab{Kind: "plan"}, want: ErrBadKind},
		{desc: "a chat with no ref", spec: vibekit.OpenTab{Kind: vibekit.TabKindChat}, want: ErrBadRef},
		{desc: "an editor with no ref", spec: vibekit.OpenTab{Kind: vibekit.TabKindEditor}, want: ErrBadRef},
		{desc: "a singleton carrying a ref", spec: vibekit.OpenTab{Kind: vibekit.TabKindSettings, Ref: "general"}, want: ErrBadRef},
		{desc: "a ref one byte over the bound", spec: vibekit.OpenTab{Kind: vibekit.TabKindEditor, Ref: strings.Repeat("p", MaxRefBytes+1)}, want: ErrBadRef},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.desc, " ", "-"), func(t *testing.T) {
			s, _ := newTestStore(t)
			sub, created, version, err := s.Open(t.Context(), tc.spec)
			if !errors.Is(err, tc.want) {
				t.Errorf("Open(%+v) error = %v, want %v", tc.spec, err, tc.want)
			}
			if created || version != 0 || sub != (vibekit.TabSubject{}) {
				t.Errorf("Open(%+v) = (%+v, created %v, v%d) on a refusal, want the zero subject, created false and version 0",
					tc.spec, sub, created, version)
			}
			if tabs, v := s.List(); len(tabs) != 0 || v != 0 {
				t.Errorf("Open(%+v) applied something: List() = %d tabs at version %d, want nothing", tc.spec, len(tabs), v)
			}
		})
	}
}

// TestOpen_AtTheProductLimitRefusesWithErrTooMany covers the refusal whose
// consequence is user-visible: at the limit, New chat stops working, because
// creating a chat opens a tab for it. The limit is asserted as a boundary — the
// last one in succeeds, the next is refused — so an off-by-one in either direction
// fails here rather than in a strip.
func TestOpen_AtTheProductLimitRefusesWithErrTooMany(t *testing.T) {
	s, dir := newTestStore(t)
	for i := range MaxOpenTabs {
		if _, _, _, err := s.Open(t.Context(), chatSpec("c-"+strconv.Itoa(i))); err != nil {
			t.Fatalf("Open number %d of %d: %v", i+1, MaxOpenTabs, err)
		}
	}

	_, created, version, err := s.Open(t.Context(), chatSpec("c-one-too-many"))
	if !errors.Is(err, ErrTooMany) {
		t.Errorf("Open number %d = %v, want ErrTooMany", MaxOpenTabs+1, err)
	}
	if created || version != 0 {
		t.Errorf("Open past the limit = (created %v, v%d), want created false and version 0", created, version)
	}
	tabs, v := s.List()
	if len(tabs) != MaxOpenTabs || v != uint64(MaxOpenTabs) {
		t.Errorf("List() = %d tabs at version %d, want %d tabs at version %d", len(tabs), v, MaxOpenTabs, MaxOpenTabs)
	}
	if doc := onDisk(t, dir); len(doc.Tabs) != MaxOpenTabs {
		t.Errorf("tabs.json holds %d tabs, want %d: the refusal must not have written", len(doc.Tabs), MaxOpenTabs)
	}
}

// TestOpen_PlacesAChildAfterItsParentsExistingChildren pins the position rule
// against the client's insertSpec, which is the module that has to agree with it:
// a strip whose server order and client order differ would reshuffle on every
// reload.
func TestOpen_PlacesAChildAfterItsParentsExistingChildren(t *testing.T) {
	s, _ := newTestStore(t)
	parent := mustOpen(t, s, chatSpec("c-parent"))
	other := mustOpen(t, s, chatSpec("c-other"))
	kid1 := mustOpen(t, s, vibekit.OpenTab{Kind: vibekit.TabKindRun, Ref: "wf-1", Parent: parent.ID, Owns: true})
	kid2 := mustOpen(t, s, vibekit.OpenTab{Kind: vibekit.TabKindRun, Ref: "wf-2", Parent: parent.ID, Owns: true})
	grandkid := mustOpen(t, s, vibekit.OpenTab{Kind: vibekit.TabKindEditor, Ref: "/w/a.ts", Parent: kid1.ID})

	tabs, _ := s.List()
	want := []string{parent.ID, kid1.ID, grandkid.ID, kid2.ID, other.ID}
	if got := idsOf(tabs); !slices.Equal(got, want) {
		t.Errorf("List() order = %v, want %v (a child after its parent's existing children, a top-level tab at the end)",
			labels(tabs, map[string]string{parent.ID: "parent", other.ID: "other", kid1.ID: "kid1", kid2.ID: "kid2", grandkid.ID: "grandkid"}),
			[]string{"parent", "kid1", "grandkid", "kid2", "other"})
	}
	if !kid2.Owns {
		t.Error("Open with Owns=true returned Owns=false; the authority flag is set at open")
	}
}

// TestOpen_PromotesATabWhoseParentIsNotOpen is the client's own answer to an
// orphan (a tab nobody can see is worse than a tab in the wrong place), and it is
// what keeps TabSubject.Parent's promise true: every stored Parent named an open
// tab at the moment it was set.
func TestOpen_PromotesATabWhoseParentIsNotOpen(t *testing.T) {
	s, dir := newTestStore(t)
	top := mustOpen(t, s, chatSpec("c-top"))
	orphan := mustOpen(t, s, vibekit.OpenTab{Kind: vibekit.TabKindRun, Ref: "wf-9", Parent: "a-tab-that-closed"})

	if orphan.Parent != "" {
		t.Errorf("Open with an absent parent returned Parent %q, want it cleared", orphan.Parent)
	}
	tabs, _ := s.List()
	if got := idsOf(tabs); !slices.Equal(got, []string{top.ID, orphan.ID}) {
		t.Errorf("List() order = %v, want the promoted tab last", got)
	}
	for _, stored := range onDisk(t, dir).Tabs {
		if stored.ID == orphan.ID && stored.Parent != "" {
			t.Errorf("tabs.json kept Parent %q for the promoted tab; the file would carry a parent that names nothing", stored.Parent)
		}
	}
}

// TestClose_RemovesTheSubtreeInOneMutation is the reason Close returns what it
// removed: a parent with children is ONE mutation, so it is one version bump and
// one event, and the event can only name every removed id if the store hands them
// back.
func TestClose_RemovesTheSubtreeInOneMutation(t *testing.T) {
	s, dir := newTestStore(t)
	parent := mustOpen(t, s, chatSpec("c-parent"))
	kid := mustOpen(t, s, vibekit.OpenTab{Kind: vibekit.TabKindRun, Ref: "wf-1", Parent: parent.ID})
	grandkid := mustOpen(t, s, vibekit.OpenTab{Kind: vibekit.TabKindEditor, Ref: "/w/a.ts", Parent: kid.ID})
	survivor := mustOpen(t, s, chatSpec("c-survivor"))
	_, before := s.List()

	closed, version, err := s.Close(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("Close(%q): %v", parent.ID, err)
	}
	if version != before+1 {
		t.Errorf("Close of a parent with two descendants = version %d, want %d: one mutation is one bump", version, before+1)
	}
	gotIDs := idsOf(closed)
	slices.Sort(gotIDs)
	wantIDs := []string{parent.ID, kid.ID, grandkid.ID}
	slices.Sort(wantIDs)
	if !slices.Equal(gotIDs, wantIDs) {
		t.Errorf("Close(parent) removed %d tabs, want the parent, its child and its grandchild (got %v, want %v)", len(closed), gotIDs, wantIDs)
	}
	tabs, _ := s.List()
	if got := idsOf(tabs); !slices.Equal(got, []string{survivor.ID}) {
		t.Errorf("List() after Close = %v, want only the survivor %q", got, survivor.ID)
	}
	if doc := onDisk(t, dir); len(doc.Tabs) != 1 || doc.Version != version {
		t.Errorf("tabs.json = %d tabs at version %d, want 1 tab at version %d", len(doc.Tabs), doc.Version, version)
	}
}

// TestClose_ScattersDoNotEscape is the same claim after a drag. Reorder permits
// any permutation of the set, so a child can sit far from its parent; the closure
// walks parent POINTERS rather than positions, which is what makes that safe.
func TestClose_ScattersDoNotEscape(t *testing.T) {
	s, _ := newTestStore(t)
	parent := mustOpen(t, s, chatSpec("c-parent"))
	kid := mustOpen(t, s, vibekit.OpenTab{Kind: vibekit.TabKindRun, Ref: "wf-1", Parent: parent.ID})
	other := mustOpen(t, s, chatSpec("c-other"))
	if _, err := s.Reorder(t.Context(), []string{kid.ID, other.ID, parent.ID}); err != nil {
		t.Fatalf("Setup: Reorder: %v", err)
	}

	closed, _, err := s.Close(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("Close(%q): %v", parent.ID, err)
	}
	if len(closed) != 2 {
		t.Errorf("Close(parent) after a drag removed %d tabs, want 2: the child is a child wherever it sits", len(closed))
	}
	if tabs, _ := s.List(); len(tabs) != 1 || tabs[0].ID != other.ID {
		t.Errorf("List() = %+v, want only %q", tabs, other.ID)
	}
}

// TestClose_AnIDThatIsNotOpenIsNotAFailure records the decision that there is no
// was-closed bool: two devices can close the same tab, and len(closed) == 0
// already says nothing happened.
func TestClose_AnIDThatIsNotOpenIsNotAFailure(t *testing.T) {
	s, _ := newTestStore(t)
	only := mustOpen(t, s, chatSpec("c-a"))
	if _, _, err := s.Close(t.Context(), only.ID); err != nil {
		t.Fatalf("Setup: Close(%q): %v", only.ID, err)
	}
	_, after := s.List()

	closed, version, err := s.Close(t.Context(), only.ID)
	if err != nil {
		t.Errorf("Close(%q) twice = %v, want no error", only.ID, err)
	}
	if len(closed) != 0 {
		t.Errorf("Close(%q) twice removed %+v, want nothing", only.ID, closed)
	}
	if version != after {
		t.Errorf("Close(%q) twice = version %d, want %d unchanged: nothing changed, so nothing is broadcast", only.ID, version, after)
	}
}

// TestReorder_RefusesWithItsSentinelAndNamesTheOffender walks the three ways an
// order can fail the exact-set check. Every case asserts ErrOrderMismatch AND
// that the message names what was wrong, because the sentinel alone cannot
// separate a short list from a duplicate — and the caller is going to log one of
// these into a support conversation.
func TestReorder_RefusesWithItsSentinelAndNamesTheOffender(t *testing.T) {
	s, _ := newTestStore(t)
	a := mustOpen(t, s, chatSpec("c-a"))
	b := mustOpen(t, s, chatSpec("c-b"))
	cee := mustOpen(t, s, chatSpec("c-c"))
	wantOrder, wantVersion := s.List()

	cases := []struct {
		desc     string
		ids      []string
		wantText string
	}{
		{desc: "a short list, which is how a client that dropped a tab would ask", ids: []string{a.ID, b.ID}, wantText: "2 ids for 3 open tabs"},
		{desc: "a long list", ids: []string{a.ID, b.ID, cee.ID, "extra"}, wantText: "4 ids for 3 open tabs"},
		{desc: "an id that is not open", ids: []string{a.ID, b.ID, "not-open"}, wantText: `"not-open" is not open`},
		{desc: "the same id twice", ids: []string{a.ID, b.ID, b.ID}, wantText: fmt.Sprintf("%q appears twice", b.ID)},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.desc, " ", "-"), func(t *testing.T) {
			version, err := s.Reorder(t.Context(), tc.ids)
			if !errors.Is(err, ErrOrderMismatch) {
				t.Errorf("Reorder(%s) error = %v, want ErrOrderMismatch", tc.desc, err)
			}
			if err != nil && !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("Reorder(%s) error = %q, want it to name %q", tc.desc, err, tc.wantText)
			}
			if version != 0 {
				t.Errorf("Reorder(%s) = version %d on a refusal, want 0", tc.desc, version)
			}
			tabs, v := s.List()
			if !slices.Equal(tabs, wantOrder) || v != wantVersion {
				t.Errorf("Reorder(%s) applied something: order %v at version %d, want %v at %d",
					tc.desc, idsOf(tabs), v, idsOf(wantOrder), wantVersion)
			}
		})
	}
}

// TestReorder_AppliesTheGestureAndBumpsOnce is the accepted path, plus the
// no-change case: a tab dragged back where it started is not news, so it emits
// nothing.
func TestReorder_AppliesTheGestureAndBumpsOnce(t *testing.T) {
	s, dir := newTestStore(t)
	a := mustOpen(t, s, chatSpec("c-a"))
	b := mustOpen(t, s, chatSpec("c-b"))
	cee := mustOpen(t, s, chatSpec("c-c"))

	want := []string{cee.ID, a.ID, b.ID}
	version, err := s.Reorder(t.Context(), want)
	if err != nil {
		t.Fatalf("Reorder(%v): %v", want, err)
	}
	if version != 4 {
		t.Errorf("Reorder after three opens = version %d, want 4", version)
	}
	tabs, _ := s.List()
	if got := idsOf(tabs); !slices.Equal(got, want) {
		t.Errorf("List() order = %v, want %v", got, want)
	}
	if got := idsOf(onDisk(t, dir).Tabs); !slices.Equal(got, want) {
		t.Errorf("tabs.json order = %v, want %v", got, want)
	}

	same, err := s.Reorder(t.Context(), want)
	if err != nil {
		t.Fatalf("Reorder to the same order: %v", err)
	}
	if same != version {
		t.Errorf("Reorder to the same order = version %d, want %d unchanged", same, version)
	}
}

// TestReorder_IsAcceptedAfterAnUnrelatedPinBumpedTheVersion is the drag a
// base-version precondition would have thrown away. A pin bumps the version
// without changing the order, so a version check would refuse a gesture whose set
// is exactly right — which is why the exact-set check IS the precondition and the
// version is an output only.
func TestReorder_IsAcceptedAfterAnUnrelatedPinBumpedTheVersion(t *testing.T) {
	s, _ := newTestStore(t)
	a := mustOpen(t, s, chatSpec("c-a"))
	b := mustOpen(t, s, chatSpec("c-b"))

	// The client reads the order it is about to publish...
	before, seen := s.List()
	want := []string{b.ID, a.ID}

	// ...and another device pins something in between.
	pinned, err := s.SetPinned(t.Context(), a.ID, true)
	if err != nil {
		t.Fatalf("Setup: SetPinned(%q, true): %v", a.ID, err)
	}
	if pinned <= seen {
		t.Fatalf("Setup: SetPinned left version %d, want it above the %d the drag was derived from", pinned, seen)
	}

	version, err := s.Reorder(t.Context(), want)
	if err != nil {
		t.Fatalf("Reorder(%v) after an unrelated pin = %v, want it accepted", want, err)
	}
	if version != pinned+1 {
		t.Errorf("Reorder after an unrelated pin = version %d, want %d", version, pinned+1)
	}
	tabs, _ := s.List()
	if got := idsOf(tabs); !slices.Equal(got, want) {
		t.Errorf("List() order = %v, want %v (the gesture, not the order it was derived from: %v)", got, want, idsOf(before))
	}
	if !tabs[1].Pinned {
		t.Error("the pin was lost by the reorder; a reorder moves rows, it does not rewrite them")
	}
}

// TestSetPinned_BumpsOnceAndIsIdempotent covers both no-change cases, because the
// version increments only on a real state change and a repeated pin is the one a
// double click produces.
func TestSetPinned_BumpsOnceAndIsIdempotent(t *testing.T) {
	s, _ := newTestStore(t)
	a := mustOpen(t, s, chatSpec("c-a"))

	v1, err := s.SetPinned(t.Context(), a.ID, true)
	if err != nil || v1 != 2 {
		t.Fatalf("SetPinned(%q, true) = (v%d, %v), want version 2", a.ID, v1, err)
	}
	if tabs, _ := s.List(); !tabs[0].Pinned {
		t.Errorf("List()[0].Pinned = false after SetPinned(true)")
	}

	v2, err := s.SetPinned(t.Context(), a.ID, true)
	if err != nil {
		t.Fatalf("SetPinned(%q, true) again: %v", a.ID, err)
	}
	if v2 != v1 {
		t.Errorf("SetPinned(%q, true) again = version %d, want %d unchanged", a.ID, v2, v1)
	}

	v3, err := s.SetPinned(t.Context(), "not-open", true)
	if err != nil {
		t.Errorf("SetPinned on a tab that is not open = %v, want no error: a pin racing a close is not a failure", err)
	}
	if v3 != v1 {
		t.Errorf("SetPinned on a tab that is not open = version %d, want %d unchanged", v3, v1)
	}

	v4, err := s.SetPinned(t.Context(), a.ID, false)
	if err != nil || v4 != v1+1 {
		t.Errorf("SetPinned(%q, false) = (v%d, %v), want version %d", a.ID, v4, err, v1+1)
	}
}

// TestList_ReturnsACopy is the reason List clones: the store's slice IS the order,
// so handing it out would let a caller reorder the collection through the value it
// was given, with no mutation, no version bump and no event.
func TestList_ReturnsACopy(t *testing.T) {
	s, _ := newTestStore(t)
	a := mustOpen(t, s, chatSpec("c-a"))
	b := mustOpen(t, s, chatSpec("c-b"))

	tabs, _ := s.List()
	slices.Reverse(tabs)
	tabs[0].Pinned = true

	again, _ := s.List()
	if got := idsOf(again); !slices.Equal(got, []string{a.ID, b.ID}) {
		t.Errorf("List() = %v after a caller reversed a previous result, want %v", got, []string{a.ID, b.ID})
	}
	if again[0].Pinned {
		t.Error("a caller's write reached the store's own subject; the clone is not doing its job")
	}
}

// TestPrune_DropsWhatNoLongerResolvesAndPromotesTheOrphans is load-time crash
// recovery: the window between the chat record being removed and its tab being
// closed. It is ONE mutation, so a caller has one event to emit and one version to
// stamp it with.
func TestPrune_DropsWhatNoLongerResolvesAndPromotesTheOrphans(t *testing.T) {
	s, dir := newTestStore(t)
	gone := mustOpen(t, s, chatSpec("c-gone"))
	kid := mustOpen(t, s, vibekit.OpenTab{Kind: vibekit.TabKindRun, Ref: "wf-1", Parent: gone.ID})
	grandkid := mustOpen(t, s, vibekit.OpenTab{Kind: vibekit.TabKindEditor, Ref: "/w/a.ts", Parent: kid.ID})
	stays := mustOpen(t, s, chatSpec("c-stays"))
	_, before := s.List()

	dropped, version, err := s.Prune(t.Context(), func(sub vibekit.TabSubject) bool {
		return sub.Kind != vibekit.TabKindChat || sub.Ref != "c-gone"
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if version != before+1 {
		t.Errorf("Prune = version %d, want %d: a drop and two promotions are one mutation", version, before+1)
	}
	if got := idsOf(dropped); !slices.Equal(got, []string{gone.ID}) {
		t.Errorf("Prune dropped %v, want only the chat tab whose chat is gone (%q)", got, gone.ID)
	}

	tabs, _ := s.List()
	if got := idsOf(tabs); !slices.Equal(got, []string{kid.ID, grandkid.ID, stays.ID}) {
		t.Errorf("List() after Prune = %v, want the promoted child, its own child, and the survivor", got)
	}
	byID := map[string]vibekit.TabSubject{}
	for _, tab := range tabs {
		byID[tab.ID] = tab
	}
	if got := byID[kid.ID].Parent; got != "" {
		t.Errorf("the child of the dropped tab has Parent %q, want it promoted to top level", got)
	}
	if got := byID[grandkid.ID].Parent; got != kid.ID {
		t.Errorf("the grandchild has Parent %q, want %q: its own parent is still open, so it is not an orphan", got, kid.ID)
	}
	if doc := onDisk(t, dir); len(doc.Tabs) != 3 || doc.Version != version {
		t.Errorf("tabs.json = %d tabs at version %d, want 3 at %d", len(doc.Tabs), doc.Version, version)
	}
}

// TestPrune_ChangesNothingWhenEverythingResolves keeps Prune out of the version
// stream on the ordinary boot, which is every boot: a bump with no change would
// make every restart look like a mutation to every connected client.
func TestPrune_ChangesNothingWhenEverythingResolves(t *testing.T) {
	s, _ := newTestStore(t)
	mustOpen(t, s, chatSpec("c-a"))
	mustOpen(t, s, vibekit.OpenTab{Kind: vibekit.TabKindDocs})
	before, beforeVersion := s.List()

	dropped, version, err := s.Prune(t.Context(), func(vibekit.TabSubject) bool { return true })
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(dropped) != 0 || version != beforeVersion {
		t.Errorf("Prune over a clean set = (%d dropped, v%d), want nothing at version %d", len(dropped), version, beforeVersion)
	}
	after, _ := s.List()
	if !slices.Equal(after, before) {
		t.Errorf("Prune over a clean set changed the set: %v -> %v", idsOf(before), idsOf(after))
	}
}

// TestPrune_ANilPredicateResolvesEverything documents the seam's one degenerate
// input, which a caller with nothing to check against would otherwise have to fake
// with a function that always returns true. It reduces Prune to the orphan
// promotion, which on a set built through Open has nothing to do.
func TestPrune_ANilPredicateResolvesEverything(t *testing.T) {
	s, _ := newTestStore(t)
	parent := mustOpen(t, s, chatSpec("c-a"))
	kid := mustOpen(t, s, vibekit.OpenTab{Kind: vibekit.TabKindRun, Ref: "wf-1", Parent: parent.ID})
	before, beforeVersion := s.List()

	dropped, version, err := s.Prune(t.Context(), nil)
	if err != nil {
		t.Fatalf("Prune(nil): %v", err)
	}
	if len(dropped) != 0 || version != beforeVersion {
		t.Errorf("Prune(nil) = (%d dropped, v%d), want nothing at version %d", len(dropped), version, beforeVersion)
	}
	after, _ := s.List()
	if !slices.Equal(after, before) {
		t.Errorf("Prune(nil) changed the set: %v -> %v", idsOf(before), idsOf(after))
	}
	if after[1].Parent != parent.ID {
		t.Errorf("Prune(nil) promoted %q, whose parent %q is open", kid.ID, parent.ID)
	}
}

// TestClosure_TerminatesOnACycle is defence against a file no Open could have
// written. Parent is set at open and never reassigned, so a cycle is
// unrepresentable through the API — but a person with an editor can write one, and
// a recursive walk would exhaust the stack on it.
func TestClosure_TerminatesOnACycle(t *testing.T) {
	tabs := []vibekit.TabSubject{
		{ID: "a", Kind: vibekit.TabKindChat, Ref: "c-a", Parent: "b"},
		{ID: "b", Kind: vibekit.TabKindChat, Ref: "c-b", Parent: "a"},
		{ID: "c", Kind: vibekit.TabKindChat, Ref: "c-c"},
	}
	got := closure(tabs, "a")
	if len(got) != 2 {
		t.Errorf("closure over a cycle = %d ids, want 2 (a and b, each visited once)", len(got))
	}
	if _, ok := got["c"]; ok {
		t.Error("closure over a cycle reached an unrelated tab")
	}
	if closure(tabs, "not-open") != nil {
		t.Error("closure of an id that is not open = non-nil, want nil")
	}
}

// labels renders ids as the names a failure message can be read with, because a
// list of two hex ids says nothing about which tab was in the wrong place.
func labels(tabs []vibekit.TabSubject, names map[string]string) []string {
	out := make([]string, 0, len(tabs))
	for _, t := range tabs {
		if name, ok := names[t.ID]; ok {
			out = append(out, name)
			continue
		}
		out = append(out, t.ID)
	}
	return out
}
