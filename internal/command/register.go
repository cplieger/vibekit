package command

import (
	"context"
	"net/http"

	"vibekit/internal/api"
)

// RegisterDefaults populates the dispatcher with the standard command
// handlers. Called once from Hub construction.
func RegisterDefaults(d *Dispatcher) {
	d.Register(api.CmdCreateChat, wrap(d, CmdCreateChat))
	d.Register(api.CmdPrompt, wrap(d, CmdPrompt))
	d.Register(api.CmdCancel, wrap(d, CmdCancel))
	d.Register(api.CmdDeleteChat, wrap(d, CmdDeleteChat))
	d.Register(api.CmdPermissionResponse, wrap(d, CmdPermission))
	d.Register(api.CmdSpawnSubagent, wrap(d, CmdSpawnSubagent))
	d.Register(api.CmdMessageSubagent, wrap(d, CmdMessageSubagent))
	d.Register(api.CmdTerminateSubagent, wrap(d, CmdTerminateSubagent))
	d.Register(api.CmdAttachSubagent, wrap(d, CmdAttachSubagent))
	d.Register(api.CmdListSessions, wrap(d, CmdListSessions))
	d.Register(api.CmdSetAutoApproveCrew, wrap(d, CmdSetAutoApproveCrew))
	d.Register(api.CmdRestoreCheckpoint, wrap(d, CmdRestoreCheckpoint))
	d.Register(api.CmdUndoEdit, wrap(d, CmdUndoEdit))
	d.Register(api.CmdRewindChat, wrap(d, CmdRewindChat))
	d.Register(api.CmdPromoteRewindChat, wrap(d, CmdPromoteRewindChat))
	d.Register(api.CmdDiscardRewindChat, wrap(d, CmdDiscardRewindChat))
	d.Register(api.CmdSetEffort, wrap(d, CmdSetEffort))
	d.Register(api.CmdResolvePendingChange, wrap(d, CmdResolvePendingChange))
	d.Register(api.CmdResolvePendingPartial, wrap(d, CmdResolvePendingChangePartial))
	d.Register(api.CmdResolveAllPendingChanges, wrap(d, CmdResolveAllPendingChanges))
	d.Register(api.CmdSetSupervisedMode, wrap(d, CmdSetSupervisedMode))
	d.Register(api.CmdTrustPendingChanges, wrap(d, CmdTrustPendingChanges))
	d.Register(api.CmdClearPendingTrust, wrap(d, CmdClearPendingTrust))
	d.Register(api.CmdCreateHook, wrap(d, CmdCreateHook))
}

// wrap adapts a handler that takes *Dispatcher as first arg into the
// Handler signature (func(ctx, w, cmd)).
func wrap(d *Dispatcher, fn func(*Dispatcher, context.Context, http.ResponseWriter, *api.ClientCommand)) Handler {
	return func(ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) {
		fn(d, ctx, w, cmd)
	}
}
