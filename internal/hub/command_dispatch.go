package hub

import (
	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// registerCommandHandlers populates the dispatcher with the concrete
// dispatch table. Called once from NewHub.
func (h *Hub) registerCommandHandlers() {
	// Register all standard handlers from the command package; which handler
	// receives which role is command.RegisterDefaults' table.
	//
	// A role is wired to whatever ACTUALLY owns its work. Four of the seven are
	// one collaborator's job, and each of those was a set of Hub methods whose
	// whole bodies forwarded to a single field — the hub was a name in the path
	// and nothing else. Three are irreducibly hub-wide and say so by naming h:
	// Chats spans the chat store, the SSE plane and the run list; TurnOutcome is
	// the seam between the bridge coordinator and the terminal registry (a turn
	// ending must close its terminal attribution); Bridges is one collaborator's
	// but needs the *sharedBridge <-> command.Bridge adaptation the hub performs.
	command.RegisterDefaults(h.dispatcher, &command.Roles{
		Bridges:   h,
		Chats:     h,
		Perms:     h.sse,
		Terminals: h.agentTerms,
		// A value, not h: the two paths are process constants, so nothing is
		// substituted and the hub is not in the middle of reading a string.
		Workspace:   command.Workspace{Dir: h.lifecycle.workDir, ConfigDir: h.lifecycle.configDir},
		Lifecycle:   h.lifecycle,
		MCP:         h.mcpRegistry,
		TurnOutcome: h,
	})

	// Register handlers that remain on Hub (complex internal coupling).
	h.dispatcher.Register(vibekit.CmdSwitchModel, h.cmdSwitchModel)
}
