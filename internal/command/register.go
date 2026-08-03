package command

import (
	"context"
	"net/http"

	"github.com/cplieger/vibekit/internal/api"
)

// RegisterDefaults populates the dispatcher with the standard command
// handlers. Called once from Hub construction.
func RegisterDefaults(d *Dispatcher) {
	d.Register(api.CmdCreateChat, wrap(d, CmdCreateChat))
	d.Register(api.CmdResumeSession, wrap(d, CmdResumeSession))
	d.Register(api.CmdPrompt, wrap(d, CmdPrompt))
	d.Register(api.CmdCancel, wrap(d, CmdCancel))
	d.Register(api.CmdDeleteChat, wrap(d, CmdDeleteChat))
	d.Register(api.CmdPermissionResponse, wrap(d, CmdPermission))
	d.Register(api.CmdElicitationResponse, wrap(d, CmdElicitationResponse))
	d.Register(api.CmdUserInputResponse, wrap(d, CmdUserInputResponse))
	d.Register(api.CmdRewindChat, wrap(d, CmdRewindChat))
	d.Register(api.CmdSetEffort, wrap(d, CmdSetEffort))
	d.Register(api.CmdSetMode, wrap(d, CmdSetMode))
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
