package agent

import (
	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// registerCommandHandlers populates the dispatcher with the concrete
// dispatch table. Called once from NewHub.
func (rt *Runtime) registerCommandHandlers() {
	// Register all standard handlers from the command package; which handler
	// receives which role is command.RegisterDefaults' table.
	//
	// A role is wired to whatever ACTUALLY owns its work. Four of the seven are
	// one collaborator's job, and each of those was a set of Runtime methods whose
	// whole bodies forwarded to a single field — the runtime was a name in the path
	// and nothing else. Three are irreducibly runtime-wide and say so by naming rt:
	// Chats spans the chat store, the event bus and the run list; TurnOutcome is
	// the seam between the bridge coordinator and the terminal registry (a turn
	// ending must close its terminal attribution). Bridges is the coordinator's,
	// reached through bridgeRole — the *sharedBridge <-> command.Bridge conversion
	// has to happen somewhere, and it happens in a type that does only that.
	command.RegisterDefaults(rt.dispatcher, &command.Roles{
		Bridges: bridgeRole{coord: rt.coord},
		Chats:   rt.chatStore,
		Bus:     rt.bus,
		// Teardown is the runtime's own, and it is the LAST role that has to be:
		// each member reaches the decision tracker, the coordinator, the terminal
		// registry, the buffers, the line tracker and the run surface at once, so
		// no collaborator can own it.
		Teardown:  rt,
		Perms:     rt.bus,
		Terminals: rt.agentTerms,
		// A value, not rt: the two paths are process constants, so nothing is
		// substituted and the runtime is not in the middle of reading a string.
		Workspace:   command.Workspace{Dir: rt.lifecycle.workDir, ConfigDir: rt.lifecycle.configDir},
		Lifecycle:   rt.lifecycle,
		MCP:         rt.mcpRegistry,
		TurnOutcome: rt,
	})

	// Register handlers that remain on Runtime (complex internal coupling).
	rt.dispatcher.Register(vibekit.CmdSwitchModel, rt.cmdSwitchModel)
}
