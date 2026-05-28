package hub

import (
	"testing"

	"vibekit/internal/api"
	"vibekit/internal/checkpoint"
)

// TestCheckpointStore_SatisfiesInterface verifies that *checkpoint.Store
// directly satisfies api.CheckpointService without an adapter.
func TestCheckpointStore_SatisfiesInterface(t *testing.T) {
	var _ api.CheckpointService = (*checkpoint.Store)(nil)
}
