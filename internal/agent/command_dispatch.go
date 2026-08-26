package agent

import (
	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/tabs"
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
	// The membership coordinator is the one thing this returns, because retention
	// needs it too: its open-tab predicate and its post-purge close are the only
	// callers of it outside internal/command. A nil tab store travels as a nil
	// role — the coordinator's tab half then answers unavailable, which is what a
	// build with no config dir has.
	rt.membership = command.RegisterDefaults(rt.dispatcher, &command.Roles{
		Bridges: bridgeRole{coord: rt.coord},
		Chats:   rt.chatStore,
		Bus:     rt.bus,
		Tabs:    tabSetOrNil(rt.tabs),
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

// tabSetOrNil converts an absent tab store into a nil INTERFACE rather than a
// non-nil interface holding a nil pointer.
//
// The distinction is the whole reason this function exists: `command.TabSet(st)`
// for a nil *tabs.Store is a non-nil interface value, so the coordinator's
// `m.tabs == nil` guard would be false and every tab command would nil-deref
// instead of answering unavailable. bridgeRole exists in this package for exactly
// the same trap.
func tabSetOrNil(st *tabs.Store) command.TabSet {
	if st == nil {
		return nil
	}
	return st
}

// Membership returns the coordinator over the chat store and the open-tab set.
//
// It is the composition root's handle on retention's two tab-facing hooks (the
// open-tab predicate and the post-purge close), and it exists for nothing else:
// every command handler receives the coordinator at registration.
func (rt *Runtime) Membership() *command.Membership { return rt.membership }
