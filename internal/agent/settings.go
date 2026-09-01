package agent

import (
	"context"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// Settings owns the KAS CONFIGURATION surface: knowledge bases, hooks, the
// governance state and the Cedar policy queries, plus the HTTP routes over them.
//
// Grouped by collaborator: every method here reaches configuration through the
// UTILITY bridge, while the rest of the runtime talks to a chat's own bridge
// (mcp_control.go reaches a CHAT bridge instead, so it lives elsewhere). utility
// arrives as a thunk because the runtime is built under a sync.Once whose hooks
// call back into agent surfaces, so holding a built runtime here would cycle.
type Settings struct {
	// governance caches the last governance state KAS reported, so a fresh page
	// load can be served without a round trip. Written by the translator through
	// SetGovernance and by the utility session's own warm path.
	governance *governanceCache
	// utility is the bridgeless runtime every call here goes through.
	utility func() *utilityRuntime
	// lifecycle supplies the workspace dir (a hook's path is resolved inside it)
	// and the process lifetime (the governance warm outlives its request).
	lifecycle *lifetime
	// broadcast publishes hooks_changed when a hook file changes underneath us.
	broadcast func(context.Context, vibekit.ServerEvent)
}

func newSettings(lc *lifetime, broadcast func(context.Context, vibekit.ServerEvent)) *Settings {
	return &Settings{
		governance: newGovernanceCache(),
		lifecycle:  lc,
		broadcast:  broadcast,
	}
}

// Config exposes the settings surface to the composition root, which hands it
// to the server as its policyProvider (PolicyList + PolicyExplain).
//
// One accessor rather than two Runtime forwards, same as Runs().
func (rt *Runtime) Config() *Settings { return rt.config }
