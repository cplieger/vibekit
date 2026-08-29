package command

// The membership coordinator's contract: the ordering that makes a chat without
// its tab, or a tab without its chat, unreachable.
//
// Every case here drives the coordinator over a REAL tabs.Store, because the
// properties are the store's behaviour under the coordinator's lock — the version
// a mutation produced, the order it left behind, one event per committed mutation.
// A fake store would let both halves agree while being wrong together.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// flakyTabs is a real tab store with two things bolted on: Close can be made to
// fail a set number of times, and a hook can run at the moment a close is about
// to happen.
//
// It embeds the real store rather than reimplementing one, so every case that is
// not about the failure exercises production behaviour. The two knobs are what no
// real store can offer: a persist failure that is TRANSIENT (which is what the
// retry rule is about) and an observation point INSIDE the coordinator's critical
// section.
type flakyTabs struct {
	*tabs.Store
	// beforeClose runs before each Close. It is what lets a test observe the state
	// between the record removal and the tab close — the window the delete
	// ordering exists to make harmless.
	beforeClose func()
	mu          sync.Mutex
	// failCloses is how many of the next Close calls fail. Counted down, so
	// failCloses:1 is the transient failure the retry is supposed to absorb and a
	// large number is the permanent one that has to emit anyway.
	failCloses int
	// failOpens is how many of the next Open calls fail, which is the only way to
	// reach the state a create retry has to repair: chat written, tab not.
	failOpens int
}

var errCloseFailed = errors.New("simulated tabs.json write failure")

func (f *flakyTabs) Close(ctx context.Context, id string) ([]vibekit.TabSubject, uint64, error) {
	f.mu.Lock()
	hook, fail := f.beforeClose, f.failCloses > 0
	if fail {
		f.failCloses--
	}
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if fail {
		return nil, 0, errCloseFailed
	}
	return f.Store.Close(ctx, id)
}

// newFlakyMembership is newTabbedMembership with the failure-injecting store.
func newFlakyMembership(t *testing.T, chats ChatStore) (*Membership, *flakyTabs, *tabBus) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	flaky := &flakyTabs{Store: st}
	bus := &tabBus{}
	teardown := &recordingTeardown{}
	return NewMembership(&MembershipDeps{Chats: chats, Tabs: flaky, Bus: bus, Teardown: teardown}), flaky, bus
}

// recordingTeardown is the delete path's teardown seam. The ordering tests
// assert nothing on it — they are about the two STORES — while the close
// escalation's tests read back which grade ran and what chain travelled.
type recordingTeardown struct {
	deleted        []vibekit.ChatID
	deletedByChain map[vibekit.ChatID][]string
	closed         []vibekit.ChatID
	mu             sync.Mutex
}

func (r *recordingTeardown) DeleteChatState(_ context.Context, id vibekit.ChatID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, id)
}

func (r *recordingTeardown) DeleteChatStateByChain(_ context.Context, id vibekit.ChatID, chain []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deletedByChain == nil {
		r.deletedByChain = make(map[vibekit.ChatID][]string)
	}
	r.deletedByChain[id] = slices.Clone(chain)
}

func (r *recordingTeardown) CloseChatState(_ context.Context, id vibekit.ChatID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = append(r.closed, id)
}

// createChat is the ordinary create every case starts from.
func createChat(t *testing.T, mem *Membership, opID string) ChatOpened {
	t.Helper()
	opened, err := mem.CreateChatAndOpen(t.Context(), ChatCreate{
		OpID: opID,
		Init: func(c *vibekit.Chat) { c.Name = vibekit.DefaultChatName },
	})
	if err != nil {
		t.Fatalf("CreateChatAndOpen(op %q) = %v, want it to succeed", opID, err)
	}
	return opened
}

// tabIDsFor is every tab in the store showing this chat, which is the population
// "a chat has a tab" and "a chat's tabs are gone" are claims about.
func tabIDsFor(st *tabs.Store, chatID vibekit.ChatID) []string {
	open, _ := st.List()
	var out []string
	for _, tab := range open {
		if tab.Kind == vibekit.TabKindChat && tab.Ref == string(chatID) {
			out = append(out, tab.ID)
		}
	}
	return out
}

