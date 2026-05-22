package hub

import (
	"context"
	"testing"
	"time"

	"vibekit/internal/api"
	"vibekit/internal/command"
)

// CommandDepsContractTest exercises every method of command.Dependencies
// against a concrete implementation. Purpose: catch interface drift between
// Hub's Dependencies implementation and command package's expectations.
func CommandDepsContractTest(t *testing.T, newDeps func(t *testing.T) command.Dependencies) {
	t.Helper()

	t.Run("CheckDedup_miss_returns_false", func(t *testing.T) {
		d := newDeps(t)
		_, ok := d.CheckDedup("nonexistent")
		if ok {
			t.Error("CheckDedup returned true for unknown key")
		}
	})

	t.Run("RecordDedup_then_CheckDedup_round_trip", func(t *testing.T) {
		d := newDeps(t)
		d.RecordDedup("req-1", []byte(`{"ok":true}`))
		got, ok := d.CheckDedup("req-1")
		if !ok {
			t.Fatal("CheckDedup returned false after RecordDedup")
		}
		if string(got) != `{"ok":true}` {
			t.Errorf("CheckDedup = %q, want {\"ok\":true}", got)
		}
	})

	t.Run("Draining_returns_false_initially", func(t *testing.T) {
		d := newDeps(t)
		if d.Draining() {
			t.Error("Draining() = true on fresh hub")
		}
	})

	t.Run("ChatStore_non_nil", func(t *testing.T) {
		d := newDeps(t)
		if d.ChatStore() == nil {
			t.Error("ChatStore() returned nil")
		}
	})

	t.Run("PendingStore_non_nil", func(t *testing.T) {
		d := newDeps(t)
		if d.PendingStore() == nil {
			t.Error("PendingStore() returned nil")
		}
	})

	t.Run("GetBridge_nil_for_unknown_chat", func(t *testing.T) {
		d := newDeps(t)
		if b := d.GetBridge("unknown-chat"); b != nil {
			t.Errorf("GetBridge(unknown) = %v, want nil", b)
		}
	})

	t.Run("WorkDir_non_empty", func(t *testing.T) {
		d := newDeps(t)
		if d.WorkDir() == "" {
			t.Error("WorkDir() returned empty string")
		}
	})

	t.Run("ConfigDir_returns_without_panic", func(t *testing.T) {
		d := newDeps(t)
		// May be empty when no configDir option is set; contract is no panic.
		_ = d.ConfigDir()
	})

	t.Run("ShutdownCtx_non_nil", func(t *testing.T) {
		d := newDeps(t)
		if d.ShutdownCtx() == nil {
			t.Error("ShutdownCtx() returned nil")
		}
	})

	t.Run("Broadcast_does_not_panic", func(t *testing.T) {
		d := newDeps(t)
		d.Broadcast(context.Background(), api.ServerEvent{Type: "test_event", ChatID: "chat-1"})
	})

	t.Run("MCPWaitForReady_returns_true_when_signaled", func(t *testing.T) {
		d := newDeps(t)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		// newTestHub signals MCP ready immediately.
		if !d.MCPWaitForReady(ctx, 50*time.Millisecond) {
			t.Error("MCPWaitForReady returned false")
		}
	})

	t.Run("ResolveInsideWorkDir_rejects_traversal", func(t *testing.T) {
		d := newDeps(t)
		_, err := d.ResolveInsideWorkDir("../../etc/passwd")
		if err == nil {
			t.Error("ResolveInsideWorkDir accepted path traversal")
		}
	})

	t.Run("InflightAdd_Done_no_panic", func(t *testing.T) {
		d := newDeps(t)
		d.InflightAdd(1)
		d.InflightDone()
	})
}

func TestHub_CommandDepsContract(t *testing.T) {
	CommandDepsContractTest(t, func(t *testing.T) command.Dependencies {
		t.Helper()
		h, _, _ := newTestHub()
		return h
	})
}
