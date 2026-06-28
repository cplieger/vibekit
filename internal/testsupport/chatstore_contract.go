package testsupport

import (
	"context"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// ChatStoreContractTest exercises the behavioral expectations of any
// api.ChatStore implementation. Run against both fakes and real
// implementations to catch drift. Each behavior lives in its own helper so the
// suite stays flat; this is the dispatcher.
func ChatStoreContractTest(t *testing.T, newStore func(t *testing.T) api.ChatStore) {
	t.Helper()

	t.Run("Get_missing_returns_false", func(t *testing.T) { testGetMissingReturnsFalse(t, newStore(t)) })
	t.Run("Mutate_creates_new_chat", func(t *testing.T) { testMutateCreatesNewChat(t, newStore(t)) })
	t.Run("Mutate_updates_existing_chat", func(t *testing.T) { testMutateUpdatesExistingChat(t, newStore(t)) })
	t.Run("Mutate_noop_when_false_returned", func(t *testing.T) { testMutateNoopWhenFalseReturned(t, newStore(t)) })
	t.Run("Delete_removes_chat", func(t *testing.T) { testDeleteRemovesChat(t, newStore(t)) })
	t.Run("List_returns_created_chats", func(t *testing.T) { testListReturnsCreatedChats(t, newStore(t)) })
	t.Run("BuildHistory_empty_for_missing", func(t *testing.T) { testBuildHistoryEmptyForMissing(t, newStore(t)) })
	t.Run("AppendMessage_adds_to_chat", func(t *testing.T) { testAppendMessageAddsToChat(t, newStore(t)) })
	t.Run("UpdateMessage_mutates_in_place", func(t *testing.T) { testUpdateMessageMutatesInPlace(t, newStore(t)) })
}

func testGetMissingReturnsFalse(t *testing.T, s api.ChatStore) {
	t.Helper()
	_, ok := s.Get(context.Background(), "nonexistent")
	if ok {
		t.Error("Get returned true for missing chat")
	}
}

func testMutateCreatesNewChat(t *testing.T, s api.ChatStore) {
	t.Helper()
	err := s.Mutate(context.Background(), "c1", func(c *api.Chat, exists bool) bool {
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

func testMutateUpdatesExistingChat(t *testing.T, s api.ChatStore) {
	t.Helper()
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "first"
		return true
	})
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, exists bool) bool {
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

func testMutateNoopWhenFalseReturned(t *testing.T, s api.ChatStore) {
	t.Helper()
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "created"
		return true
	})
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "should-not-persist"
		return false
	})
	c, _ := s.Get(context.Background(), "c1")
	if c.Name != "created" {
		t.Errorf("Name = %q, want created (mutate returned false)", c.Name)
	}
}

func testDeleteRemovesChat(t *testing.T, s api.ChatStore) {
	t.Helper()
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
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

func testListReturnsCreatedChats(t *testing.T, s api.ChatStore) {
	t.Helper()
	_ = s.Mutate(context.Background(), "a", func(c *api.Chat, _ bool) bool {
		c.Name = "alpha"
		return true
	})
	_ = s.Mutate(context.Background(), "b", func(c *api.Chat, _ bool) bool {
		c.Name = "beta"
		return true
	})
	headers := s.List(context.Background())
	if len(headers) != 2 {
		t.Fatalf("List len = %d, want 2", len(headers))
	}
}

func testBuildHistoryEmptyForMissing(t *testing.T, s api.ChatStore) {
	t.Helper()
	if h := s.BuildHistory(context.Background(), "nope"); h != "" {
		t.Errorf("BuildHistory = %q, want empty", h)
	}
}

func testAppendMessageAddsToChat(t *testing.T, s api.ChatStore) {
	t.Helper()
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "test"
		return true
	})
	msg := &api.Message{ID: "m1", Role: "user", Content: "hi"}
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

func testUpdateMessageMutatesInPlace(t *testing.T, s api.ChatStore) {
	t.Helper()
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "test"
		return true
	})
	_ = s.AppendMessage(context.Background(), "c1", &api.Message{ID: "m1", Role: "assistant", Content: "draft"})
	err := s.UpdateMessage(context.Background(), "c1", "m1", func(m *api.Message) {
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
