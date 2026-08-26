package command

// Server-minted chat ids, and the idempotency that has to replace what they took
// away.
//
// While the CLIENT chose the id, a retry carried the same id and every creating
// handler's `if exists { return false }` made the second attempt a no-op — a free,
// structural idempotency nobody had to implement. Minting server-side removes it:
// a retry mints again, so one gesture produces two chats. The Idempotency-Key
// header covers a retry inside its 5-minute cache; the op ledger covers the
// fall-through past it, which is where a user pressing Retry on a failure toast
// lands.
//
// These tests drive the handler directly and read the STORE, because "one chat and
// not two" is a fact about the store rather than about a response body.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// createReq builds a create_chat envelope. chatID is normally EMPTY — that
// absence is what tells the handler to mint.
func createReq(t *testing.T, chatID vibekit.ChatID, p vibekit.CreateChatCommand) *vibekit.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &vibekit.ClientCommand{Type: vibekit.CmdCreateChat, ChatID: chatID, Payload: payload}
}

// chatIDOfResponse reads the id out of a create's reply. The reply is what makes
// server minting workable at all, so a test that only inspected the store would
// pass with the chat returned to nobody.
func chatIDOfResponse(t *testing.T, body any) vibekit.ChatID {
	t.Helper()
	m, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("response is %T, want a map carrying the chat", body)
	}
	h, ok := m["chat"].(vibekit.ChatHeader)
	if !ok {
		t.Fatalf("response has no chat header: %+v", m)
	}
	return vibekit.ChatID(h.ID)
}

// storedIDs is every chat the store holds, which is the population "one chat, not
// two" is a claim about.
func storedIDs(t *testing.T, store *testsupport.InMemoryChatStore) []vibekit.ChatID {
	t.Helper()
	var out []vibekit.ChatID
	for _, h := range store.List(t.Context()) {
		out = append(out, vibekit.ChatID(h.ID))
	}
	return out
}

func TestCmdCreateChat_MintsAndReturns(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := newTestHost(t, store)

	body, err := CmdCreateChat(t.Context(), newTestMembership(t, host),
		createReq(t, "", vibekit.CreateChatCommand{OpID: "op-1", Name: "Tangent notes", Model: "claude-opus-5"}))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", statusOf(err), errText(err))
	}
	id := chatIDOfResponse(t, body)
	if !strings.HasPrefix(string(id), "c-") || !ids.ValidChatID(string(id)) {
		t.Errorf("returned id %q is not a valid minted chat id", id)
	}
	c, ok := store.Get(t.Context(), id)
	if !ok {
		t.Fatalf("the returned chat %q is not in the store", id)
	}
	if c.Name != "Tangent notes" || c.Model != "claude-opus-5" {
		t.Errorf("stored chat = {name:%q model:%q}, want the payload's name and model", c.Name, c.Model)
	}
}

// TestCmdCreateChat_RepeatOpReturnsOneChat is the defect server minting
// introduces, and the ledger's whole reason to exist. Both attempts carry the same
// op id, which is what the client sends when the action framework retries a
// dispatch or the user presses Retry on the failure toast.
func TestCmdCreateChat_RepeatOpReturnsOneChat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := newTestHost(t, store)
	ops := newTestMembership(t, host)
	req := func() *vibekit.ClientCommand {
		return createReq(t, "", vibekit.CreateChatCommand{OpID: "op-retry"})
	}

	first, err := CmdCreateChat(t.Context(), ops, req())
	if statusOf(err) != http.StatusOK {
		t.Fatalf("first attempt: status = %d, want 200 (%s)", statusOf(err), errText(err))
	}
	second, err := CmdCreateChat(t.Context(), ops, req())
	if statusOf(err) != http.StatusOK {
		t.Fatalf("retry: status = %d, want 200 (%s)", statusOf(err), errText(err))
	}

	if got, want := chatIDOfResponse(t, second), chatIDOfResponse(t, first); got != want {
		t.Errorf("retry returned chat %q, want the first attempt's %q", got, want)
	}
	if got := storedIDs(t, store); len(got) != 1 {
		t.Errorf("store holds %d chats (%v), want exactly 1: one gesture is one chat", len(got), got)
	}
}

// TestCmdCreateChat_OpMintedPerAttemptMakesTwoChats pins the rule the CLIENT has
// to follow rather than assuming it: `op_id` is a dispatch ARGUMENT, never minted
// inside an action's run().
//
// Verified against node_modules/@cplieger/actions/src/define.ts: `runWithRetry`
// re-invokes `def.run(args, signal, ctx)` per attempt with the same `args`, while
// the idempotency key is computed ONCE in `runOnce` outside that loop. So an id
// minted inside run() is fresh on every attempt, and this test states what the
// server then does with it — two chats, silently, for one gesture. There is no
// server-side guard that could save it, which is exactly why the rule lives at the
// dispatch site and why the failure is asserted here rather than argued in a
// comment.
func TestCmdCreateChat_OpMintedPerAttemptMakesTwoChats(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := newTestHost(t, store)
	ops := newTestMembership(t, host)

	for _, op := range []string{"op-attempt-1", "op-attempt-2"} {
		if _, err := CmdCreateChat(t.Context(), ops,
			createReq(t, "", vibekit.CreateChatCommand{OpID: op})); err != nil {
			t.Fatalf("attempt %s: %v", op, err)
		}
	}

	if got := storedIDs(t, store); len(got) != 2 {
		t.Errorf("store holds %d chats (%v), want 2: a per-attempt op id defeats the ledger, "+
			"which is why the client mints one per GESTURE", len(got), got)
	}
}

