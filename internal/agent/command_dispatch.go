package agent

import (
	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/kiroauth"
	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// registerCommandHandlers populates the dispatcher with the concrete
// dispatch table. Called once from NewHub.
func (rt *Runtime) registerCommandHandlers() {
	rt.membership = command.RegisterDefaults(rt.dispatcher, &command.Roles{
		Bridges:     bridgeRole{coord: rt.coord},
		Chats:       rt.chatStore,
		Bus:         rt.bus,
		Tabs:        tabSetOrNil(rt.tabs),
		Teardown:    rt,
		Perms:       rt.bus,
		Terminals:   rt.agentTerms,
		Workspace:   command.Workspace{Dir: rt.lifecycle.workDir, ConfigDir: rt.lifecycle.configDir},
		Lifecycle:   rt.lifecycle,
		MCP:         rt.mcpRegistry,
		TurnOutcome: rt,
		Tokens:      tokenSourceOrNil(rt.kiroToken),
	})

	rt.dispatcher.Register(vibekit.CmdSwitchModel, rt.cmdSwitchModel)
}

// tabSetOrNil converts an absent tab store into a nil INTERFACE rather than a
// non-nil interface holding a nil pointer, which would make the coordinator's
// `m.tabs == nil` guard false and every tab command nil-deref.
func tabSetOrNil(st *tabs.Store) command.TabSet {
	if st == nil {
		return nil
	}
	return st
}

// tokenSourceOrNil converts an unwired token source into a nil INTERFACE for
// the same reason as tabSetOrNil: Invalidate takes the source's mutex, so a
// non-nil interface holding a nil *kiroauth.CLISource would nil-deref.
func tokenSourceOrNil(src *kiroauth.CLISource) command.TokenSource {
	if src == nil {
		return nil
	}
	return src
}

// Membership returns the coordinator over the chat store and the open-tab
// set, retention's handle on the open-tab predicate and the post-purge close.
func (rt *Runtime) Membership() *command.Membership { return rt.membership }