// TestCreateChatAndOpen_WritesTheChatThenItsTab is the create half of the gate:
// both stores hold the new chat afterwards, and the response names both.
func TestCreateChatAndOpen_WritesTheChatThenItsTab(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)

	opened := createChat(t, mem, "op-1")

	if _, ok := store.Get(t.Context(), vibekit.ChatID(opened.Chat.ID)); !ok {
		t.Fatalf("the created chat %q is not in the chat store", opened.Chat.ID)
	}
	if got := tabIDsFor(st, vibekit.ChatID(opened.Chat.ID)); len(got) != 1 || got[0] != opened.Subject.ID {
		t.Errorf("tabs for the new chat = %v, want exactly the returned subject %q", got, opened.Subject.ID)
	}
	if opened.Version != 1 {
		t.Errorf("version = %d, want 1: the first mutation of an empty collection", opened.Version)
	}
	frames := bus.frames(t)
	if len(frames) != 1 {
		t.Fatalf("bus saw %d tabs_changed frames, want exactly 1 for one committed mutation", len(frames))
	}
	if frames[0].Changed == nil || frames[0].Changed.ID != opened.Subject.ID {
		t.Errorf("frame's changed tab = %+v, want the subject %q", frames[0].Changed, opened.Subject.ID)
	}
	if frames[0].OpID != "op-1" {
		t.Errorf("frame carries op_id %q, want the op the caller sent", frames[0].OpID)
	}
	if !slices.Equal(frames[0].Order, []string{opened.Subject.ID}) {
		t.Errorf("frame's order = %v, want the one open tab", frames[0].Order)
	}
}

// TestCreateChatAndOpen_ReplayFinishesAMissingTabWrite is the requirement the
// stage-1b ledger did not have: a retry of an op whose FIRST attempt created the
// chat and then failed its tab write must finish that write, not answer with a
// chat that has no tab.
//
// The first attempt's failure is injected, because that is the only way to reach
// the state — and it is a state a real deployment reaches on a full disk.
func TestCreateChatAndOpen_ReplayFinishesAMissingTabWrite(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, flaky, bus := newFlakyMembership(t, store)
	flaky.openFails(1)

	// First attempt: the chat lands, the tab does not.
	_, err := mem.CreateChatAndOpen(t.Context(), ChatCreate{
		OpID: "op-retry", Init: func(c *vibekit.Chat) { c.Name = "Half made" },
	})
	if err == nil {
		t.Fatal("the first attempt reported success, so the fixture did not inject a failure")
	}
	ids := storedChatIDs(t, store)
	if len(ids) != 1 {
		t.Fatalf("after a failed tab write the store holds %d chats (%v), want 1: the record leads and is kept", len(ids), ids)
	}
	if got := tabIDsFor(flaky.Store, ids[0]); len(got) != 0 {
		t.Fatalf("the first attempt left tabs %v, so there is no missing write to finish", got)
	}

	// The retry carries the same op id.
	opened, err := mem.CreateChatAndOpen(t.Context(), ChatCreate{
		OpID: "op-retry", Init: func(c *vibekit.Chat) { c.Name = "Half made" },
	})
	if err != nil {
		t.Fatalf("the retry = %v, want it to succeed", err)
	}

	if vibekit.ChatID(opened.Chat.ID) != ids[0] {
		t.Errorf("the retry answered with chat %q, want the first attempt's %q", opened.Chat.ID, ids[0])
	}
	if !opened.Replay {
		t.Error("the retry did not report a replay, so the ledger did not resolve it")
	}
	if got := tabIDsFor(flaky.Store, ids[0]); len(got) != 1 {
		t.Errorf("tabs for the replayed chat = %v, want exactly 1: the replay owes the missing tab write", got)
	}
	if opened.Subject.ID == "" {
		t.Error("the replay answered with no subject, so the caller has nothing to show")
	}
	if got := storedChatIDs(t, store); len(got) != 1 {
		t.Errorf("store holds %d chats (%v), want 1: one gesture is one chat", len(got), got)
	}
	if frames := bus.frames(t); len(frames) != 1 {
		t.Errorf("bus saw %d frames, want 1: only the replay committed a mutation", len(frames))
	}
}

