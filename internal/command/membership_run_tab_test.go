package command

// A run's tab: the parent the coordinator fills in, and the automatic open.
//
// Both exist because a run tab's PARENT is immutable after open, so getting it
// wrong is permanent and used to differ per device — the launching chat was known
// only to whichever client happened to hold the run's frames. The coordinator
// answers it from the run's own lease instead, once, for every door.

import (
	"errors"
	"net/http"
	"testing"

	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// fakeRunOwner answers which chat launched a run, as *agent.Runs does off the
// lease. `ok` and an empty chat id are DIFFERENT answers — a parentless run has a
// lease and no chat, a released one has neither — so both are expressible.
type fakeRunOwner struct {
	chats map[string]vibekit.ChatID
	// known is the lease's existence, independent of the chat id.
	known map[string]bool
}

func (f *fakeRunOwner) RunChat(workflowID string) (vibekit.ChatID, bool) {
	if f.known != nil && !f.known[workflowID] {
		return "", false
	}
	chatID, ok := f.chats[workflowID]
	if !ok && f.known == nil {
		return "", false
	}
	return chatID, true
}

// newRunTabMembership builds a coordinator over a real tab store with a run
// owner wired, which is the production shape.
func newRunTabMembership(t *testing.T, chats ChatStore, runs RunOwner) (*Membership, *tabs.Store, *tabBus) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	bus := &tabBus{}
	return NewMembership(&MembershipDeps{Chats: chats, Tabs: st, Bus: bus, Runs: runs}), st, bus
}

// runTabParent is the Parent the store holds for a run's tab.
func runTabParent(t *testing.T, st *tabs.Store, workflowID string) string {
	t.Helper()
	open, _ := st.List()
	for _, tab := range open {
		if tab.Kind == vibekit.TabKindRun && tab.Ref == workflowID {
			return tab.Parent
		}
	}
	t.Fatalf("no run tab for %q in %v", workflowID, open)
	return ""
}

// TestOpenTab_FillsARunsParentFromItsLease is symptom B: a run tab opened by a
// door that holds no chat id — a `/run/{id}` deep link on a fresh browser, a
// back press, another device's open — used to land top level and stay there for
// good, because Parent is set once.
func TestOpenTab_FillsARunsParentFromItsLease(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	runs := &fakeRunOwner{chats: map[string]vibekit.ChatID{"wf_1": "c-launcher"}}
	mem, st, _ := newRunTabMembership(t, store, runs)
	seedRecord(t, store, "c-launcher")
	chatTab, err := mem.OpenTab(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-launcher"}, "op-chat")
	if err != nil {
		t.Fatalf("open the launching chat's tab: %v", err)
	}

	opened, err := mem.OpenTab(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindRun, Ref: "wf_1"}, "op-run")
	if err != nil {
		t.Fatalf("OpenTab(run) = %v", err)
	}

	if opened.Subject.Parent != chatTab.Subject.ID {
		t.Errorf("run tab parent = %q, want the launching chat's tab %q",
			opened.Subject.Parent, chatTab.Subject.ID)
	}
	if got := runTabParent(t, st, "wf_1"); got != chatTab.Subject.ID {
		t.Errorf("the STORED parent = %q, want %q; Parent is never reassigned, so this is the only chance",
			got, chatTab.Subject.ID)
	}
}

