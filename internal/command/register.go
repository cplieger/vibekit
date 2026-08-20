package command

import (
	"context"
	"net/http"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// RegisterDefaults populates the dispatcher with the standard command
// handlers. Called once from Hub construction.
//
// It is also the role distribution table: every handler's roles are visible on
// its own line, so widening one handler's reach is a diff here and not an
// invisible consequence of a wider host.
func RegisterDefaults(d *Dispatcher, r *Roles) {
	d.Register(vibekit.CmdCreateChat, bind1(d, r.Chats, CmdCreateChat))
	d.Register(vibekit.CmdResumeSession, bind1(d, r.Chats, CmdResumeSession))
	d.Register(vibekit.CmdSetDraft, bind1(d, r.Chats, CmdSetDraft))
	d.Register(vibekit.CmdDeleteChat, bind1(d, r.Chats, CmdDeleteChat))
	d.Register(vibekit.CmdCompact, bind1(d, r.Bridges, CmdCompact))
	d.Register(vibekit.CmdSteer, bind1(d, r.Bridges, CmdSteer))
	d.Register(vibekit.CmdSteerClear, bind1(d, r.Bridges, CmdSteerClear))
	d.Register(vibekit.CmdCreateHook, bind1(d, r.Workspace, CmdCreateHook))

	d.Register(vibekit.CmdPermissionResponse, bind2(d, r.Bridges, r.Perms, CmdPermission))
	d.Register(vibekit.CmdElicitationResponse, bind2(d, r.Bridges, r.Perms, CmdElicitationResponse))
	d.Register(vibekit.CmdUserInputResponse, bind2(d, r.Bridges, r.Perms, CmdUserInputResponse))
	d.Register(vibekit.CmdRewindChat, bind2(d, r.Bridges, r.Chats, CmdRewindChat))
	d.Register(vibekit.CmdSetEffort, bind2(d, r.Bridges, r.Chats, CmdSetEffort))
	d.Register(vibekit.CmdSetMode, bind2(d, r.Bridges, r.Chats, CmdSetMode))
	d.Register(vibekit.CmdSetSupervisedMode, bind2(d, r.Bridges, r.Chats, CmdSetSupervisedMode))

	d.Register(vibekit.CmdCancel, bind3(d, r.Bridges, r.Perms, r.Terminals, CmdCancel))
	d.Register(vibekit.CmdCloseChat, bind3(d, r.Bridges, r.Chats, r.Perms, CmdCloseChat))
	d.Register(vibekit.CmdForkChat, bind3(d, r.Bridges, r.Chats, r.Workspace, CmdForkChat))

	d.Register(vibekit.CmdPrompt, bind1(d, &promptRoles{
		bridges:     r.Bridges,
		chats:       r.Chats,
		workspace:   r.Workspace,
		lifecycle:   r.Lifecycle,
		mcp:         r.MCP,
		turnOutcome: r.TurnOutcome,
	}, CmdPrompt))
}

// bind1 adapts a one-role handler into the Handler signature, closing over the
// role it was registered with.
//
// Three arities rather than one variadic binder: the type parameters are what
// make the registration line above compile only when the roles handed over are
// the roles the handler declared, and a []any would give that up.
func bind1[A any](d *Dispatcher, a A, fn func(*Dispatcher, A, context.Context, http.ResponseWriter, *vibekit.ClientCommand)) Handler {
	return func(ctx context.Context, w http.ResponseWriter, cmd *vibekit.ClientCommand) {
		fn(d, a, ctx, w, cmd)
	}
}

// bind2 adapts a two-role handler into the Handler signature.
func bind2[A, B any](d *Dispatcher, a A, b B, fn func(*Dispatcher, A, B, context.Context, http.ResponseWriter, *vibekit.ClientCommand)) Handler {
	return func(ctx context.Context, w http.ResponseWriter, cmd *vibekit.ClientCommand) {
		fn(d, a, b, ctx, w, cmd)
	}
}

// bind3 adapts a three-role handler into the Handler signature.
func bind3[A, B, C any](d *Dispatcher, a A, b B, c C, fn func(*Dispatcher, A, B, C, context.Context, http.ResponseWriter, *vibekit.ClientCommand)) Handler {
	return func(ctx context.Context, w http.ResponseWriter, cmd *vibekit.ClientCommand) {
		fn(d, a, b, c, ctx, w, cmd)
	}
}