// TestCreateChatAndOpen_AtTheLimitLeavesTheChatStoreUnchanged is the capacity
// reservation, and the orphan it prevents is the whole reason the reservation runs
// before the mint. Reversed, the refusal lands after the record is written and the
// gesture leaves a chat nothing can ever open.
func TestCreateChatAndOpen_AtTheLimitLeavesTheChatStoreUnchanged(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	fillTabs(t, mem, tabs.MaxOpenTabs)
	before := len(storedChatIDs(t, store))
	framesBefore := len(bus.frames(t))

	_, err := mem.CreateChatAndOpen(t.Context(), ChatCreate{
		OpID: "op-full", Init: func(c *vibekit.Chat) { c.Name = "Never made" },
	})

	if statusOf(err) != http.StatusConflict {
		t.Fatalf("status = %d, want 409 at the limit (%s)", statusOf(err), errText(err))
	}
	if !errors.Is(err, errTabsFull) {
		t.Errorf("error = %v, want errTabsFull", err)
	}
	if got := storedChatIDs(t, store); len(got) != before {
		t.Errorf("store holds %d chats, want the %d it held before: a refused create must not leave an orphan", len(got), before)
	}
	if open, _ := st.List(); len(open) != tabs.MaxOpenTabs {
		t.Errorf("the set holds %d tabs, want %d unchanged", len(open), tabs.MaxOpenTabs)
	}
	if got := len(bus.frames(t)); got != framesBefore {
		t.Errorf("a refused create emitted %d new frames, want 0", got-framesBefore)
	}
}

