package hub

// Contract tests: reusable test suites that verify behavioral expectations
// of ACPBridge, ChatStore, and MCPConfig interfaces. Run against both fakes
// and real implementations to catch drift.

import (
	"context"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/bridge"
	"github.com/cplieger/vibekit/internal/testsupport"
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

// ChatStoreContractTest delegates to the shared testsupport version
// so all packages run the same contract suite.
func ChatStoreContractTest(t *testing.T, newStore func(t *testing.T) api.ChatStore) {
	t.Helper()
	testsupport.ChatStoreContractTest(t, newStore)
}

func TestFakeChatStore_Contract(t *testing.T) {
	ChatStoreContractTest(t, func(t *testing.T) api.ChatStore {
		t.Helper()
		return newFakeChatStore()
	})
}

// --- MCPConfig contract test ---

// MCPConfigContractTest delegates to the shared testsupport version
// so all packages run the same contract suite.
func MCPConfigContractTest(t *testing.T, newConfig func(t *testing.T) api.MCPConfig) {
	t.Helper()
	testsupport.MCPConfigContractTest(t, newConfig)
}

func TestFakeMCPConfig_Contract(t *testing.T) {
	MCPConfigContractTest(t, func(t *testing.T) api.MCPConfig {
		t.Helper()
		return &fakeMCPConfig{enabled: map[string]struct{}{}}
	})
}
