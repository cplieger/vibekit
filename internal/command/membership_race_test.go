package command

// The coordinator under concurrency. These are the cases the operation lock
// exists for, and each asserts an INVARIANT rather than an outcome, because the
// outcome legitimately depends on who won.
//
// Run under -race. The gate command for this package is
// `go test -count=1 -race ./internal/command/`, and every case here is designed
// to fail loudly rather than flakily: the assertions are consistency properties
// that hold for every interleaving, so a pass is not a scheduling accident.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestMembership_ConcurrentCreateAndDeleteOfOneChat asserts the pair the two
// stores must agree on, and asserts it as a BICONDITIONAL rather than as an
// expected winner: the chat exists if and only if it has a tab.
//
// Both halves matter and they are different defects. A chat with no tab is
// unreachable — nothing can open it and no client can see it. A tab with no chat
// is a row that can only fail, and it survives until the next restart because
// Prune runs at load.
//
// The create and the delete are genuinely racing (no handoff, no sleep), and the
// loop runs enough iterations that the interleaving varies. A test that named a
// winner would be pinning the scheduler.
func TestMembership_ConcurrentCreateAndDeleteOfOneChat(t *testing.T) {
	for i := range 40 {
		store := testsupport.NewInMemoryChatStore()
		mem, st, _ := newRacedMembership(t, store)
		// The delete needs an id, so the chat is created first and the RACE is a
		// second create of the same op against the delete of what it produced.
		first := createChat(t, mem, "op-race")
		chatID := vibekit.ChatID(first.Chat.ID)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = mem.CreateChatAndOpen(t.Context(), ChatCreate{
				OpID: "op-race", Init: func(c *vibekit.Chat) { c.Name = "racer" },
			})
		}()
		go func() {
			defer wg.Done()
			_ = mem.DeleteChatAndCloseTabs(t.Context(), chatID, "op-del")
		}()
		wg.Wait()

		_, chatExists := store.Get(t.Context(), chatID)
		hasTab := len(tabIDsFor(st, chatID)) > 0
		if chatExists != hasTab {
			t.Fatalf("iteration %d: chat exists = %v but has a tab = %v; the two stores must agree",
				i, chatExists, hasTab)
		}
	}
}

// TestMembership_TwoOpensRaceForTheFinalSlot. Exactly one wins, the loser gets
// the product limit's 409, and the set lands EXACTLY at MaxOpenTabs.
//
// The last assertion is the one that would catch a check-then-act reservation:
// two opens that both read "47 open, room for one" would both mint, leaving 49
// tabs in a collection whose whole limit is 48.
func TestMembership_TwoOpensRaceForTheFinalSlot(t *testing.T) {
	for i := range 20 {
		store := testsupport.NewInMemoryChatStore()
		mem, st, _ := newRacedMembership(t, store)
		fillTabs(t, mem, tabs.MaxOpenTabs-1)
		seedRecord(t, store, "c-alpha")
		seedRecord(t, store, "c-beta")

		var wg sync.WaitGroup
		errs := make([]error, 2)
		for j, ref := range []string{"c-alpha", "c-beta"} {
			wg.Go(func() {
				_, errs[j] = mem.OpenTab(t.Context(),
					vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: ref}, "op-race")
			})
		}
		wg.Wait()

		won := 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case errors.Is(err, errTabsFull):
			default:
				t.Fatalf("iteration %d: an open failed with %v, want either success or errTabsFull", i, err)
			}
		}
		if won != 1 {
			t.Fatalf("iteration %d: %d of 2 opens won the final slot, want exactly 1", i, won)
		}
		if open, _ := st.List(); len(open) != tabs.MaxOpenTabs {
			t.Fatalf("iteration %d: the set holds %d tabs, want exactly %d: the limit is the limit",
				i, len(open), tabs.MaxOpenTabs)
		}
	}
}