// TestOpenTab_AtTheLimitIsRefusedAndTheChatStoreIsUntouched is open_tab's half of
// the same limit. open_tab NEVER mints, so the chat store must be exactly as it
// was — asserted rather than assumed, because "open_tab does not create a chat" is
// the split this whole design rests on.
func TestOpenTab_AtTheLimitIsRefusedAndTheChatStoreIsUntouched(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newTabbedMembership(t, store)
	// One real chat, so the refusal is the LIMIT rather than the missing-chat gate.
	opened := createChat(t, mem, "op-seed")
	seedRecord(t, store, "c-waiting")
	fillTabs(t, mem, tabs.MaxOpenTabs)
	before := storedChatIDs(t, store)

	_, err := mem.OpenTab(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-waiting"}, "op-open")

	if statusOf(err) != http.StatusConflict {
		t.Fatalf("status = %d, want 409 at the limit (%s)", statusOf(err), errText(err))
	}
	if got := storedChatIDs(t, store); !slices.Equal(got, before) {
		t.Errorf("chat store went from %v to %v; open_tab must never write a chat", before, got)
	}
	if open, _ := st.List(); len(open) != tabs.MaxOpenTabs {
		t.Errorf("the set holds %d tabs, want %d: nothing was opened", len(open), tabs.MaxOpenTabs)
	}
	if got := tabIDsFor(st, vibekit.ChatID(opened.Chat.ID)); len(got) != 1 {
		t.Errorf("the seeded chat's tab = %v, want it untouched", got)
	}
}

// TestOpenTab_IsIdempotentAndSaysSo is why `created` is on the response at all: a
// second open of one subject commits nothing, so it emits nothing, so a caller
// waiting for an event would wait forever.
func TestOpenTab_IsIdempotentAndSaysSo(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, bus := newTabbedMembership(t, store)
	seedRecord(t, store, "c-open")

	first, err := mem.OpenTab(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-open"}, "op-a")
	if err != nil {
		t.Fatalf("first open = %v", err)
	}
	second, err := mem.OpenTab(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-open"}, "op-b")
	if err != nil {
		t.Fatalf("second open = %v", err)
	}

	if !first.Created {
		t.Error("the first open reported created:false")
	}
	if second.Created {
		t.Error("the second open reported created:true; one (kind, ref) is one tab")
	}
	if second.Subject.ID != first.Subject.ID {
		t.Errorf("the second open returned tab %q, want the open one %q", second.Subject.ID, first.Subject.ID)
	}
	if second.Version != first.Version {
		t.Errorf("version moved from %d to %d; an idempotent open commits nothing", first.Version, second.Version)
	}
	if got := len(bus.frames(t)); got != 1 {
		t.Errorf("bus saw %d frames, want 1: the idempotent open emits nothing", got)
	}
}

// TestOpenTab_ForAChatThatIsGoneIsRefused is the delete ordering seen from the
// open side. The record is the gate, so an open for a chat that is not there is a
// 404 rather than a tab pointing at nothing.
func TestOpenTab_ForAChatThatIsGoneIsRefused(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newTabbedMembership(t, store)

	_, err := mem.OpenTab(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-never"}, "op-a")

	if statusOf(err) != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", statusOf(err), errText(err))
	}
	if !errors.Is(err, errOpenChatUnknown) {
		t.Errorf("error = %v, want errOpenChatUnknown", err)
	}
	if open, _ := st.List(); len(open) != 0 {
		t.Errorf("the refused open left %d tabs behind, want none", len(open))
	}
}

// TestCloseTab_AParentAndItsChildrenAreOneMutation is the reason the event carries
// removed_ids as a LIST and the reason there is one event type rather than two: a
// singular per-tab event would have forced either several frames sharing one
// version (the second reads as a duplicate) or several bumps for one mutation.
func TestCloseTab_AParentAndItsChildrenAreOneMutation(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	parent := createChat(t, mem, "op-parent")
	childA := openChild(t, mem, store, "c-child-a", parent.Subject.ID)
	childB := openChild(t, mem, store, "c-child-b", parent.Subject.ID)
	survivor := createChat(t, mem, "op-survivor")
	_, versionBefore := st.List()
	framesBefore := len(bus.frames(t))

	closed, version, err := mem.CloseTab(t.Context(), parent.Subject.ID, "op-close")
	if err != nil {
		t.Fatalf("CloseTab = %v", err)
	}

	want := []string{parent.Subject.ID, childA.Subject.ID, childB.Subject.ID}
	got := subjectIDs(closed)
	slices.Sort(got)
	wantSorted := slices.Clone(want)
	slices.Sort(wantSorted)
	if !slices.Equal(got, wantSorted) {
		t.Errorf("closed = %v, want the parent and both children %v", got, wantSorted)
	}
	if version != versionBefore+1 {
		t.Errorf("version went %d -> %d, want exactly one bump for one mutation", versionBefore, version)
	}
	frames := bus.frames(t)
	if len(frames)-framesBefore != 1 {
		t.Fatalf("the close emitted %d frames, want exactly 1", len(frames)-framesBefore)
	}
	last := frames[len(frames)-1]
	removed := slices.Clone(last.RemovedIDs)
	slices.Sort(removed)
	if !slices.Equal(removed, wantSorted) {
		t.Errorf("removed_ids = %v, want every id that went %v", last.RemovedIDs, wantSorted)
	}
	if last.Version != version {
		t.Errorf("frame version = %d, want the version the mutation committed (%d)", last.Version, version)
	}
	if !slices.Equal(last.Order, []string{survivor.Subject.ID}) {
		t.Errorf("frame order = %v, want the one surviving tab %q", last.Order, survivor.Subject.ID)
	}
}

// TestCloseTab_AnIdThatIsNotOpenIsNotAnError: two devices can close the same tab,
// and an empty closed list already says nothing happened.
func TestCloseTab_AnIdThatIsNotOpenIsNotAnError(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	createChat(t, mem, "op-a")
	_, before := st.List()

	closed, version, err := mem.CloseTab(t.Context(), "not-open", "op-close")
	if err != nil {
		t.Fatalf("closing an absent id = %v, want no error", err)
	}
	if len(closed) != 0 {
		t.Errorf("closed = %v, want nothing", subjectIDs(closed))
	}
	if version != before {
		t.Errorf("version went %d -> %d, want it unchanged", before, version)
	}
	if got := len(bus.frames(t)); got != 1 {
		t.Errorf("bus saw %d frames, want the 1 from the create only", got)
	}
}

// TestCloseTab_AChatTabRunsTheTeardownAndKeepsTheRecord is what close_chat became.
// The × kills the work; the record survives, because a closed chat is a chat
// without a tab and reopening it session/loads everything back.
func TestCloseTab_AChatTabRunsTheTeardownAndKeepsTheRecord(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	var tornDown []vibekit.ChatID
	mem := NewMembership(&MembershipDeps{
		Chats: store, Tabs: st, Bus: &tabBus{},
		CloseChat: func(_ context.Context, id vibekit.ChatID) { tornDown = append(tornDown, id) },
	})
	opened := createChat(t, mem, "op-a")

	if _, _, err := mem.CloseTab(t.Context(), opened.Subject.ID, "op-close"); err != nil {
		t.Fatalf("CloseTab = %v", err)
	}

	if !slices.Equal(tornDown, []vibekit.ChatID{vibekit.ChatID(opened.Chat.ID)}) {
		t.Errorf("teardown ran for %v, want exactly the closed chat %q", tornDown, opened.Chat.ID)
	}
	if _, ok := store.Get(t.Context(), vibekit.ChatID(opened.Chat.ID)); !ok {
		t.Error("closing the tab deleted the chat record; only delete_chat may do that")
	}
}

// TestDeleteChatAndCloseTabs_TheRecordLeads is the delete half of the gate.
//
// The observation happens INSIDE the coordinator's critical section, via the
// store's own close hook, because that is the only place the intermediate state
// exists. Two things are asserted from there: the record is already gone, and an
// Open issued at that moment is refused — it queues on the operation lock, finds
// no chat when it runs, and leaves nothing behind.
func TestDeleteChatAndCloseTabs_TheRecordLeads(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, flaky, _ := newFlakyMembership(t, store)
	opened := createChat(t, mem, "op-a")
	chatID := vibekit.ChatID(opened.Chat.ID)

	var recordPresentAtClose bool
	var openErr error
	var openDone sync.WaitGroup
	flaky.setBeforeClose(func() {
		_, recordPresentAtClose = store.Get(context.Background(), chatID)
		openDone.Go(func() {
			_, openErr = mem.OpenTab(context.Background(),
				vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: string(chatID)}, "op-racer")
		})
	})

	if err := mem.DeleteChatAndCloseTabs(t.Context(), chatID, "op-del"); err != nil {
		t.Fatalf("DeleteChatAndCloseTabs = %v", err)
	}
	openDone.Wait()

	if recordPresentAtClose {
		t.Error("the chat record was still there when its tabs were closed; the record must lead on delete")
	}
	if statusOf(openErr) != http.StatusNotFound {
		t.Errorf("the racing open got status %d, want 404: once the record is gone every open is refused (%s)",
			statusOf(openErr), errText(openErr))
	}
	if got := tabIDsFor(flaky.Store, chatID); len(got) != 0 {
		t.Errorf("tabs for the deleted chat = %v, want none", got)
	}
}

// TestDeleteChatAndCloseTabs_RetriesAFailedCloseWithoutARestart is the gap the
// store alone cannot close. Prune only runs at load, so a tab close that fails
// after the record is gone would otherwise leave a live tab for a chat nothing can
// open until the process restarts.
func TestDeleteChatAndCloseTabs_RetriesAFailedCloseWithoutARestart(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, flaky, bus := newFlakyMembership(t, store)
	opened := createChat(t, mem, "op-a")
	chatID := vibekit.ChatID(opened.Chat.ID)
	flaky.failCloseOnce()

	if err := mem.DeleteChatAndCloseTabs(t.Context(), chatID, "op-del"); err != nil {
		t.Fatalf("DeleteChatAndCloseTabs = %v", err)
	}

	if got := tabIDsFor(flaky.Store, chatID); len(got) != 0 {
		t.Errorf("tabs for the deleted chat = %v, want none: the retry owes this in the same pass", got)
	}
	if got := bus.removedIDs(t); !slices.Contains(got, opened.Subject.ID) {
		t.Errorf("removed ids on the wire = %v, want the closed tab %q", got, opened.Subject.ID)
	}
}

// TestDeleteChatAndCloseTabs_AnnouncesTheRemovalEvenWhenTheCloseKeepsFailing is
// the second half of that rule. When the retry fails too, the removal is emitted
// anyway: the authoritative fact is that the chat is gone, and a client still
// showing the tab is showing a chat nothing can open.
//
// The frame is stamped one PAST the store's version, because a client discards
// anything at or below its local one — stamping the unchanged version would make
// the frame silently useless, which is the whole point of emitting it.
func TestDeleteChatAndCloseTabs_AnnouncesTheRemovalEvenWhenTheCloseKeepsFailing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, flaky, bus := newFlakyMembership(t, store)
	opened := createChat(t, mem, "op-a")
	chatID := vibekit.ChatID(opened.Chat.ID)
	_, versionBefore := flaky.List()
	flaky.openFails(0)
	flaky.setFailCloses(10)

	if err := mem.DeleteChatAndCloseTabs(t.Context(), chatID, "op-del"); err != nil {
		t.Fatalf("DeleteChatAndCloseTabs = %v", err)
	}

	frames := bus.frames(t)
	last := frames[len(frames)-1]
	if !slices.Equal(last.RemovedIDs, []string{opened.Subject.ID}) {
		t.Errorf("removed_ids = %v, want the tab whose close failed (%q)", last.RemovedIDs, opened.Subject.ID)
	}
	if last.Version != versionBefore+1 {
		t.Errorf("frame version = %d, want %d: a client ignores anything at or below its local version",
			last.Version, versionBefore+1)
	}
	if _, ok := store.Get(t.Context(), chatID); ok {
		t.Error("the chat record survived a delete; the record leads and is removed first")
	}
}

// TestRetentionClose_ClosesWhatThePredicateRaced. Retention skips a chat that has
// an open tab, so this path exists only for a tab opened between that check and the
// remove — and it resolves it in the same pass rather than at the next restart.
func TestRetentionClose_ClosesWhatThePredicateRaced(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	opened := createChat(t, mem, "op-a")
	chatID := vibekit.ChatID(opened.Chat.ID)
	// The reaper removed the file directly, so the record is already gone when the
	// hook fires.
	if err := store.Delete(t.Context(), chatID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	mem.RetentionClose(t.Context(), chatID)

	if got := tabIDsFor(st, chatID); len(got) != 0 {
		t.Errorf("tabs for the purged chat = %v, want none", got)
	}
	if got := bus.removedIDs(t); !slices.Contains(got, opened.Subject.ID) {
		t.Errorf("removed ids on the wire = %v, want the purged chat's tab %q", got, opened.Subject.ID)
	}
	if last := bus.frames(t); last[len(last)-1].OpID != "" {
		t.Errorf("the retention frame carries op_id %q, want none: no client asked for it", last[len(last)-1].OpID)
	}
}

// TestHasOpenTab_IsRetentionsSecondPredicate. Two facts, one for each direction,
// because a predicate that answered true for everything would also make retention
// opt-out and pass a one-sided test.
func TestHasOpenTab_IsRetentionsSecondPredicate(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)
	opened := createChat(t, mem, "op-a")
	closedChat := createChat(t, mem, "op-b")
	if _, _, err := mem.CloseTab(t.Context(), closedChat.Subject.ID, "op-close"); err != nil {
		t.Fatalf("CloseTab = %v", err)
	}

	cases := []struct {
		desc string
		chat vibekit.ChatID
		want bool
	}{
		{desc: "a chat with a tab on the strip is in use", chat: vibekit.ChatID(opened.Chat.ID), want: true},
		{desc: "a chat whose tab was closed is not", chat: vibekit.ChatID(closedChat.Chat.ID), want: false},
		{desc: "a chat that was never open is not", chat: "c-stranger", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			if got := mem.HasOpenTab(tc.chat); got != tc.want {
				t.Errorf("HasOpenTab(%q) = %v, want %v", tc.chat, got, tc.want)
			}
		})
	}
}

