package hub

// Contract tests: reusable test suites that verify behavioral expectations
// of ACPBridge, ChatStore, and MCPConfig interfaces. Run against both fakes
// and real implementations to catch drift.

import (
	"context"
	"testing"
	"time"

	"vibekit/internal/api"
	"vibekit/internal/bridge"
)

// --- Bridge contract test ---

// BridgeContractTest exercises the behavioral expectations of any
// ACPBridge implementation: Start → Call → Notify → Respond → Stop
// lifecycle. Run against fakeBridge to catch drift when the real
// bridge's semantics evolve.
func BridgeContractTest(t *testing.T, newBridge func() api.ACPBridge) {
	t.Helper()

	t.Run("Start_sets_session_id", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(context.Background(), &api.StartOpts{Agent: "agent", Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if id := b.SessionID(); id == "" {
			t.Error("SessionID empty after Start")
		}
	})

	t.Run("Start_with_existing_session", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(context.Background(), &api.StartOpts{SessionID: "existing-sess", Agent: "agent", Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if id := b.SessionID(); id != "existing-sess" {
			t.Errorf("SessionID = %q, want existing-sess", id)
		}
	})

	t.Run("Call_returns_response", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(context.Background(), &api.StartOpts{Agent: "agent", Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		resp, err := b.Call(context.Background(), "session/prompt", nil)
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		if resp == nil {
			t.Fatal("Call returned nil response")
		}
	})

	t.Run("Notify_does_not_error", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(context.Background(), &api.StartOpts{Agent: "agent", Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if err := b.Notify(context.Background(), "session/update", nil); err != nil {
			t.Errorf("Notify: %v", err)
		}
	})

	t.Run("Respond_does_not_error", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(context.Background(), &api.StartOpts{Agent: "agent", Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if err := b.Respond(context.Background(), 1, map[string]string{"ok": "true"}, nil); err != nil {
			t.Errorf("Respond: %v", err)
		}
	})

	t.Run("Stop_closes_NotifCh", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(context.Background(), &api.StartOpts{Agent: "agent", Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		ch := b.NotifCh()
		b.Stop()
		// Channel must be closed after Stop.
		select {
		case _, ok := <-ch:
			if ok {
				// Draining is fine; eventually it must close.
				for range ch {
				}
			}
		case <-time.After(100 * time.Millisecond):
			t.Error("NotifCh not closed after Stop")
		}
	})

	t.Run("ModelID_returns_value", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(context.Background(), &api.StartOpts{Agent: "agent", Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if id := b.ModelID(); id == "" {
			t.Error("ModelID empty after Start")
		}
	})
}

func TestFakeBridge_Contract(t *testing.T) {
	BridgeContractTest(t, func() api.ACPBridge {
		return newFakeBridge()
	})
}

// TestFakeBridge_SharedContract runs the exported ACPBridgeContractTest
// from the bridge package against fakeBridge to detect pre-Start drift.
func TestFakeBridge_SharedContract(t *testing.T) {
	bridge.ACPBridgeContractTest(t, func() api.ACPBridge {
		return newFakeBridge()
	})
}

// --- ChatStore contract test ---

// ChatStoreContractTest exercises the behavioral expectations of any
// api.ChatStore implementation. Run against both the fake and the real
// store to catch drift.
func ChatStoreContractTest(t *testing.T, newStore func(t *testing.T) api.ChatStore) {
	t.Helper()

	t.Run("Get_missing_returns_false", func(t *testing.T) {
		s := newStore(t)
		_, ok := s.Get(context.Background(), "nonexistent")
		if ok {
			t.Error("Get returned true for missing chat")
		}
	})

	t.Run("Mutate_creates_new_chat", func(t *testing.T) {
		s := newStore(t)
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
	})

	t.Run("Mutate_updates_existing_chat", func(t *testing.T) {
		s := newStore(t)
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
	})

	t.Run("Mutate_noop_when_false_returned", func(t *testing.T) {
		s := newStore(t)
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
	})

	t.Run("Delete_removes_chat", func(t *testing.T) {
		s := newStore(t)
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
	})

	t.Run("List_returns_created_chats", func(t *testing.T) {
		s := newStore(t)
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
	})

	t.Run("BuildHistory_empty_for_missing", func(t *testing.T) {
		s := newStore(t)
		if h := s.BuildHistory(context.Background(), "nope"); h != "" {
			t.Errorf("BuildHistory = %q, want empty", h)
		}
	})

	t.Run("AppendMessage_adds_to_chat", func(t *testing.T) {
		s := newStore(t)
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
	})

	t.Run("UpdateMessage_mutates_in_place", func(t *testing.T) {
		s := newStore(t)
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
	})
}

func TestFakeChatStore_Contract(t *testing.T) {
	ChatStoreContractTest(t, func(t *testing.T) api.ChatStore {
		t.Helper()
		return newFakeChatStore()
	})
}

// --- MCPConfig contract test ---

// MCPConfigContractTest exercises the behavioral expectations of any
// api.MCPConfig implementation. Run against both the fake and the real
// mcp.Store to catch drift.
func MCPConfigContractTest(t *testing.T, newConfig func(t *testing.T) api.MCPConfig) {
	t.Helper()

	t.Run("EnabledNames_empty_when_no_servers", func(t *testing.T) {
		cfg := newConfig(t)
		names := cfg.EnabledNames(context.Background())
		if len(names) != 0 {
			t.Errorf("EnabledNames() = %v, want empty", names)
		}
	})

	t.Run("ACPServers_empty_when_no_servers", func(t *testing.T) {
		cfg := newConfig(t)
		servers := cfg.ACPServers(context.Background())
		if len(servers) != 0 {
			t.Errorf("ACPServers() = %v, want empty", servers)
		}
	})
}

func TestFakeMCPConfig_Contract(t *testing.T) {
	MCPConfigContractTest(t, func(t *testing.T) api.MCPConfig {
		t.Helper()
		return &fakeMCPConfig{enabled: map[string]struct{}{}}
	})
}