// TestOpenTab_AClientSuppliedParentWins is why the fill is a fallback rather
// than an override: History names the parent of a FINISHED run, whose lease has
// been released, so the coordinator has nothing to answer with there.
func TestOpenTab_AClientSuppliedParentWins(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	runs := &fakeRunOwner{chats: map[string]vibekit.ChatID{"wf_1": "c-lease"}}
	mem, st, _ := newRunTabMembership(t, store, runs)
	seedRecord(t, store, "c-lease")
	seedRecord(t, store, "c-explicit")
	if _, err := mem.OpenTab(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-lease"}, "op-a"); err != nil {
		t.Fatalf("open c-lease: %v", err)
	}
	explicit, err := mem.OpenTab(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-explicit"}, "op-b")
	if err != nil {
		t.Fatalf("open c-explicit: %v", err)
	}

	if _, err := mem.OpenTab(t.Context(), vibekit.OpenTab{
		Kind:   vibekit.TabKindRun,
		Ref:    "wf_1",
		Parent: explicit.Subject.ID,
	}, "op-run"); err != nil {
		t.Fatalf("OpenTab(run) = %v", err)
	}

	if got := runTabParent(t, st, "wf_1"); got != explicit.Subject.ID {
		t.Errorf("parent = %q, want the caller's %q rather than the lease's", got, explicit.Subject.ID)
	}
}

// TestOpenTab_LeavesAParentlessRunTopLevel is the case the fix must NOT break: a
// manual or scheduled run has a lease and no launching chat, so it belongs at the
// top of the strip exactly as before.
func TestOpenTab_LeavesAParentlessRunTopLevel(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	for name, runs := range map[string]RunOwner{
		"a parentless run's lease carries no chat": &fakeRunOwner{
			chats: map[string]vibekit.ChatID{"wf_1": ""},
			known: map[string]bool{"wf_1": true},
		},
		"a finished run has no lease at all": &fakeRunOwner{known: map[string]bool{}},
		"no run owner is wired":              nil,
	} {
		t.Run(name, func(t *testing.T) {
			mem, st, _ := newRunTabMembership(t, store, runs)
			if _, err := mem.OpenTab(t.Context(),
				vibekit.OpenTab{Kind: vibekit.TabKindRun, Ref: "wf_1"}, "op-run"); err != nil {
				t.Fatalf("OpenTab(run) = %v", err)
			}
			if got := runTabParent(t, st, "wf_1"); got != "" {
				t.Errorf("parent = %q, want top level", got)
			}
		})
	}
}

// TestOpenTab_FillsNoParentForAnyOtherKind pins the fill to run tabs. A chat's
// nesting is the tangent's own two-field opt-in and an editor's is nobody's, so a
// fill reaching them would invent a hierarchy no door asked for.
func TestOpenTab_FillsNoParentForAnyOtherKind(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	// A run owner that would answer for ANY ref, so a kind leaking through the
	// gate is visible rather than merely unproven.
	runs := &fakeRunOwner{chats: map[string]vibekit.ChatID{"/workspace/a.go": "c-launcher", "c-other": "c-launcher"}}
	mem, st, _ := newRunTabMembership(t, store, runs)
	seedRecord(t, store, "c-launcher")
	seedRecord(t, store, "c-other")
	if _, err := mem.OpenTab(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-launcher"}, "op-a"); err != nil {
		t.Fatalf("open c-launcher: %v", err)
	}

	for _, spec := range []vibekit.OpenTab{
		{Kind: vibekit.TabKindEditor, Ref: "/workspace/a.go"},
		{Kind: vibekit.TabKindChat, Ref: "c-other"},
	} {
		opened, err := mem.OpenTab(t.Context(), spec, "op-"+spec.Ref)
		if err != nil {
			t.Fatalf("OpenTab(%s) = %v", spec.Kind, err)
		}
		if opened.Subject.Parent != "" {
			t.Errorf("%s tab parent = %q, want top level", spec.Kind, opened.Subject.Parent)
		}
	}
	if open, _ := st.List(); len(open) != 3 {
		t.Errorf("the set holds %d tabs, want 3", len(open))
	}
}

// TestOpenRunTab_NestsUnderTheLaunchingChatAsAView is symptom A: the tab a
// starting run gets, opened once by the server so it reaches every device rather
// than only the one holding the launching chat's tab when a frame arrived.
func TestOpenRunTab_NestsUnderTheLaunchingChatAsAView(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newRunTabMembership(t, store, &fakeRunOwner{})
	seedRecord(t, store, "c-launcher")
	chatTab, err := mem.OpenTab(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-launcher"}, "op-chat")
	if err != nil {
		t.Fatalf("open the launching chat's tab: %v", err)
	}

	opened, err := mem.OpenRunTab(t.Context(), "wf_1", "c-launcher", "")
	if err != nil {
		t.Fatalf("OpenRunTab = %v", err)
	}

	if !opened.Created {
		t.Error("the first automatic open reported created:false")
	}
	if opened.Subject.Parent != chatTab.Subject.ID {
		t.Errorf("parent = %q, want the launching chat's tab %q", opened.Subject.Parent, chatTab.Subject.ID)
	}
	if opened.Subject.Owns {
		t.Error("owns = true: a run tab is a VIEW, so its × must stop nothing")
	}
	if got := runTabParent(t, st, "wf_1"); got != chatTab.Subject.ID {
		t.Errorf("the STORED parent = %q, want %q", got, chatTab.Subject.ID)
	}
	// One frame for the chat's tab, one for the run's: the automatic open is a
	// committed mutation like any other, and that frame is how it reaches every
	// other device.
	if got := len(bus.frames(t)); got != 2 {
		t.Errorf("bus saw %d frames, want 2", got)
	}
}

// TestOpenRunTab_RefusesWhenTheLaunchingChatHasNoTab is the refusal the retry
// depends on. It has to be DISTINGUISHABLE from the capacity refusal, because the
// two mean opposite things to the caller: try again on the next step, versus stop.
//
// Opening top level instead would be worse than refusing: Parent is immutable, so
// it would foreclose nesting for the life of the run.
func TestOpenRunTab_RefusesWhenTheLaunchingChatHasNoTab(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newRunTabMembership(t, store, &fakeRunOwner{})
	// The record exists; only its TAB does not — a background chat on another
	// device's arrangement.
	seedRecord(t, store, "c-closed")

	_, err := mem.OpenRunTab(t.Context(), "wf_1", "c-closed", "")

	if !errors.Is(err, ErrNoParentTab) {
		t.Fatalf("err = %v, want ErrNoParentTab", err)
	}
	if errors.Is(err, errTabsFull) {
		t.Error("the no-parent refusal is indistinguishable from the capacity one")
	}
	if open, _ := st.List(); len(open) != 0 {
		t.Errorf("the refused open left %v behind, want nothing", open)
	}
	if got := len(bus.frames(t)); got != 0 {
		t.Errorf("bus saw %d frames, want none: nothing committed", got)
	}
}

// TestOpenRunTab_ReservesTheLastSlotForTheReader is the capacity rule. Creating a
// chat opens a tab, so at MaxOpenTabs New chat stops working — and a tab the app
// opened by itself must not be what consumes the last slot.
func TestOpenRunTab_ReservesTheLastSlotForTheReader(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newRunTabMembership(t, store, &fakeRunOwner{})
	seedRecord(t, store, "c-launcher")
	if _, err := mem.OpenTab(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-launcher"}, "op-chat"); err != nil {
		t.Fatalf("open the launching chat's tab: %v", err)
	}
	fillTabs(t, mem, tabs.MaxOpenTabs-1)

	_, err := mem.OpenRunTab(t.Context(), "wf_1", "c-launcher", "")

	if statusOf(err) != http.StatusConflict {
		t.Fatalf("status = %d, want 409 one slot short of the limit (%s)", statusOf(err), errText(err))
	}
	if open, _ := st.List(); len(open) != tabs.MaxOpenTabs-1 {
		t.Errorf("the set holds %d tabs, want %d: nothing was opened", len(open), tabs.MaxOpenTabs-1)
	}
	// The slot the refusal held back is spendable by the reader's own gesture,
	// which is the whole point of holding it.
	if _, err := mem.CreateChatAndOpen(t.Context(), ChatCreate{
		OpID: "op-new-chat",
		Init: func(c *vibekit.Chat) { c.Name = vibekit.DefaultChatName },
	}); err != nil {
		t.Errorf("New chat = %v, want it to still work at MaxOpenTabs-1", err)
	}
}

// TestOpenRunTab_AnAlreadyOpenRunIsExemptFromTheReservation matches reserveSlot's
// own exemption for a create's retry: an idempotent repeat mints nothing, so
// refusing it at the limit would strand a run whose tab is already there.
func TestOpenRunTab_AnAlreadyOpenRunIsExemptFromTheReservation(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newRunTabMembership(t, store, &fakeRunOwner{})
	seedRecord(t, store, "c-launcher")
	if _, err := mem.OpenTab(t.Context(), vibekit.OpenTab{Kind: vibekit.TabKindChat, Ref: "c-launcher"}, "op-chat"); err != nil {
		t.Fatalf("open the launching chat's tab: %v", err)
	}
	first, err := mem.OpenRunTab(t.Context(), "wf_1", "c-launcher", "")
	if err != nil {
		t.Fatalf("first OpenRunTab = %v", err)
	}
	fillTabs(t, mem, tabs.MaxOpenTabs)

	second, err := mem.OpenRunTab(t.Context(), "wf_1", "c-launcher", "")
	if err != nil {
		t.Fatalf("repeat OpenRunTab at the limit = %v, want the open tab back", err)
	}
	if second.Created {
		t.Error("the repeat reported created:true; one (kind, ref) is one tab")
	}
	if second.Subject.ID != first.Subject.ID {
		t.Errorf("repeat returned %q, want the open tab %q", second.Subject.ID, first.Subject.ID)
	}
	if open, _ := st.List(); len(open) != tabs.MaxOpenTabs {
		t.Errorf("the set holds %d tabs, want %d", len(open), tabs.MaxOpenTabs)
	}
}

// TestOpenRunTab_WithNoTabStoreIsReportedRatherThanPanicking: a build with no
// config dir has no set to open into, and the caller logs rather than retrying.
func TestOpenRunTab_WithNoTabStoreIsReportedRatherThanPanicking(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem := NewMembership(&MembershipDeps{Chats: store})

	_, err := mem.OpenRunTab(t.Context(), "wf_1", "c-launcher", "")

	if !errors.Is(err, ErrTabsUnavailable) {
		t.Errorf("err = %v, want ErrTabsUnavailable", err)
	}
	if errors.Is(err, ErrNoParentTab) {
		t.Error("an unwired store reads as a retryable no-parent refusal")
	}
}
