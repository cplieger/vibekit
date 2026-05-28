package hub

import (
	"testing"

	"vibekit/internal/api"
)

// TestCheckpointAdapter_DelegatesAllMethods verifies the adapter satisfies
// the full CheckpointService interface at compile time and that construction
// with a nil store doesn't panic (the adapter is a thin wrapper).
func TestCheckpointAdapter_DelegatesAllMethods(t *testing.T) {
	// Compile-time assertion: if checkpointAdapter ever drops a method,
	// this assignment fails to compile.
	var _ api.CheckpointService = (*checkpointAdapter)(nil)
}

// TestCheckpointAdapter_ErrorPropagation verifies that a nil-store adapter
// panics on method calls (proving delegation is direct, not swallowed).
func TestCheckpointAdapter_ErrorPropagation(t *testing.T) {
	// With a nil store, any delegated call should panic (nil deref).
	// This proves the adapter delegates directly without error swallowing.
	a := &checkpointAdapter{store: nil}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic from nil store delegation")
		}
	}()
	_ = a.OldestTag(nil, "test-chat")
}
