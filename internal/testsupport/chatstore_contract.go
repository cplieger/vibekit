package testsupport

import (
	"context"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// ChatStoreContract is the subject of ChatStoreContractTest: the 7 methods of a
// chat store this suite exercises. There is no shared ChatStore interface any
// more — each consumer declares 1 to 5 methods of the 9 — and a contract suite
// has no business naming a method it does not assert on.
//
// SetDraft is absent because the suite does not exercise it, and that is a GAP
// rather than a decision: SetDraft has the subtlest contract of the nine (no
// UpdatedAt stamp, no broadcast, a silent no-op on a chat that does not exist)
// and no shared case pins any of it. RegisterRoutes is absent because a store's
// HTTP mounting is not a storage behaviour.
type ChatStoreContract interface {
	Get(ctx context.Context, id vibekit.ChatID) (*vibekit.Chat, bool)
	List(ctx context.Context) []vibekit.ChatHeader
	BuildHistory(ctx context.Context, id vibekit.ChatID) string
	Mutate(ctx context.Context, id vibekit.ChatID, mutate func(c *vibekit.Chat, exists bool) bool) error
	Delete(ctx context.Context, id vibekit.ChatID) error
	AppendMessage(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error
	UpdateMessage(ctx context.Context, chatID vibekit.ChatID, msgID string, mutate func(*vibekit.Message)) error
}

// ChatStoreContractTest exercises the behavioral expectations of any chat store
// implementation. Run against both fakes and real implementations to catch
// drift. Each behavior lives in its own helper so the
// suite stays flat; this is the dispatcher.
func ChatStoreContractTest(t *testing.T, newStore func(t *testing.T) ChatStoreContract) {
	t.Helper()

	t.Run("Get_missing_returns_false", func(t *testing.T) { testGetMissingReturnsFalse(t, newStore(t)) })
	t.Run("Get_returns_an_independent_copy", func(t *testing.T) { testGetReturnsIndependentCopy(t, newStore(t)) })
	t.Run("Mutate_creates_new_chat", func(t *testing.T) { testMutateCreatesNewChat(t, newStore(t)) })
	t.Run("Mutate_updates_existing_chat", func(t *testing.T) { testMutateUpdatesExistingChat(t, newStore(t)) })
	t.Run("Mutate_noop_when_false_returned", func(t *testing.T) { testMutateNoopWhenFalseReturned(t, newStore(t)) })
	t.Run("Delete_removes_chat", func(t *testing.T) { testDeleteRemovesChat(t, newStore(t)) })
	t.Run("List_returns_created_chats", func(t *testing.T) { testListReturnsCreatedChats(t, newStore(t)) })
	t.Run("BuildHistory_empty_for_missing", func(t *testing.T) { testBuildHistoryEmptyForMissing(t, newStore(t)) })
	t.Run("AppendMessage_adds_to_chat", func(t *testing.T) { testAppendMessageAddsToChat(t, newStore(t)) })
	t.Run("UpdateMessage_mutates_in_place", func(t *testing.T) { testUpdateMessageMutatesInPlace(t, newStore(t)) })
}

func testGetMissingReturnsFalse(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_, ok := s.Get(context.Background(), "nonexistent")
	if ok {
		t.Error("Get returned true for missing chat")
	}
}

// testGetReturnsIndependentCopy pins the property that makes "Mutate is the only
// write path" true: a caller holding a chat Get handed it cannot reach the stored
// one through it.
//
// The real store gets this for free — Get decodes the file, so the value is new
// bytes — and the two fakes did NOT, because `clone := *c` copies a struct and
// shares every slice inside it. Chat has seven slice fields and Message has more,
// so a caller could edit a stored message's Content, or its Blocks, with no
// Mutate anywhere. Nothing here asserted it, so both fakes were more permissive
// than the thing they stand in for, which is the direction that makes a test pass
// while production breaks.
//
// Three depths, because they fail independently: the slice header, an element,
// and an element's own slice.
func testGetReturnsIndependentCopy(t *testing.T, s ChatStoreContract) {
	t.Helper()
	const tampered = "tampered"
	ctx := context.Background()
	_ = s.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "keep"
		c.Messages = []vibekit.Message{{
			ID: "m1", Role: "assistant", Content: "original",
			Blocks: []vibekit.Block{{Type: vibekit.BlockText, Text: "block-original"}},
		}}
		return true
	})

	got, ok := s.Get(ctx, "c1")
	if !ok {
		t.Fatal("Get returned false for a chat that was just created")
	}
	if len(got.Messages) != 1 || len(got.Messages[0].Blocks) != 1 {
		t.Fatalf("fixture did not store what this case mutates: %+v", got.Messages)
	}
	got.Name = tampered
	got.Messages[0].Content = tampered
	got.Messages[0].Blocks[0].Text = tampered
	got.Messages = append(got.Messages, vibekit.Message{ID: "m2"})

	after, _ := s.Get(ctx, "c1")
	if after.Name != "keep" {
		t.Errorf("Chat.Name = %q after a caller edited a Get result, want %q", after.Name, "keep")
	}
	if len(after.Messages) != 1 {
		t.Fatalf("Messages len = %d after a caller appended to a Get result, want 1", len(after.Messages))
	}
	if after.Messages[0].Content != "original" {
		t.Errorf("Messages[0].Content = %q after a caller edited a Get result, want %q",
			after.Messages[0].Content, "original")
	}
	if len(after.Messages[0].Blocks) != 1 || after.Messages[0].Blocks[0].Text != "block-original" {
		t.Errorf("Messages[0].Blocks = %+v after a caller edited a Get result, want the stored block",
			after.Messages[0].Blocks)
	}
}

