package mcp

import (
	"context"

	"github.com/cplieger/vibekit/internal/mcp/prewarm"
)

// NewPrewarmRunner creates a prewarm runner using the store directly
// as the server lister (Store satisfies prewarm.ServerLister via its
// EnabledServers method).
func NewPrewarmRunner(ctx context.Context, store *Store) *prewarm.Runner {
	return prewarm.NewRunner(ctx, store)
}
