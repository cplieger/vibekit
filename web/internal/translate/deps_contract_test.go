package translate

import (
	"context"
	"testing"

	"vibekit/internal/api"
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

	// NewMessageID must be non-empty.
	if d.NewMessageID() == "" {
		t.Error("NewMessageID() returned empty string")
	}

	// WorkDir must be non-empty.
	if d.WorkDir() == "" {
		t.Error("WorkDir() returned empty string")
	}

	// ConfigDir must be non-empty.
	if d.ConfigDir() == "" {
		t.Error("ConfigDir() returned empty string")
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
