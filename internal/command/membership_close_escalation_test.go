package command

// The retention-off close escalation: a close_tab that leaves a chat with no
// open tab DELETES that chat's record inside the same operation, and the
// teardown that follows runs on state captured before the record went.
//
// Every case drives the coordinator over a REAL tabs.Store (membership_test.go's
// rule): the doomed-set decision reads the store's subtree and the commit is the
// store's Close, so a fake store would let the decision and the commit agree
// while being wrong together. The chat store is the in-memory double, wired to
// the same recording bus so the tabs_changed / chat_deleted ORDER is observable
// on one timeline.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/vibekit/internal/settings"
	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// escalationHost is everything a close-escalation case reads back: the
// coordinator, the two stores, the bus both stores broadcast on, the recording
// teardown, and the liveness of the context each delete-grade teardown ran
// under (the roll-forward cases assert it survived the request's cancellation).
type escalationHost struct {
	mem         *Membership
	st          *tabs.Store
	bus         *tabBus
	store       ChatStore
	teardown    *recordingTeardown
	recordSeen  map[vibekit.ChatID]bool
	teardownCtx []error
}

// newEscalationHost wires a coordinator with the escalation seams: the given
// retention read, a delete grade that records the captured chain plus whether
// the record still existed when it ran, and a close grade that records the id.
func newEscalationHost(t *testing.T, store ChatStore, retention retentionRead) *escalationHost {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	return newEscalationHostOver(store, st, retention)
}

func newEscalationHostOver(store ChatStore, tabSet TabSet, retention retentionRead) *escalationHost {
	bus := &tabBus{}
	h := &escalationHost{
		st:         nil,
		bus:        bus,
		store:      store,
		teardown:   &recordingTeardown{},
		recordSeen: make(map[vibekit.ChatID]bool),
	}
	if realStore, ok := tabSet.(*tabs.Store); ok {
		h.st = realStore
	}
	h.mem = NewMembership(&MembershipDeps{
		Chats:    store,
		Tabs:     tabSet,
		Bus:      bus,
		Teardown: h.teardown,
		CloseChat: func(ctx context.Context, chatID vibekit.ChatID) {
			h.teardown.CloseChatState(ctx, chatID)
		},
		DeleteChat: func(ctx context.Context, chatID vibekit.ChatID, chain []string) {
			_, exists := store.Get(ctx, chatID)
			h.recordSeen[chatID] = exists
			h.teardownCtx = append(h.teardownCtx, ctx.Err())
			h.teardown.DeleteChatStateByChain(ctx, chatID, chain)
		},
		Retention: retention,
	})
	return h
}

// seedSessionedRecord seeds a chat whose record carries a session CHAIN — a
// retired session plus the live one — which is what the escalation must capture
// before the record goes.
func seedSessionedRecord(t *testing.T, store ChatStore, id vibekit.ChatID, chain ...string) {
	t.Helper()
	if err := store.Mutate(t.Context(), id, func(c *vibekit.Chat, _ bool) bool {
		c.Name = string(id)
		for _, sess := range chain {
			c.RecordSession(sess)
		}
		return true
	}); err != nil {
		t.Fatalf("seed %q: %v", id, err)
	}
}

func retentionOff(context.Context) bool { return false }
func retentionOn(context.Context) bool  { return true }

// eventTypes projects the bus's whole timeline, both stores' events included.
func eventTypes(bus *tabBus) []vibekit.EventType {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	out := make([]vibekit.EventType, 0, len(bus.events))
	for _, evt := range bus.events {
		out = append(out, evt.Type)
	}
	return out
}