// TestReorderTabs_AcceptedWhileAnUnrelatedPinBumpedTheVersion is the drag an
// earlier revision would have discarded. The exact-set check is the whole
// precondition; a version precondition would refuse this, because a pin elsewhere
// bumps the version without changing the order.
func TestReorderTabs_AcceptedWhileAnUnrelatedPinBumpedTheVersion(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newTabbedMembership(t, store)
	a := createChat(t, mem, "op-a")
	b := createChat(t, mem, "op-b")
	c := createChat(t, mem, "op-c")

	// The gesture's own view of the set, taken BEFORE the unrelated mutation.
	dragged := []string{c.Subject.ID, a.Subject.ID, b.Subject.ID}
	pinnedVersion, err := mem.SetPinned(t.Context(), b.Subject.ID, true, "op-pin")
	if err != nil {
		t.Fatalf("SetPinned = %v", err)
	}

	version, err := mem.ReorderTabs(t.Context(), dragged, "op-drag")
	if err != nil {
		t.Fatalf("ReorderTabs after an unrelated pin = %v, want it ACCEPTED (%s)", err, errText(err))
	}

	if version != pinnedVersion+1 {
		t.Errorf("version went %d -> %d, want one bump", pinnedVersion, version)
	}
	open, _ := st.List()
	if got := subjectIDs(open); !slices.Equal(got, dragged) {
		t.Errorf("order = %v, want the arrangement the drag committed %v", got, dragged)
	}
}

