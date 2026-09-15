package translate

// The `user_message_id_assigned` stamp: the id KAS's own log holds a prompt under is
// what makes that turn addressable, and there is no correlation key on the frame — the
// prompt it belongs to is the chat's newest prompt-class user row. So what is pinned
// here is WHICH row is chosen, that a repeat is a no-op, and that a frame belonging to
// something other than this chat stamps nothing.

import (
	"context"
	"testing"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// commitCountingStore records how many Mutate calls actually committed, which is the
// only way to tell an idempotent no-op from a rewrite that happened to land the same
// bytes.
type commitCountingStore struct {
	ChatRecords
	commits int
}

func (s *commitCountingStore) Mutate(ctx context.Context, id vibekit.ChatID, fn func(*vibekit.Chat, bool) bool) (string, error) {
	return s.ChatRecords.Mutate(ctx, id, func(c *vibekit.Chat, exists bool) bool {
		changed := fn(c, exists)
		if changed {
			s.commits++
		}
		return changed
	})
}

// userMessageIDFrame is the update-level object KAS sends. `_meta` sits at its top,
// which is one level in from `params` — the standing nesting trap on this wire.
func userMessageIDFrame(t *testing.T, kasID string) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta": map[string]any{
			"kiro": map[string]any{
				"kind":          "user_message_id_assigned",
				"userMessageId": kasID,
			},
		},
	})
}

// seedRows replaces c1's transcript, so each case states the layout it depends on.
func seedRows(t *testing.T, store *testsupport.InMemoryChatStore, msgs []vibekit.Message) {
	t.Helper()
	if _, err := store.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Messages = msgs
		return true
	}); err != nil {
		t.Fatalf("seed rows: %v", err)
	}
}

// kasIDsOf reads the stamp off every row, so an assertion names which row moved rather
// than only that one did.
func kasIDsOf(t *testing.T, store *testsupport.InMemoryChatStore) []string {
	t.Helper()
	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat c1 missing")
	}
	out := make([]string, 0, len(c.Messages))
	for i := range c.Messages {
		out = append(out, c.Messages[i].KASMessageID)
	}
	return out
}

// The newest prompt-class user row is the prompt this frame belongs to: KAS emits it
// between its own append and the model call, so nothing newer can exist yet.
func TestHandleSessionInfoUpdate_StampsTheNewestPromptRow(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	seedRows(t, store, []vibekit.Message{
		{ID: "m-1", Role: vibekit.RoleUser, Content: "first"},
		{ID: "a-1", Role: vibekit.RoleAssistant, Content: "reply"},
		{ID: "m-2", Role: vibekit.RoleUser, Content: "second"},
	})

	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		userMessageIDFrame(t, "38572497-a17f-4172-bdfb-7eb82919a378"), FrameAttribution{})

	got := kasIDsOf(t, store)
	want := []string{"", "", "38572497-a17f-4172-bdfb-7eb82919a378"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kas ids = %q, want %q", got, want)
		}
	}
}

// A steer is not a prompt and KAS assigns it no record id of this kind, so the stamp
// must reach past it to the prompt that opened the turn.
func TestHandleSessionInfoUpdate_SkipsASteerToReachThePrompt(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	seedRows(t, store, []vibekit.Message{
		{ID: "m-1", Role: vibekit.RoleUser, Content: "the prompt"},
		{ID: "steer-1", Role: vibekit.RoleUser, UserKind: vibekit.UserKindSteer, Content: "mid-turn"},
	})

	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		userMessageIDFrame(t, "kas-1"), FrameAttribution{})

	got := kasIDsOf(t, store)
	if got[0] != "kas-1" || got[1] != "" {
		t.Errorf("kas ids = %q, want the prompt stamped and the steer untouched", got)
	}
}

// The empty-turn retry re-sends the SAME prompt, so KAS mints a SECOND record for it and
// the newer id is the one revertMultiple will accept.
func TestHandleSessionInfoUpdate_ADifferentIDOverwritesTheStamp(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	seedRows(t, store, []vibekit.Message{{ID: "m-1", Role: vibekit.RoleUser, Content: "prompt"}})
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", userMessageIDFrame(t, "kas-1"), FrameAttribution{})
	tr.HandleSessionInfoUpdate(t.Context(), "c1", userMessageIDFrame(t, "kas-2"), FrameAttribution{})

	if got := kasIDsOf(t, store)[0]; got != "kas-2" {
		t.Errorf("kas id = %q, want kas-2: the newest record is the addressable one", got)
	}
}

// A chat file is rewritten wholesale on every commit, so a repeated frame must not cost
// one — and a repeat is reachable, since a reconnect can redeliver.
func TestHandleSessionInfoUpdate_ARepeatedIDCommitsNothing(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	seedRows(t, store, []vibekit.Message{{ID: "m-1", Role: vibekit.RoleUser, Content: "prompt"}})
	counting := &commitCountingStore{ChatRecords: store}
	deps.store = counting
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", userMessageIDFrame(t, "kas-1"), FrameAttribution{})
	tr.HandleSessionInfoUpdate(t.Context(), "c1", userMessageIDFrame(t, "kas-1"), FrameAttribution{})

	if counting.commits != 1 {
		t.Errorf("commits = %d, want 1: a repeated id is a no-op", counting.commits)
	}
}

// Nothing to stamp is a normal state, not an error: a chat whose only rows are events,
// and the chat KAS's own auto-wake prompts, both reach here with no prompt row.
func TestHandleSessionInfoUpdate_NoPromptRowCommitsNothing(t *testing.T) {
	deps, _, store := depsWithStore(t, "c1")
	seedRows(t, store, []vibekit.Message{{ID: "e-1", Role: vibekit.RoleEvent, EventKind: vibekit.EventCompacted}})
	counting := &commitCountingStore{ChatRecords: store}
	deps.store = counting

	New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
		userMessageIDFrame(t, "kas-1"), FrameAttribution{})

	if counting.commits != 0 {
		t.Errorf("commits = %d, want 0", counting.commits)
	}
}

// The stamp is positional, so an id belonging to something OTHER than this chat's own
// prompt would land on the reader's newest turn and make rewind revert the wrong thing.
// A workflow step's answer prompts on the step's session, and a subagent has its own.
func TestHandleSessionInfoUpdate_AForeignFrameStampsNothing(t *testing.T) {
	for name, attr := range map[string]FrameAttribution{
		"a workflow step's own session": {Step: true},
		"a subagent's session":          {SubSessionID: "sess_sub"},
	} {
		t.Run(name, func(t *testing.T) {
			deps, _, store := depsWithStore(t, "c1")
			seedRows(t, store, []vibekit.Message{{ID: "m-1", Role: vibekit.RoleUser, Content: "prompt"}})

			New(rolesOf(deps)).HandleSessionInfoUpdate(t.Context(), "c1",
				userMessageIDFrame(t, "kas-1"), attr)

			if got := kasIDsOf(t, store)[0]; got != "" {
				t.Errorf("kas id = %q, want empty: this frame is not the chat's", got)
			}
		})
	}
}
