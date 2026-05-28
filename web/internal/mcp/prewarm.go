// Adapter: delegates to internal/mcp/prewarm sub-package.
// Kept for backward compatibility with composition.go's mcpPkg.* references.
package mcp

import (
	"context"

	"vibekit/internal/mcp/prewarm"
)

// PrewarmState re-exports the sub-package type for existing consumers.
type PrewarmState = prewarm.State

const (
	PrewarmInstalling = prewarm.Installing
	PrewarmDone       = prewarm.Done
	PrewarmFailed     = prewarm.Failed
)

// PrewarmRunner re-exports the sub-package type.
type PrewarmRunner = prewarm.Runner

// storeAdapter adapts *Store to the prewarm.ServerLister interface.
type storeAdapter struct {
	store *Store
}

func (a *storeAdapter) EnabledServers(ctx context.Context) []prewarm.ServerInfo {
	servers := a.store.EnabledRaw(ctx)
	out := make([]prewarm.ServerInfo, len(servers))
	for i, s := range servers {
		out[i] = prewarm.ServerInfo{
			Prewarm:   s.Prewarm,
			Enabled:   s.Enabled,
			Transport: string(s.Transport),
			Command:   s.Command,
			Args:      s.Args,
		}
	}
	return out
}

// NewPrewarmRunner creates a prewarm runner using the store as server lister.
func NewPrewarmRunner(ctx context.Context, store *Store) *PrewarmRunner {
	r := prewarm.NewRunner(ctx, &storeAdapter{store: store})
	return r
}

// extractNpxPackage delegates to the sub-package for backward compat with tests.
func extractNpxPackage(s *Server) string {
	return prewarm.ExtractNpxPackage(prewarm.ServerInfo{
		Prewarm:   s.Prewarm,
		Enabled:   s.Enabled,
		Transport: string(s.Transport),
		Command:   s.Command,
		Args:      s.Args,
	})
}

// npmPkgSpecRe re-exports the regex for test access.
var npmPkgSpecRe = prewarm.NpmPkgSpecRe

// tailOutput re-exports for test access.
var tailOutput = prewarm.TailOutput

// ringBuffer re-exports for test access.
type ringBuffer = prewarm.RingBuffer
