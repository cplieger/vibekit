package testsupport

import (
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
		c, ok := s.Get(t.Context(), "any")
		if ok {
			t.Error("Get returned true")
		}
		if c != nil {
			t.Error("Get returned non-nil chat")
		}
	})

	t.Run("List_returns_nil", func(t *testing.T) {
		if h := s.List(t.Context()); h != nil {
			t.Errorf("List returned %v, want nil", h)
		}
	})

	t.Run("BuildHistory_returns_empty", func(t *testing.T) {
		if h := s.BuildHistory(t.Context(), "any"); h != "" {
			t.Errorf("BuildHistory = %q, want empty", h)
		}
	})

	t.Run("Mutate_returns_nil", func(t *testing.T) {
		err := s.Mutate(t.Context(), "any", func(*api.Chat, bool) bool { return true })
		if err != nil {
			t.Errorf("Mutate: %v", err)
		}
	})

	t.Run("Delete_returns_nil", func(t *testing.T) {
		if err := s.Delete(t.Context(), "any"); err != nil {
			t.Errorf("Delete: %v", err)
		}
	})

	t.Run("AppendMessage_returns_nil", func(t *testing.T) {
		err := s.AppendMessage(t.Context(), "any", &api.Message{})
		if err != nil {
			t.Errorf("AppendMessage: %v", err)
		}
	})

	t.Run("UpdateMessage_returns_nil", func(t *testing.T) {
		err := s.UpdateMessage(t.Context(), "any", "m1", func(*api.Message) {})
		if err != nil {
			t.Errorf("UpdateMessage: %v", err)
		}
	})

	t.Run("no_panic_on_rapid_calls", func(t *testing.T) {
		// Exercise all methods in sequence to verify no panics.
		s.RegisterRoutes(nil)
		_, _ = s.Get(t.Context(), "x")
		_ = s.List(t.Context())
		_ = s.BuildHistory(t.Context(), "x")
		_ = s.Mutate(t.Context(), "x", func(*api.Chat, bool) bool { return false })
		_ = s.Delete(t.Context(), "x")
		_ = s.AppendMessage(t.Context(), "x", &api.Message{})
		_ = s.UpdateMessage(t.Context(), "x", "m", func(*api.Message) {})
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
			ctx := t.Context()
			_, _ = s.Get(ctx, "x")
			_ = s.List(ctx)
			_ = s.BuildHistory(ctx, "x")
			_ = s.Mutate(ctx, "x", func(*api.Chat, bool) bool { return false })
			_ = s.Delete(ctx, "x")
			_ = s.AppendMessage(ctx, "x", &api.Message{})
			_ = s.UpdateMessage(ctx, "x", "m", func(*api.Message) {})
		}()
	}
	for range goroutines {
		<-done
	}
}
