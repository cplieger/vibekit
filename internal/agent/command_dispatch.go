package agent

import (
	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// registerCommandHandlers populates the dispatcher with the concrete
// dispatch table. Called once from NewHub.
func (h *Runtime) registerCommandHandlers() {
	// Register all standard handlers from the command package; which handler
	// receives which role is command.RegisterDefaults' table.
	//
	// A role is wired to whatever ACTUALLY owns its work. Four of the seven are
	// one collaborator's job, and each of those was a set of Runtime methods whose
	// whole bodies forwarded to a single field — the runtime was a name in the path
	// and nothing else. Three are irreducibly hub-wide and say so by naming h:
	// Chats spans the chat store, the SSE plane and the run list; TurnOutcome is
	// the seam between the bridge coordinator and the terminal registry (a turn
	// ending must close its terminal attribution); Bridges is one collaborator's
	// but needs the *sharedBridge <-> command.Bridge adaptation the runtime performs.
	command.RegisterDefaults(h.dispatcher, &command.Roles{
		Bridges: h,
		Chats:   h.chatStore,
		Bus:     h.bus,
		// Teardown is the runtime's own, and it is the LAST role that has to be:
		// each member reaches the decision tracker, the coordinator, the terminal
		// registry, the buffers, the line tracker and the run surface at once, so
		// no collaborator can own it.
		Teardown:  h,
		Perms:     h.bus,
		Terminals: h.agentTerms,
		// A value, not h: the two paths are process constants, so nothing is
		// substituted and the runtime is not in the middle of reading a string.
		Workspace:   command.Workspace{Dir: h.lifecycle.workDir, ConfigDir: h.lifecycle.configDir},
		Lifecycle:   h.lifecycle,
		MCP:         h.mcpRegistry,
		TurnOutcome: h,
	})

	// Register handlers that remain on Runtime (complex internal coupling).
	h.dispatcher.Register(vibekit.CmdSwitchModel, h.cmdSwitchModel)
}
