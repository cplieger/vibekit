package translate

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestStubDeps_Contract verifies that baseDeps satisfies the Deps
// interface contract: non-nil returns for required accessors and
// no panics on basic operations.
func TestStubDeps_Contract(t *testing.T) {
	d := newBaseDeps()

	// Verify interface satisfaction at compile time.
	var _ hostDouble = d

	ctx := t.Context()

	// ChatRecords must be non-nil.
	if d.ChatRecords() == nil {
		t.Error("ChatRecords() returned nil")
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
	d.Broadcast(ctx, vibekit.ServerEvent{})
}

// TestBaseDeps_FullContract mirrors the hub's TranslateDepsContractTest
// assertions to catch drift between baseDeps and the Deps interface.
func TestBaseDeps_FullContract(t *testing.T) {
	d := newBaseDeps()
	ctx := t.Context()

	t.Run("ChatRecords_non_nil", func(t *testing.T) {
		if d.ChatRecords() == nil {
			t.Error("ChatRecords() returned nil")
		}
	})

	t.Run("WorkDir_non_empty", func(t *testing.T) {
		if d.WorkDir() == "" {
			t.Error("WorkDir() returned empty string")
		}
	})

	t.Run("Broadcast_does_not_panic", func(t *testing.T) {
		d.Broadcast(ctx, vibekit.ServerEvent{Type: "test_event", ChatID: "chat-1"})
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
		r.RecordConnected(ctx, "test-server", nil, nil, nil)
		r.SignalReady()
	})

	t.Run("PendingPermsAdd_does_not_panic", func(t *testing.T) {
		d.PendingPermsAdd(42, vibekit.ServerEvent{Type: "permission_needed", ChatID: "c1"})
	})

	t.Run("NotifyPush_does_not_panic", func(t *testing.T) {
		d.NotifyPush(ctx, "test body", vibekit.PushKindPermission, "")
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

	t.Run("IsScheduledRun_false_for_an_unmarked_run", func(t *testing.T) {
		// False is the default that matters: a manual run must never be reported
		// as scheduled, so the stub's zero value is the manual case.
		if d.IsScheduledRun("wf-unknown") {
			t.Error("IsScheduledRun(unknown) = true, want false")
		}
	})
}
