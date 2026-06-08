package hub

import (
	"testing"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/checkpoint"
)

// TestCheckpointStore_SatisfiesInterface verifies that *checkpoint.Store
// directly satisfies api.CheckpointService without an adapter.
func TestCheckpointStore_SatisfiesInterface(t *testing.T) {
	var _ api.CheckpointService = (*checkpoint.Store)(nil)
}