// TestReorderTabs_RefusalShapes walks every way an order can fail to name the open
// set. All four are one sentinel and one status, because the client's answer to all
// of them is identical: re-list, never re-send.
func TestReorderTabs_RefusalShapes(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newTabbedMembership(t, store)
	a := createChat(t, mem, "op-a")
	b := createChat(t, mem, "op-b")
	c := createChat(t, mem, "op-c")
	want := []string{a.Subject.ID, b.Subject.ID, c.Subject.ID}
	_, versionBefore := st.List()

	cases := []struct {
		desc  string
		order []string
	}{
		{desc: "a short list, which is what a client that dropped a tab would send", order: []string{a.Subject.ID, b.Subject.ID}},
		{desc: "a long list naming a tab that is not open", order: []string{a.Subject.ID, b.Subject.ID, c.Subject.ID, "ghost"}},
		{desc: "an id that is not open", order: []string{a.Subject.ID, b.Subject.ID, "ghost"}},
		{desc: "the same id twice", order: []string{a.Subject.ID, b.Subject.ID, b.Subject.ID}},
		{desc: "an empty order against a non-empty set", order: nil},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			_, err := mem.ReorderTabs(t.Context(), tc.order, "op-drag")

			if statusOf(err) != http.StatusConflict {
				t.Fatalf("status = %d, want 409 (%s)", statusOf(err), errText(err))
			}
			if !errors.Is(err, tabs.ErrOrderMismatch) {
				t.Errorf("error = %v, want tabs.ErrOrderMismatch", err)
			}
			open, version := st.List()
			if got := subjectIDs(open); !slices.Equal(got, want) {
				t.Errorf("a refused reorder changed the order to %v, want %v unchanged", got, want)
			}
			if version != versionBefore {
				t.Errorf("a refused reorder moved the version to %d, want %d", version, versionBefore)
			}
		})
	}
}

