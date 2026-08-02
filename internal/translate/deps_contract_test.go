package translate

import (
	"context"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// TestStubDeps_Contract verifies that baseDeps satisfies the Deps
// interface contract: non-nil returns for required accessors and
// no panics on basic operations.
func TestStubDeps_Contract(t *testing.T) {
	d := newBaseDeps()

	// Verify interface satisfaction at compile time.
	var _ Deps = d

	ctx := context.Background()

	// ChatStore must be non-nil.
	if d.ChatStore() == nil {
		t.Error("ChatStore() returned nil")
	}

	// WorkDir must be non-empty.
	if d.WorkDir() == "" {
		t.Error("WorkDir() returned empty string")
	}

	// BufferStore must be non-nil.
	if d.BufferStore() == nil {
		t.Error("BufferStore() returned nil")
	}

	// LineTracker must be non-nil.
	if d.LineTracker() == nil {
		t.Error("LineTracker() returned nil")
	}

	// MCPRecorder must be non-nil.
	if d.MCPRecorder() == nil {
		t.Error("MCPRecorder() returned nil")
	}

	// Broadcast must not panic.
	d.Broadcast(ctx, api.ServerEvent{})

	// BridgeNotify must not panic.
	_ = d.BridgeNotify(ctx, "chat-1", "test", nil)

	// BridgeRespond must not panic.
	_ = d.BridgeRespond(ctx, "chat-1", 1, nil, nil)
}

// TestBaseDeps_FullContract mirrors the hub's TranslateDepsContractTest
// assertions to catch drift between baseDeps and the Deps interface.
func TestBaseDeps_FullContract(t *testing.T) {
	d := newBaseDeps()
	ctx := context.Background()

	t.Run("ChatStore_non_nil", func(t *testing.T) {
		if d.ChatStore() == nil {
			t.Error("ChatStore() returned nil")
		}
	})

	t.Run("WorkDir_non_empty", func(t *testing.T) {
		if d.WorkDir() == "" {
			t.Error("WorkDir() returned empty string")
		}
	})

	t.Run("Broadcast_does_not_panic", func(t *testing.T) {
		d.Broadcast(ctx, api.ServerEvent{Type: "test_event", ChatID: "chat-1"})
	})

	t.Run("BridgeNotify_nil_error_for_unknown_chat", func(t *testing.T) {
		err := d.BridgeNotify(ctx, "unknown-chat", "test/method", nil)
		if err != nil {
			t.Errorf("BridgeNotify(unknown) = %v, want nil", err)
		}
	})

	t.Run("BridgeRespond_nil_error_for_unknown_chat", func(t *testing.T) {
		err := d.BridgeRespond(ctx, "unknown-chat", 1, nil, nil)
		if err != nil {
			t.Errorf("BridgeRespond(unknown) = %v, want nil", err)
		}
	})

	t.Run("ParentACPSession_empty_for_unknown_chat", func(t *testing.T) {
		if s := d.ParentACPSession("unknown-chat"); s != "" {
			t.Errorf("ParentACPSession(unknown) = %q, want empty", s)
		}
	})

	t.Run("MCPRecorder_does_not_panic", func(t *testing.T) {
		r := d.MCPRecorder()
		if r == nil {
			t.Fatal("MCPRecorder() returned nil")
		}
		r.RecordConnected(ctx, "test-server", nil, nil)
		r.SignalReady()
	})

	t.Run("PendingPermsAdd_Remove_does_not_panic", func(t *testing.T) {
		d.PendingPermsAdd(42, api.ServerEvent{Type: "permission_needed", ChatID: "c1"})
		d.PendingPermsRemove(42)
	})

	t.Run("NotifyPush_does_not_panic", func(t *testing.T) {
		d.NotifyPush(ctx, "test body", api.PushKindPermission)
	})

	t.Run("BufferStore_non_nil", func(t *testing.T) {
		if d.BufferStore() == nil {
			t.Error("BufferStore() returned nil")
		}
	})

	t.Run("LineTracker_non_nil", func(t *testing.T) {
		if d.LineTracker() == nil {
			t.Error("LineTracker() returned nil")
		}
	})

	t.Run("IsHookStatusEnabled_returns_bool", func(t *testing.T) {
		_ = d.IsHookStatusEnabled()
	})
}
