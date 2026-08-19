package command

import (
	"context"
	"net/http"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// RegisterDefaults populates the dispatcher with the standard command
// handlers. Called once from Hub construction.
func RegisterDefaults(d *Dispatcher) {
	d.Register(vibekit.CmdCreateChat, wrap(d, CmdCreateChat))
	d.Register(vibekit.CmdResumeSession, wrap(d, CmdResumeSession))
	d.Register(vibekit.CmdForkChat, wrap(d, CmdForkChat))
	d.Register(vibekit.CmdPrompt, wrap(d, CmdPrompt))
	d.Register(vibekit.CmdCancel, wrap(d, CmdCancel))
	d.Register(vibekit.CmdDeleteChat, wrap(d, CmdDeleteChat))
	d.Register(vibekit.CmdCloseChat, wrap(d, CmdCloseChat))
	d.Register(vibekit.CmdPermissionResponse, wrap(d, CmdPermission))
	d.Register(vibekit.CmdElicitationResponse, wrap(d, CmdElicitationResponse))
	d.Register(vibekit.CmdUserInputResponse, wrap(d, CmdUserInputResponse))
	d.Register(vibekit.CmdRewindChat, wrap(d, CmdRewindChat))
	d.Register(vibekit.CmdCompact, wrap(d, CmdCompact))
	d.Register(vibekit.CmdSetEffort, wrap(d, CmdSetEffort))
	d.Register(vibekit.CmdSetDraft, wrap(d, CmdSetDraft))
	d.Register(vibekit.CmdSetMode, wrap(d, CmdSetMode))
	d.Register(vibekit.CmdSetSupervisedMode, wrap(d, CmdSetSupervisedMode))
	d.Register(vibekit.CmdSteer, wrap(d, CmdSteer))
	d.Register(vibekit.CmdSteerClear, wrap(d, CmdSteerClear))
	d.Register(vibekit.CmdCreateHook, wrap(d, CmdCreateHook))
}

// wrap adapts a handler that takes *Dispatcher as first arg into the
// Handler signature (func(ctx, w, cmd)).
func wrap(d *Dispatcher, fn func(*Dispatcher, context.Context, http.ResponseWriter, *vibekit.ClientCommand)) Handler {
	return func(ctx context.Context, w http.ResponseWriter, cmd *vibekit.ClientCommand) {
		fn(d, ctx, w, cmd)
	}
}
