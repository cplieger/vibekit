package hub

import (
	"testing"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/translate"
)

// TranslateDepsContractTest exercises every method of translate.Deps
// against a concrete implementation. Purpose: catch interface drift between
// Hub's Deps implementation and translate package's expectations.
func TranslateDepsContractTest(t *testing.T, newDeps func(t *testing.T) translate.Deps) {
	t.Helper()

	t.Run("ChatRecords_non_nil", func(t *testing.T) {
		d := newDeps(t)
		if d.ChatRecords() == nil {
			t.Error("ChatRecords() returned nil")
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
		d.Broadcast(t.Context(), api.ServerEvent{Type: "test_event", ChatID: "chat-1"})
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
		r.RecordConnected(t.Context(), "test-server", nil, nil, nil)
		r.SignalReady()
	})

	t.Run("PendingPermsAdd_does_not_panic", func(t *testing.T) {
		d := newDeps(t)
		d.PendingPermsAdd(42, api.ServerEvent{Type: "permission_needed", ChatID: "c1"})
	})

	t.Run("NotifyPush_does_not_panic", func(t *testing.T) {
		d := newDeps(t)
		d.NotifyPush(t.Context(), "test body", api.PushKindPermission, "")
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

	t.Run("IsScheduledRun_false_for_an_unlaunched_run", func(t *testing.T) {
		// A run nothing launched is not scheduled. This is the direction that
		// matters: reporting a manual run as scheduled would put a start toast on
		// every launch the user made by hand.
		d := newDeps(t)
		if d.IsScheduledRun("wf-never-launched") {
			t.Error("IsScheduledRun(unlaunched) = true, want false")
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