func testMutateCreatesNewChat(t *testing.T, s ChatStoreContract) {
	t.Helper()
	err := s.Mutate(context.Background(), "c1", func(c *vibekit.Chat, exists bool) bool {
		if exists {
			t.Error("exists should be false for new chat")
		}
		c.Name = "hello"
		return true
	})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	c, ok := s.Get(context.Background(), "c1")
	if !ok {
		t.Fatal("Get returned false after Mutate create")
	}
	if c.Name != "hello" {
		t.Errorf("Name = %q, want hello", c.Name)
	}
}

func testMutateUpdatesExistingChat(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_ = s.Mutate(context.Background(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "first"
		return true
	})
	_ = s.Mutate(context.Background(), "c1", func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			t.Error("exists should be true for existing chat")
		}
		c.Name = "second"
		return true
	})
	c, _ := s.Get(context.Background(), "c1")
	if c.Name != "second" {
		t.Errorf("Name = %q, want second", c.Name)
	}
}

func testMutateNoopWhenFalseReturned(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_ = s.Mutate(context.Background(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "created"
		return true
	})
	_ = s.Mutate(context.Background(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "should-not-persist"
		return false
	})
	c, _ := s.Get(context.Background(), "c1")
	if c.Name != "created" {
		t.Errorf("Name = %q, want created (mutate returned false)", c.Name)
	}
}

func testDeleteRemovesChat(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_ = s.Mutate(context.Background(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "doomed"
		return true
	})
	if err := s.Delete(context.Background(), "c1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, ok := s.Get(context.Background(), "c1")
	if ok {
		t.Error("Get returned true after Delete")
	}
}

func testListReturnsCreatedChats(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_ = s.Mutate(context.Background(), "a", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "alpha"
		return true
	})
	_ = s.Mutate(context.Background(), "b", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "beta"
		return true
	})
	headers := s.List(context.Background())
	if len(headers) != 2 {
		t.Fatalf("List len = %d, want 2", len(headers))
	}
}

func testBuildHistoryEmptyForMissing(t *testing.T, s ChatStoreContract) {
	t.Helper()
	if h := s.BuildHistory(context.Background(), "nope"); h != "" {
		t.Errorf("BuildHistory = %q, want empty", h)
	}
}

func testAppendMessageAddsToChat(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_ = s.Mutate(context.Background(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "test"
		return true
	})
	msg := &vibekit.Message{ID: "m1", Role: "user", Content: "hi"}
	if err := s.AppendMessage(context.Background(), "c1", msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	c, _ := s.Get(context.Background(), "c1")
	if len(c.Messages) != 1 {
		t.Fatalf("Messages len = %d, want 1", len(c.Messages))
	}
	if c.Messages[0].Content != "hi" {
		t.Errorf("Content = %q, want hi", c.Messages[0].Content)
	}
}

func testUpdateMessageMutatesInPlace(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_ = s.Mutate(context.Background(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "test"
		return true
	})
	_ = s.AppendMessage(context.Background(), "c1", &vibekit.Message{ID: "m1", Role: "assistant", Content: "draft"})
	err := s.UpdateMessage(context.Background(), "c1", "m1", func(m *vibekit.Message) {
		m.Content = "final"
	})
	if err != nil {
		t.Fatalf("UpdateMessage: %v", err)
	}
	c, _ := s.Get(context.Background(), "c1")
	if c.Messages[0].Content != "final" {
		t.Errorf("Content = %q, want final", c.Messages[0].Content)
	}
}