// TestReorderTabs_AnIdenticalOrderCommitsNothing: a tab dragged back where it
// started is not news, so it bumps no version and emits no frame.
func TestReorderTabs_AnIdenticalOrderCommitsNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	a := createChat(t, mem, "op-a")
	b := createChat(t, mem, "op-b")
	_, before := st.List()
	framesBefore := len(bus.frames(t))

	version, err := mem.ReorderTabs(t.Context(), []string{a.Subject.ID, b.Subject.ID}, "op-drag")
	if err != nil {
		t.Fatalf("ReorderTabs = %v", err)
	}

	if version != before {
		t.Errorf("version went %d -> %d for an unchanged order", before, version)
	}
	if got := len(bus.frames(t)) - framesBefore; got != 0 {
		t.Errorf("an unchanged order emitted %d frames, want 0", got)
	}
}

// TestSetPinned_IdempotentInBothDirections, plus the refusal for an id that is not
// open — which a close does NOT report, because a pin is a statement about a tab
// and answering success would claim a tab that does not exist is pinned.
func TestSetPinned_IdempotentInBothDirections(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	a := createChat(t, mem, "op-a")

	first, err := mem.SetPinned(t.Context(), a.Subject.ID, true, "op-pin")
	if err != nil {
		t.Fatalf("SetPinned = %v", err)
	}
	again, err := mem.SetPinned(t.Context(), a.Subject.ID, true, "op-pin-again")
	if err != nil {
		t.Fatalf("repeat SetPinned = %v", err)
	}
	if again != first {
		t.Errorf("version went %d -> %d for a repeat pin, want it unchanged", first, again)
	}

	_, err = mem.SetPinned(t.Context(), "not-open", true, "op-pin-ghost")
	if statusOf(err) != http.StatusNotFound {
		t.Errorf("status = %d for a pin on an absent tab, want 404 (%s)", statusOf(err), errText(err))
	}

	frames := bus.frames(t)
	pins := 0
	for _, f := range frames {
		if f.Changed != nil && f.Changed.ID == a.Subject.ID && f.Changed.Pinned {
			pins++
		}
	}
	if pins != 1 {
		t.Errorf("saw %d pinned frames for %q, want exactly 1", pins, a.Subject.ID)
	}
	open, _ := st.List()
	if i := indexOfTab(open, a.Subject.ID); i < 0 || !open[i].Pinned {
		t.Errorf("the tab is not pinned in the store: %+v", open)
	}
}

