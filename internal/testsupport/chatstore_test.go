package testsupport

import (
	"context"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// TestNopChatStore_Contract verifies NopChatStore satisfies the no-op
// subset of the ChatStore contract: every method returns zero/nil/false,
// no panics on any call sequence.
func TestNopChatStore_Contract(t *testing.T) {
	s := NopChatStore{}

	// Verify compile-time assertion holds.
	var _ api.ChatStore = s

	t.Run("Get_returns_nil_false", func(t *testing.T) {
		c, ok := s.Get(context.Background(), "any")
		if ok {
			t.Error("Get returned true")
		}
		if c != nil {
			t.Error("Get returned non-nil chat")
		}
	})

	t.Run("List_returns_nil", func(t *testing.T) {
		if h := s.List(context.Background()); h != nil {
			t.Errorf("List returned %v, want nil", h)
		}
	})

	t.Run("BuildHistory_returns_empty", func(t *testing.T) {
		if h := s.BuildHistory(context.Background(), "any"); h != "" {
			t.Errorf("BuildHistory = %q, want empty", h)
		}
	})

	t.Run("Mutate_returns_nil", func(t *testing.T) {
		err := s.Mutate(context.Background(), "any", func(*api.Chat, bool) bool { return true })
		if err != nil {
			t.Errorf("Mutate: %v", err)
		}
	})

	t.Run("Delete_returns_nil", func(t *testing.T) {
		if err := s.Delete(context.Background(), "any"); err != nil {
			t.Errorf("Delete: %v", err)
		}
	})

	t.Run("Archive_returns_nil", func(t *testing.T) {
		if err := s.Archive(context.Background(), "any"); err != nil {
			t.Errorf("Archive: %v", err)
		}
	})

	t.Run("AppendMessage_returns_nil", func(t *testing.T) {
		err := s.AppendMessage(context.Background(), "any", &api.Message{})
		if err != nil {
			t.Errorf("AppendMessage: %v", err)
		}
	})

	t.Run("UpdateMessage_returns_nil", func(t *testing.T) {
		err := s.UpdateMessage(context.Background(), "any", "m1", func(*api.Message) {})
		if err != nil {
			t.Errorf("UpdateMessage: %v", err)
		}
	})

	t.Run("no_panic_on_rapid_calls", func(t *testing.T) {
		// Exercise all methods in sequence to verify no panics.
		s.RegisterRoutes(nil)
		s.SetBroadcaster(nil)
		_, _ = s.Get(context.Background(), "x")
		_ = s.List(context.Background())
		_ = s.BuildHistory(context.Background(), "x")
		_ = s.Mutate(context.Background(), "x", func(*api.Chat, bool) bool { return false })
		_ = s.Delete(context.Background(), "x")
		_ = s.Archive(context.Background(), "x")
		_ = s.ListArchived(context.Background())
		_ = s.RestoreArchived(context.Background(), "x")
		_ = s.UpdateArchivedSummary(context.Background(), "x", "s")
		_, _ = s.LoadArchived(context.Background(), "x")
		_ = s.DeleteArchived(context.Background(), "x")
		_ = s.AppendMessage(context.Background(), "x", &api.Message{})
		_ = s.UpdateMessage(context.Background(), "x", "m", func(*api.Message) {})
	})
}

// TestNopChatStore_ConcurrentSafety verifies NopChatStore is safe to use
// from multiple goroutines without external synchronization.
func TestNopChatStore_ConcurrentSafety(t *testing.T) {
	s := NopChatStore{}
	const goroutines = 10
	done := make(chan struct{})
	for range goroutines {
		go func() {
			defer func() { done <- struct{}{} }()
			ctx := context.Background()
			_, _ = s.Get(ctx, "x")
			_ = s.List(ctx)
			_ = s.BuildHistory(ctx, "x")
			_ = s.Mutate(ctx, "x", func(*api.Chat, bool) bool { return false })
			_ = s.Delete(ctx, "x")
			_ = s.Archive(ctx, "x")
			_ = s.ListArchived(ctx)
			_ = s.RestoreArchived(ctx, "x")
			_ = s.UpdateArchivedSummary(ctx, "x", "s")
			_, _ = s.LoadArchived(ctx, "x")
			_ = s.DeleteArchived(ctx, "x")
			_ = s.AppendMessage(ctx, "x", &api.Message{})
			_ = s.UpdateMessage(ctx, "x", "m", func(*api.Message) {})
		}()
	}
	for range goroutines {
		<-done
	}
}