// TestCmdCreateChat_NoOpIDMintsEveryTime is the other side of the same rule, and
// it is deliberate rather than a gap: with no correlation id there is no key to
// record a chat under, so every attempt is a new gesture. It is also the shape two
// separate clicks on New chat take, which the design states rather than guards.
func TestCmdCreateChat_NoOpIDMintsEveryTime(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := newTestHost(t, store)
	ops := newTestMembership(t, host)

	for range 2 {
		if _, err := CmdCreateChat(t.Context(), ops,
			createReq(t, "", vibekit.CreateChatCommand{})); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	if got := storedIDs(t, store); len(got) != 2 {
		t.Errorf("store holds %d chats (%v), want 2", len(got), got)
	}
}

// TestCmdCreateChat_AcceptsAnExplicitID keeps the cheapest compatibility the
// change allows: the envelope id still works, so no caller is broken by the mint.
func TestCmdCreateChat_AcceptsAnExplicitID(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := newTestHost(t, store)

	body, err := CmdCreateChat(t.Context(), newTestMembership(t, host),
		createReq(t, "c-chosen", vibekit.CreateChatCommand{}))
	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", statusOf(err), errText(err))
	}

	if got := chatIDOfResponse(t, body); got != "c-chosen" {
		t.Errorf("returned %q, want the id the envelope named", got)
	}
	if _, ok := store.Get(t.Context(), "c-chosen"); !ok {
		t.Error("the chat was not created under the id the envelope named")
	}
}

func TestCmdCreateChat_Refusals(t *testing.T) {
	cases := []struct {
		desc    string
		payload vibekit.CreateChatCommand
	}{
		{desc: "a model id that is not an identifier", payload: vibekit.CreateChatCommand{Model: "../etc/passwd"}},
		{desc: "a name over the cap", payload: vibekit.CreateChatCommand{Name: strings.Repeat("n", vibekit.MaxChatNameBytes+1)}},
		{desc: "an op id with a path separator", payload: vibekit.CreateChatCommand{OpID: "op/../x"}},
		{desc: "an op id over the identifier cap", payload: vibekit.CreateChatCommand{OpID: strings.Repeat("o", 129)}},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			host := newTestHost(t, store)

			_, err := CmdCreateChat(t.Context(), newTestMembership(t, host), createReq(t, "", tc.payload))

			if statusOf(err) != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 for %s", statusOf(err), tc.desc)
			}
			if got := storedIDs(t, store); len(got) != 0 {
				t.Errorf("a refused create left %d chats behind (%v), want none", len(got), got)
			}
		})
	}
}

// --- the ledger itself ---

// TestCreateLedger_ResolvesOncePerOp is the property every caller relies on:
// reserve and mint happen under ONE lock, so an overlapping repeat cannot mint a
// second id.
func TestCreateLedger_ResolvesOncePerOp(t *testing.T) {
	l := newCreateLedger()
	mints := 0
	mint := func() vibekit.ChatID {
		mints++
		return vibekit.NewChatID()
	}

	first, replay := l.resolve("op-a", mint)
	if replay {
		t.Error("the first resolve reported a replay")
	}
	second, replay := l.resolve("op-a", mint)
	if !replay {
		t.Error("the repeat did not report a replay")
	}

	if first != second {
		t.Errorf("resolve returned %q then %q, want one id for one op", first, second)
	}
	if mints != 1 {
		t.Errorf("minted %d ids, want 1", mints)
	}
}

// TestCreateLedger_ExpiresAndBounds covers the two things that make the map safe
// to keep in memory. The TTL is checked through an injected clock rather than a
// sleep: the real value is ten minutes.
func TestCreateLedger_ExpiresAndBounds(t *testing.T) {
	t.Run("an op past its TTL mints again", func(t *testing.T) {
		l := newCreateLedger()
		now := time.Now()
		l.now = func() time.Time { return now }

		first, _ := l.resolve("op-a", vibekit.NewChatID)
		now = now.Add(createOpTTL + time.Second)
		second, replay := l.resolve("op-a", vibekit.NewChatID)

		if replay {
			t.Error("an expired op reported a replay")
		}
		if first == second {
			t.Error("an expired op resolved to the same chat, so the TTL did nothing")
		}
	})

	t.Run("the map stays under its cap", func(t *testing.T) {
		l := newCreateLedger()
		l.maxN = 8
		for i := range 200 {
			l.resolve("op-"+string(rune('a'+i%26))+string(rune('a'+i/26)), vibekit.NewChatID)
		}

		l.mu.Lock()
		n := len(l.ops)
		l.mu.Unlock()
		if n > l.maxN {
			t.Errorf("ledger holds %d entries, want at most %d", n, l.maxN)
		}
	})

	t.Run("an empty op is never recorded", func(t *testing.T) {
		l := newCreateLedger()
		first, replay := l.resolve("", vibekit.NewChatID)
		second, _ := l.resolve("", vibekit.NewChatID)

		if replay {
			t.Error("an empty op reported a replay")
		}
		if first == second {
			t.Error("an empty op resolved to one chat twice; there is no key to record it under")
		}
		l.mu.Lock()
		n := len(l.ops)
		l.mu.Unlock()
		if n != 0 {
			t.Errorf("ledger recorded %d entries for an empty op, want 0", n)
		}
	})
}
