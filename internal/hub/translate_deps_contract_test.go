package hub

import (
	"context"
	"testing"

	"vibekit/internal/api"
	"vibekit/internal/translate"
)

// TranslateDepsContractTest exercises every method of translate.Deps
// against a concrete implementation. Purpose: catch interface drift between
// Hub's Deps implementation and translate package's expectations.
func TranslateDepsContractTest(t *testing.T, newDeps func(t *testing.T) translate.Deps) {
	t.Helper()

	t.Run("ChatStore_non_nil", func(t *testing.T) {
		d := newDeps(t)
		if d.ChatStore() == nil {
			t.Error("ChatStore() returned nil")
		}
	})

	t.Run("WorkDir_non_empty", func(t *testing.T) {
		d := newDeps(t)
		if d.WorkDir() == "" {
			t.Error("WorkDir() returned empty string")
		}
	})

	t.Run("Broadcast_does_not_panic", func(t *testing.T) {
		d := newDeps(t)
		d.Broadcast(context.Background(), api.ServerEvent{Type: "test_event", ChatID: "chat-1"})
	})

	t.Run("BridgeNotify_nil_error_for_unknown_chat", func(t *testing.T) {
		d := newDeps(t)
		err := d.BridgeNotify(context.Background(), "unknown-chat", "test/method", nil)
		if err != nil {
			t.Errorf("BridgeNotify(unknown) = %v, want nil", err)
		}
	})

	t.Run("BridgeRespond_nil_error_for_unknown_chat", func(t *testing.T) {
		d := newDeps(t)
		err := d.BridgeRespond(context.Background(), "unknown-chat", 1, nil, nil)
		if err != nil {
			t.Errorf("BridgeRespond(unknown) = %v, want nil", err)
		}
	})

	t.Run("ParentACPSession_empty_for_unknown_chat", func(t *testing.T) {
		d := newDeps(t)
		if s := d.ParentACPSession("unknown-chat"); s != "" {
			t.Errorf("ParentACPSession(unknown) = %q, want empty", s)
		}
	})

	t.Run("MCPRecorder_does_not_panic", func(t *testing.T) {
		d := newDeps(t)
		r := d.MCPRecorder()
		if r == nil {
			t.Fatal("MCPRecorder() returned nil")
		}
		r.RecordConnected(context.Background(), "test-server")
		r.SignalReady()
	})

	t.Run("PendingPermsAdd_Remove_does_not_panic", func(t *testing.T) {
		d := newDeps(t)
		d.PendingPermsAdd(42, api.ServerEvent{Type: "permission_needed", ChatID: "c1"})
		d.PendingPermsRemove(42)
	})

	t.Run("NotifyPush_does_not_panic", func(t *testing.T) {
		d := newDeps(t)
		d.NotifyPush(context.Background(), "test body", api.PushKindPermission)
	})

	t.Run("PermissionRules_returns_without_panic", func(t *testing.T) {
		d := newDeps(t)
		// May be nil when configDir is empty; the contract is that it doesn't panic.
		_ = d.PermissionRules()
	})

	t.Run("BufferStore_non_nil", func(t *testing.T) {
		d := newDeps(t)
		if d.BufferStore() == nil {
			t.Error("BufferStore() returned nil")
		}
	})

	t.Run("LineTracker_non_nil", func(t *testing.T) {
		d := newDeps(t)
		if d.LineTracker() == nil {
			t.Error("LineTracker() returned nil")
		}
	})
}

func TestHub_TranslateDepsContract(t *testing.T) {
	TranslateDepsContractTest(t, func(t *testing.T) translate.Deps {
		t.Helper()
		h, _, _ := newTestHub()
		return h
	})
}