// TestCloseTab_RetentionOffDeletesTheChatsTheCloseLeftTabless is the
// escalation's happy path, root and child at once: a parent chat tab with a
// tangent chat under it closes as one mutation, both chats become tabless, and
// with retention OFF both records are deleted IN the close operation — with the
// tabs_changed frame first, the captured chains delivered to the delete-grade
// teardown, and that teardown running after the records are gone.
func TestCloseTab_RetentionOffDeletesTheChatsTheCloseLeftTabless(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	h := newEscalationHost(t, store, retentionOff)
	store.Bus = h.bus
	seedSessionedRecord(t, store, "c-root", "sess-root-old", "sess-root-live")
	seedSessionedRecord(t, store, "c-tangent", "sess-tangent")

	root, err := h.mem.OpenTab(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-root"}, "op-r")
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	if _, err = h.mem.OpenTab(t.Context(),
		vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-tangent", Parent: root.Subject.ID}, "op-t"); err != nil {
		t.Fatalf("open tangent: %v", err)
	}

	closed, _, err := h.mem.CloseTab(t.Context(), root.Subject.ID, "op-close")
	if err != nil {
		t.Fatalf("CloseTab = %v", err)
	}
	if len(closed) != 2 {
		t.Fatalf("closed %d tabs, want 2 (parent + child)", len(closed))
	}

	// Both records went, children included — History (the chat list) agrees.
	for _, id := range []vibekit.ChatID{"c-root", "c-tangent"} {
		if _, ok := store.Get(t.Context(), id); ok {
			t.Errorf("chat %q still has a record after a retention-off last-tab close", id)
		}
	}

	// The teardown got the DELETE grade with the chain captured BEFORE the
	// delete — nothing can re-read it off the record now — and ran after the
	// record was gone. The close grade ran for nobody.
	wantChains := map[vibekit.ChatID][]string{
		"c-root":    {"sess-root-old", "sess-root-live"},
		"c-tangent": {"sess-tangent"},
	}
	for id, want := range wantChains {
		got, ok := h.teardown.deletedByChain[id]
		if !ok {
			t.Errorf("no delete-grade teardown ran for %q", id)
			continue
		}
		if !slices.Equal(got, want) {
			t.Errorf("captured chain for %q = %v, want %v", id, got, want)
		}
		if h.recordSeen[id] {
			t.Errorf("the delete-grade teardown for %q ran while the record still existed; the escalation must delete first and tear down from the capture", id)
		}
	}
	if len(h.teardown.closed) != 0 {
		t.Errorf("close-grade teardown ran for %v alongside the delete grade; a doomed chat gets exactly one grade", h.teardown.closed)
	}

	// The frame order is the delete path's, cross-device: tabs_changed applies
	// the removal first, then each chat_deleted is a no-op on a row already gone.
	// Keyed on the CLOSE's own frame (the one naming removed ids) — the opens
	// above also emitted tabs_changed, so the first frame of that type proves
	// nothing.
	h.bus.mu.Lock()
	removalAt, deletedAt := -1, -1
	for i, evt := range h.bus.events {
		if p, ok := evt.Payload.(vibekit.TabsChangedPayload); ok && len(p.RemovedIDs) > 0 && removalAt < 0 {
			removalAt = i
		}
		if evt.Type == vibekit.EventChatDeleted && deletedAt < 0 {
			deletedAt = i
		}
	}
	h.bus.mu.Unlock()
	if removalAt < 0 || deletedAt < 0 || removalAt > deletedAt {
		t.Errorf("removal frame at %d, first chat_deleted at %d; want the tabs_changed removal before every chat_deleted", removalAt, deletedAt)
	}
	deletes := 0
	for _, typ := range eventTypes(h.bus) {
		if typ == vibekit.EventChatDeleted {
			deletes++
		}
	}
	if deletes != 2 {
		t.Errorf("saw %d chat_deleted events, want 2 (root + tangent)", deletes)
	}
}

// fixedTabs is a minimal TabSet for the one case the REAL store cannot stage:
// two open tabs for one chat ref. The store's load-time sanitize enforces
// (kind, ref) uniqueness, so the remaining-refs arm of the doomed set — pure
// coordinator arithmetic — gets a stub whose answers the case declares.
type fixedTabs struct {
	open    []vibekit.TabSubject
	subtree []vibekit.TabSubject
	closed  []vibekit.TabSubject
}

func (f *fixedTabs) Open(context.Context, vibekit.OpenTab) (vibekit.TabSubject, bool, uint64, error) {
	return vibekit.TabSubject{}, false, 0, errors.New("not staged")
}

func (f *fixedTabs) Close(context.Context, string) ([]vibekit.TabSubject, uint64, error) {
	return f.closed, 3, nil
}

func (f *fixedTabs) Reorder(context.Context, []string) (uint64, error) { return 0, nil }

func (f *fixedTabs) SetPinned(context.Context, string, bool) (uint64, error) { return 0, nil }

func (f *fixedTabs) List() ([]vibekit.TabSubject, uint64) { return f.open, 2 }

func (f *fixedTabs) Subtree(string) []vibekit.TabSubject { return f.subtree }

// TestCloseTab_LeavesAChatWithARemainingTabAlone pins the remaining-refs half
// of the doomed set: a chat is doomed only when the close takes its LAST tab.
// The real store cannot hold two tabs for one ref (sanitize enforces
// uniqueness), so the arithmetic is pinned over a declared set: closing one of
// a chat's two tabs must delete nothing and run the ordinary close grade.
func TestCloseTab_LeavesAChatWithARemainingTabAlone(t *testing.T) {
	one := vibekit.TabSubject{ID: "tb_one", Kind: vibekit.TabKindChat, Ref: "c-x"}
	two := vibekit.TabSubject{ID: "tb_two", Kind: vibekit.TabKindChat, Ref: "c-x"}
	store := testsupport.NewInMemoryChatStore()
	h := newEscalationHostOver(store, &fixedTabs{
		open:    []vibekit.TabSubject{one, two},
		subtree: []vibekit.TabSubject{one},
		closed:  []vibekit.TabSubject{one},
	}, retentionOff)
	store.Bus = h.bus
	seedSessionedRecord(t, store, "c-x", "sess-x")

	closed, _, err := h.mem.CloseTab(t.Context(), "tb_one", "op-close")
	if err != nil {
		t.Fatalf("CloseTab = %v", err)
	}
	if len(closed) != 1 {
		t.Fatalf("closed %d tabs, want 1", len(closed))
	}
	if _, ok := store.Get(t.Context(), "c-x"); !ok {
		t.Error("closing one of a chat's two tabs deleted the record; only the LAST tab may")
	}
	if len(h.teardown.deletedByChain) != 0 {
		t.Errorf("delete-grade teardown ran for %v; the chat still has a tab", h.teardown.deletedByChain)
	}
	if !slices.Contains(h.teardown.closed, vibekit.ChatID("c-x")) {
		t.Errorf("close-grade teardown ran for %v, want c-x", h.teardown.closed)
	}
}

// TestCloseTab_SubtreeWithoutTheChatsTabDeletesNothing is the ordinary non-last
// shape reachable without a hand-edited file: closing a non-chat child under a
// chat tab takes no chat tab with it, so nothing is doomed whatever retention
// says.
func TestCloseTab_SubtreeWithoutTheChatsTabDeletesNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	h := newEscalationHost(t, store, retentionOff)
	seedSessionedRecord(t, store, "c-a", "sess-a")
	parent, err := h.mem.OpenTab(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-a"}, "op-a")
	if err != nil {
		t.Fatalf("open chat: %v", err)
	}
	child, err := h.mem.OpenTab(t.Context(),
		vibekit.OpenTab{Kind: vibekit.TabKindRun, Ref: "wf_1", Parent: parent.Subject.ID}, "op-run")
	if err != nil {
		t.Fatalf("open run child: %v", err)
	}

	if _, _, err = h.mem.CloseTab(t.Context(), child.Subject.ID, "op-close"); err != nil {
		t.Fatalf("CloseTab = %v", err)
	}
	if _, ok := store.Get(t.Context(), "c-a"); !ok {
		t.Error("closing a run child deleted the chat's record")
	}
	if len(h.teardown.deletedByChain) != 0 || len(h.teardown.closed) != 0 {
		t.Errorf("teardown ran (delete=%v close=%v) for a close that took no chat tab",
			h.teardown.deletedByChain, h.teardown.closed)
	}
}

// TestCloseTab_RetentionDecidesWhetherTheRecordSurvives folds the three
// keep-direction predicates into one table: retention ON, an ABSENT
// config.json, and an UNREADABLE config.json each delete nothing on a last-tab
// close — the last two through the REAL settings read, because they ARE the
// fail-toward-keeping contract — while the close itself still commits and the
// close-grade teardown still runs.
func TestCloseTab_RetentionDecidesWhetherTheRecordSurvives(t *testing.T) {
	cases := []struct {
		name      string
		retention func(t *testing.T) retentionRead
	}{
		{name: "retention ON", retention: func(*testing.T) retentionRead { return retentionOn }},
		{name: "absent config.json", retention: func(t *testing.T) retentionRead {
			dir := t.TempDir()
			return func(ctx context.Context) bool { return settings.RetentionEnabled(ctx, dir) }
		}},
		{name: "unreadable config.json", retention: func(t *testing.T) retentionRead {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{ not json"), 0o600); err != nil {
				t.Fatalf("write config.json: %v", err)
			}
			return func(ctx context.Context) bool { return settings.RetentionEnabled(ctx, dir) }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			h := newEscalationHost(t, store, tc.retention(t))
			store.Bus = h.bus
			seedSessionedRecord(t, store, "c-keep", "sess-keep")
			opened, err := h.mem.OpenTab(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-keep"}, "op-o")
			if err != nil {
				t.Fatalf("open: %v", err)
			}

			closed, _, err := h.mem.CloseTab(t.Context(), opened.Subject.ID, "op-close")
			if err != nil {
				t.Fatalf("CloseTab = %v", err)
			}
			if len(closed) != 1 {
				t.Fatalf("closed %d tabs, want 1", len(closed))
			}
			if _, ok := store.Get(t.Context(), "c-keep"); !ok {
				t.Error("a last-tab close deleted the record; this predicate must keep it")
			}
			if len(h.teardown.deletedByChain) != 0 {
				t.Errorf("delete-grade teardown ran for %v under a keeping predicate", h.teardown.deletedByChain)
			}
			if !slices.Contains(h.teardown.closed, vibekit.ChatID("c-keep")) {
				t.Errorf("close-grade teardown ran for %v, want c-keep", h.teardown.closed)
			}
			if got := slices.Index(eventTypes(h.bus), vibekit.EventChatDeleted); got >= 0 {
				t.Error("a chat_deleted frame went out for a record that must survive")
			}
		})
	}
}

// failingDeleteStore refuses every Delete, which is the post-commit failure the
// roll-forward contract is about: the tabs are gone, the record is not.
type failingDeleteStore struct {
	*testsupport.InMemoryChatStore
}

var errDeleteRefused = errors.New("simulated chat file removal failure")

func (s *failingDeleteStore) Delete(context.Context, vibekit.ChatID) error {
	return errDeleteRefused
}

// TestCloseTab_RecordDeleteFailureStillAnswersSuccess: after the commit point
// there is no rollback. A failed record delete logs and DEMOTES the chat to the
// close grade — its record and sessions survive together, so it stays
// reopenable — and the close still answers success with the closed ids.
func TestCloseTab_RecordDeleteFailureStillAnswersSuccess(t *testing.T) {
	store := &failingDeleteStore{InMemoryChatStore: testsupport.NewInMemoryChatStore()}
	h := newEscalationHost(t, store, retentionOff)
	seedSessionedRecord(t, store.InMemoryChatStore, "c-stuck", "sess-stuck")
	opened, err := h.mem.OpenTab(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-stuck"}, "op-o")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	closed, _, err := h.mem.CloseTab(t.Context(), opened.Subject.ID, "op-close")
	if err != nil {
		t.Fatalf("CloseTab = %v, want success — post-commit failures roll forward", err)
	}
	if len(closed) != 1 || closed[0].ID != opened.Subject.ID {
		t.Fatalf("closed = %v, want the closed tab's id", closed)
	}
	if _, ok := store.Get(t.Context(), "c-stuck"); !ok {
		t.Error("the record is gone even though Delete failed")
	}
	if open, _ := h.st.List(); len(open) != 0 {
		t.Errorf("the tab survived: %v — the close committed and must stay committed", open)
	}
	if len(h.teardown.deletedByChain) != 0 {
		t.Errorf("delete-grade teardown ran for %v; a surviving record keeps its sessions (close grade)", h.teardown.deletedByChain)
	}
	if !slices.Contains(h.teardown.closed, vibekit.ChatID("c-stuck")) {
		t.Errorf("close-grade teardown ran for %v, want c-stuck", h.teardown.closed)
	}
}

// ctxCheckingStore mirrors the real chat.Store's first line: Delete refuses a
// canceled context. It is what makes the roll-forward test below able to FAIL —
// the in-memory double ignores ctx, so without this the record would delete
// even off the dead request context.
type ctxCheckingStore struct {
	*testsupport.InMemoryChatStore
}

func (s *ctxCheckingStore) Delete(ctx context.Context, id vibekit.ChatID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.InMemoryChatStore.Delete(ctx, id)
}

// cancelOnCommitTabs cancels the request context at the exact moment the tab
// close commits — the client walked away with the response undeliverable —
// which is the window the detached roll-forward context exists for.
type cancelOnCommitTabs struct {
	*tabs.Store
	cancel context.CancelFunc
}

func (c *cancelOnCommitTabs) Close(ctx context.Context, id string) ([]vibekit.TabSubject, uint64, error) {
	closed, v, err := c.Store.Close(ctx, id)
	c.cancel()
	return closed, v, err
}

// TestCloseTab_ClientAbandonedRequestStillRollsForward: a request context
// canceled after the commit point must not cancel the record delete or the
// teardown — both run under a context detached from the request.
func TestCloseTab_ClientAbandonedRequestStillRollsForward(t *testing.T) {
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	reqCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store := &ctxCheckingStore{InMemoryChatStore: testsupport.NewInMemoryChatStore()}
	h := newEscalationHostOver(store, &cancelOnCommitTabs{Store: st, cancel: cancel}, retentionOff)
	seedSessionedRecord(t, store.InMemoryChatStore, "c-gone", "sess-gone")
	opened, err := h.mem.OpenTab(reqCtx, vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-gone"}, "op-o")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	closed, _, err := h.mem.CloseTab(reqCtx, opened.Subject.ID, "op-close")
	if err != nil {
		t.Fatalf("CloseTab = %v, want success", err)
	}
	if len(closed) != 1 {
		t.Fatalf("closed %d tabs, want 1", len(closed))
	}
	if reqCtx.Err() == nil {
		t.Fatal("fixture: the request context was never canceled, so this case proves nothing")
	}
	if _, ok := store.Get(t.Context(), "c-gone"); ok {
		t.Error("the record survived: the roll-forward was canceled with the request")
	}
	chain, ok := h.teardown.deletedByChain["c-gone"]
	if !ok || !slices.Equal(chain, []string{"sess-gone"}) {
		t.Errorf("delete-grade teardown chain = %v (ran %v), want [sess-gone]", chain, ok)
	}
	for _, ctxErr := range h.teardownCtx {
		if ctxErr != nil {
			t.Errorf("the delete-grade teardown ran under a dead context (%v); it must be detached from the request", ctxErr)
		}
	}
}

// TestCloseTab_RecordlessChatSkipped: a chat tab whose record does not exist —
// the crash residue between a delete's two writes — is closed and torn down
// like any chat tab, and the escalation SKIPS it: no chats.Delete and no
// chat_deleted for an id no device knows.
func TestCloseTab_RecordlessChatSkipped(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	h := newEscalationHost(t, store, retentionOff)
	store.Bus = h.bus
	seedRecord(t, store, "c-ghost")
	opened, err := h.mem.OpenTab(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-ghost"}, "op-o")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// The record vanishes out from under the tab (a crashed delete's window).
	if err := store.Delete(t.Context(), "c-ghost"); err != nil {
		t.Fatalf("remove the record: %v", err)
	}
	deletesBefore := 0
	for _, typ := range eventTypes(h.bus) {
		if typ == vibekit.EventChatDeleted {
			deletesBefore++
		}
	}

	closed, _, err := h.mem.CloseTab(t.Context(), opened.Subject.ID, "op-close")
	if err != nil {
		t.Fatalf("CloseTab = %v", err)
	}
	if len(closed) != 1 {
		t.Fatalf("closed %d tabs, want 1", len(closed))
	}
	if len(h.teardown.deletedByChain) != 0 {
		t.Errorf("delete-grade teardown ran for %v; a recordless chat is skipped", h.teardown.deletedByChain)
	}
	if !slices.Contains(h.teardown.closed, vibekit.ChatID("c-ghost")) {
		t.Errorf("close-grade teardown ran for %v, want c-ghost", h.teardown.closed)
	}
	deletesAfter := 0
	for _, typ := range eventTypes(h.bus) {
		if typ == vibekit.EventChatDeleted {
			deletesAfter++
		}
	}
	if deletesAfter != deletesBefore {
		t.Errorf("a chat_deleted frame went out for a recordless chat (%d -> %d)", deletesBefore, deletesAfter)
	}
}
