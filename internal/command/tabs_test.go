package command

// The four tab commands at their DOOR: what a payload may say, and what status a
// refusal answers with.
//
// The ordering, the capacity reservation and the event are the coordinator's and
// are tested in membership_test.go. What is left here is the part a handler owns —
// the payload's shape, the identifier bounds, and the status each sentinel maps
// to — plus the two response fields whose absence would strand a client:
// `created` on an open and `closed` on a close.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// tabCmd builds a command envelope for one of the four tab types.
func tabCmd(t *testing.T, typ vibekit.CommandType, payload any) *vibekit.ClientCommand {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &vibekit.ClientCommand{Type: typ, Payload: raw}
}

// bodyField reads one field out of a command's success body. A Fatalf rather than
// a skip: a response missing the field the client resolves on is the defect.
func bodyField(t *testing.T, body any, key string) any {
	t.Helper()
	m, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("response is %T, want a map", body)
	}
	v, ok := m[key]
	if !ok {
		t.Fatalf("response has no %q: %+v", key, m)
	}
	return v
}

// TestCmdOpenTab_ReturnsTheSubjectAndTheCreatedFlag. `created` is the field that
// makes an idempotent open resolvable: it commits nothing, so it emits nothing,
// so a client waiting on the event would wait forever.
func TestCmdOpenTab_ReturnsTheSubjectAndTheCreatedFlag(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)
	seedRecord(t, store, "c-open")
	cmd := tabCmd(t, vibekit.CmdOpenTab, vibekit.OpenTabCommand{
		Kind: vibekit.TabKindChat, Ref: "c-open", OpID: "op-1",
	})

	first, err := CmdOpenTab(t.Context(), mem, cmd)
	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", statusOf(err), errText(err))
	}
	second, err := CmdOpenTab(t.Context(), mem, cmd)
	if statusOf(err) != http.StatusOK {
		t.Fatalf("repeat: status = %d, want 200 (%s)", statusOf(err), errText(err))
	}

	if got := bodyField(t, first, "created"); got != true {
		t.Errorf("first open reported created = %v, want true", got)
	}
	if got := bodyField(t, second, "created"); got != false {
		t.Errorf("repeat open reported created = %v, want false: it commits nothing and emits nothing", got)
	}
	subject, ok := bodyField(t, first, "subject").(vibekit.TabSubject)
	if !ok {
		t.Fatalf("subject is %T, want vibekit.TabSubject", bodyField(t, first, "subject"))
	}
	if subject.Kind != vibekit.TabKindChat || subject.Ref != "c-open" {
		t.Errorf("subject = %+v, want the (chat, c-open) tab", subject)
	}
}

// TestCmdOpenTab_PayloadRefusals is the door's own vocabulary. Each case is a
// 400: a kind the store cannot hold, a ref that is not a chat id, an id outside
// the identifier rule, and a singleton carrying a ref it has no meaning for.
func TestCmdOpenTab_PayloadRefusals(t *testing.T) {
	cases := []struct {
		desc    string
		payload vibekit.OpenTabCommand
		want    int
	}{
		{
			desc:    "a kind that is not one of the eight",
			payload: vibekit.OpenTabCommand{Kind: "plan", Ref: "c-a"},
			want:    http.StatusBadRequest,
		},
		{
			desc:    "a chat ref that is not a chat id",
			payload: vibekit.OpenTabCommand{Kind: vibekit.TabKindChat, Ref: "../etc/passwd"},
			want:    http.StatusBadRequest,
		},
		{
			desc:    "an op id with a path separator",
			payload: vibekit.OpenTabCommand{Kind: vibekit.TabKindSettings, OpID: "op/../x"},
			want:    http.StatusBadRequest,
		},
		{
			desc:    "a parent id outside the identifier rule",
			payload: vibekit.OpenTabCommand{Kind: vibekit.TabKindSettings, Parent: "a/b"},
			want:    http.StatusBadRequest,
		},
		{
			// The store's own refusal, surfaced through the handler's mapping: a
			// singleton takes no ref.
			desc:    "a singleton carrying a ref",
			payload: vibekit.OpenTabCommand{Kind: vibekit.TabKindSettings, Ref: "/workspace/x.go"},
			want:    http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			mem, st, bus := newTabbedMembership(t, store)

			_, err := CmdOpenTab(t.Context(), mem, tabCmd(t, vibekit.CmdOpenTab, tc.payload))

			if statusOf(err) != tc.want {
				t.Errorf("status = %d, want %d (%s)", statusOf(err), tc.want, errText(err))
			}
			if open, _ := st.List(); len(open) != 0 {
				t.Errorf("a refused open left %d tabs behind, want none", len(open))
			}
			if got := len(bus.frames(t)); got != 0 {
				t.Errorf("a refused open emitted %d frames, want none", got)
			}
		})
	}
}