// --- fixture helpers ---

// openFails makes the next n Opens fail. A method on the fixture rather than a
// field write at the call site so the mutex is not somebody else's business.
func (f *flakyTabs) openFails(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failOpens = n
}

func (f *flakyTabs) failCloseOnce() { f.setFailCloses(1) }

func (f *flakyTabs) setFailCloses(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failCloses = n
}

func (f *flakyTabs) setBeforeClose(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beforeClose = fn
}

// Open fails the first failOpens calls, which is how a create reaches the state
// its retry has to repair: chat written, tab not.
func (f *flakyTabs) Open(ctx context.Context, spec vibekit.OpenTab) (vibekit.TabSubject, bool, uint64, error) {
	f.mu.Lock()
	fail := f.failOpens > 0
	if fail {
		f.failOpens--
	}
	f.mu.Unlock()
	if fail {
		return vibekit.TabSubject{}, false, 0, errCloseFailed
	}
	return f.Store.Open(ctx, spec)
}

// seedRecord puts a chat in the store without opening a tab for it, which is what
// every chat the user has closed looks like.
//
// Named apart from rewind_test.go's seedChat deliberately: that one seeds a
// four-message transcript because a rewind needs turns to revert to, and these
// cases care only that the record exists.
func seedRecord(t *testing.T, store *testsupport.InMemoryChatStore, id vibekit.ChatID) {
	t.Helper()
	if err := store.Mutate(t.Context(), id, func(c *vibekit.Chat, _ bool) bool {
		c.Name = string(id)
		return true
	}); err != nil {
		t.Fatalf("seed %q: %v", id, err)
	}
}

// openChild opens a chat tab hanging under parentTab.
func openChild(t *testing.T, mem *Membership, store *testsupport.InMemoryChatStore, id vibekit.ChatID, parentTab string) TabOpened {
	t.Helper()
	seedRecord(t, store, id)
	opened, err := mem.OpenTab(t.Context(),
		vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: string(id), Parent: parentTab}, "op-child")
	if err != nil {
		t.Fatalf("open child %q: %v", id, err)
	}
	return opened
}

// fillTabs opens n EDITOR tabs, which need no chat record, so a capacity test is
// about capacity rather than about seeding chats.
func fillTabs(t *testing.T, mem *Membership, n int) {
	t.Helper()
	open, _ := mem.tabs.List()
	for i := len(open); i < n; i++ {
		if _, err := mem.OpenTab(t.Context(),
			vibekit.OpenTab{Kind: vibekit.TabKindEditor, Ref: fmt.Sprintf("/workspace/f%d.go", i)}, ""); err != nil {
			t.Fatalf("fill tab %d: %v", i, err)
		}
	}
}

// storedChatIDs is every chat the store holds.
func storedChatIDs(t *testing.T, store *testsupport.InMemoryChatStore) []vibekit.ChatID {
	t.Helper()
	var out []vibekit.ChatID
	for _, h := range store.List(t.Context()) {
		out = append(out, vibekit.ChatID(h.ID))
	}
	slices.Sort(out)
	return out
}
