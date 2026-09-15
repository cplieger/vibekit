package agent

import (
	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// registerCommandHandlers populates the dispatcher with the dispatch table.
func (rt *Runtime) registerCommandHandlers() {
	rt.membership = command.RegisterDefaults(rt.dispatcher, &command.Roles{
		Bridges:   bridgeRole{coord: rt.coord},
		Chats:     rt.chatStore,
		Bus:       rt.bus,
		Tabs:      tabSetOrNil(rt.tabs),
		Runs:      rt.runs,
		Teardown:  rt,
		Perms:     rt.bus,
		Terminals: rt.agentTerms,
		Workspace: command.Workspace{
			Dir:       rt.lifecycle.workDir,
			ConfigDir: rt.lifecycle.configDir,
			// An uploaded file's path comes back as an attachment, and the composer's
			// upload target sits beside the workspace rather than inside it, so
			// attachment confinement needs it as a second root or every composer
			// drop degrades to a text block naming the filename.
			UploadsDir: vibekit.DefaultUploadDir,
		},
		Lifecycle:     rt.lifecycle,
		MCP:           rt.mcpRegistry,
		TurnOutcome:   rt,
		Steers:        rt.steerLedger,
		Status:        rt,
		AuthReadiness: rt.authReadiness,
	})

	rt.dispatcher.Register(vibekit.CmdSwitchModel, rt.cmdSwitchModel)
}

func tabSetOrNil(st *tabs.Store) command.TabSet {
	if st == nil {
		return nil
	}
	return st
}

// Membership returns the coordinator over the chat store and the open-tab
// set, retention's handle on the open-tab predicate and the post-purge close.
func (rt *Runtime) Membership() *command.Membership { return rt.membership }