// TestCmdOpenTab_ForAMissingChatIs404. The delete-ordering gate seen from the
// command boundary: a 404 is what tells the client to drop the row rather than
// retry it.
func TestCmdOpenTab_ForAMissingChatIs404(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)

	_, err := CmdOpenTab(t.Context(), mem, tabCmd(t, vibekit.CmdOpenTab,
		vibekit.OpenTabCommand{Kind: vibekit.TabKindChat, Ref: "c-gone"}))

	if statusOf(err) != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (%s)", statusOf(err), errText(err))
	}
}

// TestCmdCloseTab_ReturnsEveryClosedID. The response is a LIST because a parent
// and its children go as one mutation, and it is empty rather than an error for
// an id that is not open.
func TestCmdCloseTab_ReturnsEveryClosedID(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)
	parent := createChat(t, mem, "op-parent")
	child := openChild(t, mem, store, "c-child", parent.Subject.ID)

	body, err := CmdCloseTab(t.Context(), mem, tabCmd(t, vibekit.CmdCloseTab,
		vibekit.CloseTabCommand{ID: parent.Subject.ID, OpID: "op-close"}))
	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", statusOf(err), errText(err))
	}

	closed, ok := bodyField(t, body, "closed").([]string)
	if !ok {
		t.Fatalf("closed is %T, want []string", bodyField(t, body, "closed"))
	}
	if len(closed) != 2 {
		t.Errorf("closed = %v, want both the parent %q and its child %q", closed, parent.Subject.ID, child.Subject.ID)
	}

	again, err := CmdCloseTab(t.Context(), mem, tabCmd(t, vibekit.CmdCloseTab,
		vibekit.CloseTabCommand{ID: parent.Subject.ID}))
	if statusOf(err) != http.StatusOK {
		t.Fatalf("closing twice: status = %d, want 200: two devices can close one tab (%s)",
			statusOf(err), errText(err))
	}
	if got, _ := bodyField(t, again, "closed").([]string); len(got) != 0 {
		t.Errorf("the second close reported %v closed, want nothing", got)
	}
}

// TestCmdReorderTabs_EveryRefusalShapeIs409 is the status half of the exact-set
// contract. One sentinel, one status, because the client's answer to all of them
// is the same: re-list, never re-send.
func TestCmdReorderTabs_EveryRefusalShapeIs409(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newTabbedMembership(t, store)
	a := createChat(t, mem, "op-a")
	b := createChat(t, mem, "op-b")

	cases := []struct {
		desc  string
		order []string
	}{
		{desc: "a short list", order: []string{a.Subject.ID}},
		{desc: "an id that is not open", order: []string{a.Subject.ID, "ghost"}},
		{desc: "the same id twice", order: []string{a.Subject.ID, a.Subject.ID}},
		{desc: "a list longer than the open set", order: []string{a.Subject.ID, b.Subject.ID, "extra"}},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			_, err := CmdReorderTabs(t.Context(), mem, tabCmd(t, vibekit.CmdReorderTabs,
				vibekit.ReorderTabsCommand{Order: tc.order, OpID: "op-drag"}))

			if statusOf(err) != http.StatusConflict {
				t.Errorf("status = %d, want 409 (%s)", statusOf(err), errText(err))
			}
		})
	}

	t.Run("a valid order is accepted and reports its version", func(t *testing.T) {
		_, before := st.List()
		body, err := CmdReorderTabs(t.Context(), mem, tabCmd(t, vibekit.CmdReorderTabs,
			vibekit.ReorderTabsCommand{Order: []string{b.Subject.ID, a.Subject.ID}, OpID: "op-drag"}))
		if statusOf(err) != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", statusOf(err), errText(err))
		}
		if got := bodyField(t, body, "version"); got != before+1 {
			t.Errorf("version = %v, want %d", got, before+1)
		}
	})

	t.Run("an order longer than the decode bound is 413", func(t *testing.T) {
		order := make([]string, tabs.MaxTabs+1)
		for i := range order {
			order[i] = "x"
		}
		_, err := CmdReorderTabs(t.Context(), mem, tabCmd(t, vibekit.CmdReorderTabs,
			vibekit.ReorderTabsCommand{Order: order}))

		if statusOf(err) != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413 (%s)", statusOf(err), errText(err))
		}
	})
}

