package agent

import (
	"context"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// Settings owns the KAS CONFIGURATION surface: knowledge bases, hooks, the
// governance state and the Cedar policy queries, plus the HTTP routes over them.
//
// The grouping is not by name, it is by collaborator. Every one of these reads or
// writes configuration through the UTILITY bridge — the bridgeless, chatless
// runtime kept for exactly this class of call — while the rest of the runtime talks to
// a chat's own bridge. mcp_control.go looks similar and is deliberately NOT here:
// it reaches firstLiveBridge, a CHAT bridge, because reconnecting an MCP server
// and fetching a prompt are things a live session does.
//
// utility arrives as a thunk for the same reason Runs's does: the runtime is
// built under a sync.Once whose hooks call back into hub surfaces, and one of
// those hooks is this plane's own broadcastHooksChanged. Holding a built runtime
// here would be a construction cycle.
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

// Config exposes the configuration plane to the composition root, which hands it
// to the server as its policyProvider (PolicyList + PolicyExplain).
//
// One accessor rather than two Runtime forwards, same as Runs().
//
// the caller only forwards it on.
//
//nolint:revive // unexported-return: the type is package-internal on purpose and
func (rt *Runtime) Config() *Settings { return rt.config }
