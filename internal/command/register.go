package command

import (
	"context"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// RegisterDefaults populates the dispatcher with the standard command
// handlers. Called once from Runtime construction.
//
// It is also the role distribution table: every handler's roles are visible on
// its own line, so widening one handler's reach is a diff here and not an
// invisible consequence of a wider host.
func RegisterDefaults(d *Dispatcher, r *Roles) {
	d.Register(vibekit.CmdCreateChat, bind1(r.Chats, CmdCreateChat))
	d.Register(vibekit.CmdResumeSession, bind1(r.Chats, CmdResumeSession))
	d.Register(vibekit.CmdSetDraft, bind1(r.Chats, CmdSetDraft))
	d.Register(vibekit.CmdDeleteChat, bind1(r.Chats, CmdDeleteChat))
	d.Register(vibekit.CmdCompact, bind1(r.Bridges, CmdCompact))
	d.Register(vibekit.CmdSteer, bind1(r.Bridges, CmdSteer))
	d.Register(vibekit.CmdSteerClear, bind1(r.Bridges, CmdSteerClear))
	d.Register(vibekit.CmdCreateHook, bind1(r.Workspace, CmdCreateHook))

	d.Register(vibekit.CmdPermissionResponse, bind2(r.Bridges, r.Perms, CmdPermission))
	d.Register(vibekit.CmdElicitationResponse, bind2(r.Bridges, r.Perms, CmdElicitationResponse))
	d.Register(vibekit.CmdUserInputResponse, bind2(r.Bridges, r.Perms, CmdUserInputResponse))
	d.Register(vibekit.CmdRewindChat, bind2(r.Bridges, r.Chats, CmdRewindChat))
	d.Register(vibekit.CmdSetEffort, bind2(r.Bridges, r.Chats, CmdSetEffort))
	d.Register(vibekit.CmdSetMode, bind2(r.Bridges, r.Chats, CmdSetMode))
	d.Register(vibekit.CmdSetSupervisedMode, bind2(r.Bridges, r.Chats, CmdSetSupervisedMode))

	d.Register(vibekit.CmdCancel, bind3(r.Bridges, r.Perms, r.Terminals, CmdCancel))
	d.Register(vibekit.CmdCloseChat, bind3(r.Bridges, r.Chats, r.Perms, CmdCloseChat))
	d.Register(vibekit.CmdForkChat, bind3(r.Bridges, r.Chats, r.Workspace, CmdForkChat))

	d.Register(vibekit.CmdPrompt, bind1(&promptRoles{
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
//
// The binders no longer take the Dispatcher. They threaded it into every handler
// so the handler could call response helpers on it; a handler that returns its
// outcome needs nothing from its router.
//
// ctx is first, as it is everywhere else in Go. It used to sit after the roles,
// which is what every handler's //nolint:revive // context-as-argument was
// suppressing — 19 identical waivers for one avoidable ordering.
func bind1[A any](a A, fn func(context.Context, A, *vibekit.ClientCommand) (any, error)) Handler {
	return func(ctx context.Context, cmd *vibekit.ClientCommand) (any, error) {
		return fn(ctx, a, cmd)
	}
}

// bind2 adapts a two-role handler into the Handler signature.
func bind2[A, B any](a A, b B, fn func(context.Context, A, B, *vibekit.ClientCommand) (any, error)) Handler {
	return func(ctx context.Context, cmd *vibekit.ClientCommand) (any, error) {
		return fn(ctx, a, b, cmd)
	}
}

// bind3 adapts a three-role handler into the Handler signature.
func bind3[A, B, C any](a A, b B, c C, fn func(context.Context, A, B, C, *vibekit.ClientCommand) (any, error)) Handler {
	return func(ctx context.Context, cmd *vibekit.ClientCommand) (any, error) {
		return fn(ctx, a, b, c, cmd)
	}
}