// TestCmdPinTab_RefusesAnAbsentTab. Unlike a close, a pin on a tab that is not
// open is a 404: a pin is a statement ABOUT a tab, so success would claim
// something that does not exist is pinned.
func TestCmdPinTab_RefusesAnAbsentTab(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)
	opened := createChat(t, mem, "op-a")

	cases := []struct {
		desc    string
		payload vibekit.PinTabCommand
		want    int
	}{
		{desc: "the open tab", payload: vibekit.PinTabCommand{ID: opened.Subject.ID, Pinned: true}, want: http.StatusOK},
		{desc: "a tab that is not open", payload: vibekit.PinTabCommand{ID: "ghost", Pinned: true}, want: http.StatusNotFound},
		{desc: "an empty id", payload: vibekit.PinTabCommand{Pinned: true}, want: http.StatusBadRequest},
		{desc: "an id outside the identifier rule", payload: vibekit.PinTabCommand{ID: "a/b", Pinned: true}, want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			_, err := CmdPinTab(t.Context(), mem, tabCmd(t, vibekit.CmdPinTab, tc.payload))

			if statusOf(err) != tc.want {
				t.Errorf("status = %d, want %d (%s)", statusOf(err), tc.want, errText(err))
			}
		})
	}
}

// TestTabCommands_AnUnwiredStoreIs503 is the shape a build with no config dir
// has. Answered rather than swallowed: a command whose effect did not happen must
// not report success.
func TestTabCommands_AnUnwiredStoreIs503(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem := newTestMembership(t, store)
	seedRecord(t, store, "c-a")

	cases := []struct {
		desc string
		call func() (any, error)
	}{
		{desc: "open", call: func() (any, error) {
			return CmdOpenTab(t.Context(), mem, tabCmd(t, vibekit.CmdOpenTab,
				vibekit.OpenTabCommand{Kind: vibekit.TabKindChat, Ref: "c-a"}))
		}},
		{desc: "close", call: func() (any, error) {
			return CmdCloseTab(t.Context(), mem, tabCmd(t, vibekit.CmdCloseTab, vibekit.CloseTabCommand{ID: "t1"}))
		}},
		{desc: "reorder", call: func() (any, error) {
			return CmdReorderTabs(t.Context(), mem, tabCmd(t, vibekit.CmdReorderTabs,
				vibekit.ReorderTabsCommand{Order: []string{"t1"}}))
		}},
		{desc: "pin", call: func() (any, error) {
			return CmdPinTab(t.Context(), mem, tabCmd(t, vibekit.CmdPinTab, vibekit.PinTabCommand{ID: "t1"}))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			_, err := tc.call()

			if statusOf(err) != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503 (%s)", statusOf(err), errText(err))
			}
		})
	}
}

// TestCmdCreateChat_RetryFinishesTheTabWrite is the requirement at the command
// boundary rather than at the coordinator's: one gesture, one chat, and the
// retry's RESPONSE carries the subject a client needs to render.
//
// The first attempt's tab write is made to fail, which is the only way to reach
// the state — and a state a real deployment reaches on a full disk.
func TestCmdCreateChat_RetryFinishesTheTabWrite(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, flaky, _ := newFlakyMembership(t, store)
	flaky.openFails(1)
	req := func() *vibekit.ClientCommand {
		return createReq(t, "", vibekit.CreateChatCommand{OpID: "op-retry", Name: "Half made"})
	}

	if _, err := CmdCreateChat(t.Context(), mem, req()); err == nil {
		t.Fatal("the first attempt reported success, so the fixture injected nothing")
	}
	body, err := CmdCreateChat(t.Context(), mem, req())
	if statusOf(err) != http.StatusOK {
		t.Fatalf("the retry: status = %d, want 200 (%s)", statusOf(err), errText(err))
	}

	id := chatIDOfResponse(t, body)
	if got := storedChatIDs(t, store); len(got) != 1 || got[0] != id {
		t.Errorf("store holds %v, want exactly the returned chat %q", got, id)
	}
	subject, ok := bodyField(t, body, "subject").(vibekit.TabSubject)
	if !ok {
		t.Fatalf("subject is %T, want vibekit.TabSubject", bodyField(t, body, "subject"))
	}
	if subject.Ref != string(id) {
		t.Errorf("the retry's subject refers to %q, want the chat it answered with %q", subject.Ref, id)
	}
	if got := tabIDsFor(flaky.Store, id); len(got) != 1 || got[0] != subject.ID {
		t.Errorf("tabs for the chat = %v, want exactly the returned subject %q", got, subject.ID)
	}
}

// TestCmdCreateChat_AtTheLimitLeavesNoOrphan is the reservation seen through the
// command: the refusal is a 409 naming the remedy, and the chat store is
// untouched.
func TestCmdCreateChat_AtTheLimitLeavesNoOrphan(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)
	fillTabs(t, mem, tabs.MaxOpenTabs)
	before := storedChatIDs(t, store)

	_, err := CmdCreateChat(t.Context(), mem, createReq(t, "", vibekit.CreateChatCommand{OpID: "op-full"}))

	if statusOf(err) != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", statusOf(err), errText(err))
	}
	if !strings.Contains(errText(err), "close a tab first") {
		t.Errorf("the refusal reads %q, want it to name the remedy", errText(err))
	}
	if got := storedChatIDs(t, store); len(got) != len(before) {
		t.Errorf("store holds %d chats, want the %d it held: a refused create must leave no orphan", len(got), len(before))
	}
}