// TestMembership_ADeleteWhoseTabCloseFailsRetriesUnderRace is the live-repair
// rule under concurrency: the retry must land in the SAME pass, so no tab for a
// deleted chat survives the call, whatever else is running.
//
// The concurrent open is what makes it a race rather than the sequential case:
// it either wins (its tab is then closed by the delete's own walk) or is refused
// (the record is already gone). Either way the end state is the same, which is
// the property.
func TestMembership_ADeleteWhoseTabCloseFailsRetriesUnderRace(t *testing.T) {
	for i := range 20 {
		store := testsupport.NewInMemoryChatStore()
		mem, flaky, _ := newFlakyMembership(t, store)
		opened := createChat(t, mem, "op-a")
		chatID := vibekit.ChatID(opened.Chat.ID)
		flaky.failCloseOnce()

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = mem.DeleteChatAndCloseTabs(t.Context(), chatID, "op-del")
		}()
		go func() {
			defer wg.Done()
			_, _ = mem.OpenTab(t.Context(),
				vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: string(chatID)}, "op-open")
		}()
		wg.Wait()

		if _, ok := store.Get(t.Context(), chatID); ok {
			t.Fatalf("iteration %d: the chat record survived its delete", i)
		}
		if got := tabIDsFor(flaky.Store, chatID); len(got) != 0 {
			t.Fatalf("iteration %d: tabs %v for a deleted chat survived the pass; the retry owes this without a restart",
				i, got)
		}
	}
}

// newRacedMembership is newTabbedMembership plus the teardown seam the delete
// path needs. Separate from newFlakyMembership because these cases want the REAL
// store's behaviour, failure injection included only where a case asks for it.
func newRacedMembership(t *testing.T, chats ChatStore) (*Membership, *tabs.Store, *tabBus) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	bus := &tabBus{}
	return NewMembership(MembershipDeps{
		Chats: chats, Tabs: st, Bus: bus, Teardown: &recordingTeardown{},
	}), st, bus
}

// hookedChats runs a hook the FIRST time Get is called for a chat, from inside
// the coordinator's critical section.
//
// It is what makes the lock's effect observable rather than sampled: an
// interleaving that depends on two goroutines hitting a window measured in
// nanoseconds is not something a test can schedule, and a loop that hopes for it
// passes for the wrong reason far more often than it fails.
type hookedChats struct {
	*testsupport.InMemoryChatStore
	hook func()
	on   vibekit.ChatID
	once sync.Once
}

func (h *hookedChats) Get(ctx context.Context, id vibekit.ChatID) (*vibekit.Chat, bool) {
	c, ok := h.InMemoryChatStore.Get(ctx, id)
	if id == h.on {
		h.once.Do(h.hook)
	}
	return c, ok
}

// TestMembership_ADeleteCannotInterleaveWithACreate holds the operation lock to
// its purpose, deterministically.
//
// The hook fires INSIDE CreateChatAndOpen, between its record read and its tab
// write — the one window where a delete could leave a tab whose chat is gone. It
// starts the delete and waits a bounded time for it to finish. With the lock the
// delete cannot start, so the wait expires and the create completes against a
// record that is still there; without it the delete lands in the window and the
// create then mints a tab for a chat nothing can open.
//
// The wait FAILS CLOSED: expiring is the passing path, so a slow machine cannot
// turn this into a false failure — it can only cost the wait.
func TestMembership_ADeleteCannotInterleaveWithACreate(t *testing.T) {
	inner := testsupport.NewInMemoryChatStore()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	chats := &hookedChats{InMemoryChatStore: inner}
	mem := NewMembership(MembershipDeps{
		Chats: chats, Tabs: st, Bus: &tabBus{}, Teardown: &recordingTeardown{},
	})
	first := createChat(t, mem, "op-race")
	chatID := vibekit.ChatID(first.Chat.ID)

	deleted := make(chan struct{})
	chats.on = chatID
	chats.hook = func() {
		go func() {
			defer close(deleted)
			_ = mem.DeleteChatAndCloseTabs(context.Background(), chatID, "op-del")
		}()
		// Bounded: with the lock held this expires, which is the correct outcome.
		select {
		case <-deleted:
		case <-time.After(300 * time.Millisecond):
		}
	}

	// A repeat of the same op, so it resolves to the chat above and reaches the
	// hook with the record present.
	opened, err := mem.CreateChatAndOpen(t.Context(), ChatCreate{
		OpID: "op-race", Init: func(c *vibekit.Chat) { c.Name = "racer" },
	})
	if err != nil {
		t.Fatalf("the replayed create = %v, want it to succeed", err)
	}
	<-deleted

	if _, ok := inner.Get(t.Context(), chatID); ok {
		t.Fatal("the chat record survived its delete")
	}
	if got := tabIDsFor(st, chatID); len(got) != 0 {
		t.Errorf("tabs %v survived for a deleted chat (the create returned subject %q); the operation "+
			"lock is what stops a delete landing between a create's record read and its tab write",
			got, opened.Subject.ID)
	}
}
