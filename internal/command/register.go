package command

import (
	"context"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// RegisterDefaults populates the dispatcher with the standard command
// handlers and returns the membership coordinator it built. Called once from
// Runtime construction.
//
// It is also the role distribution table: every handler's roles are visible on
// its own line, so widening one handler's reach is a diff here and not an
// invisible consequence of a wider host.
//
// The coordinator is RETURNED rather than taken as a role, because it is built
// from four roles plus a teardown composed out of three more, and the composition
// is this file's job. The host needs the value back for retention's two hooks —
// the open-tab predicate and the post-purge close — which are the only callers
// outside this package.
func RegisterDefaults(d *Dispatcher, r *Roles) *Membership {
	// The membership coordinator, constructed here rather than at package scope
	// (go-rulebook §6b): a package-level instance is state independent tests
	// interact through, and one per Runtime is the correct scope anyway.
	//
	// It replaced a bare create ledger shared by the three creating commands.
	// Those three still share it — server minting removes the idempotency a
	// client-minted id gave each of them for free — but the ledger is now inside
	// the coordinator, because resolving an op and reserving a tab slot have to
	// happen in one critical section.
	mem := NewMembership(MembershipDeps{
		Chats:    r.Chats,
		Tabs:     r.Tabs,
		Bus:      r.Bus,
		Teardown: r.Teardown,
		// The chat-tab teardown, composed here from the three roles it needs. The
		// coordinator asks one question — "this chat's work stops now" — and this
		// is the one place that answers it, so close_tab and the retired close_chat
		// command cannot drift apart.
		CloseChat: func(ctx context.Context, chatID vibekit.ChatID) {
			closeChatTeardown(ctx, r.Bridges, r.Perms, r.Teardown, chatID)
		},
	})

	d.Register(vibekit.CmdCreateChat, bind1(mem, CmdCreateChat))
	d.Register(vibekit.CmdResumeSession, bind1(mem, CmdResumeSession))
	d.Register(vibekit.CmdCompact, bind1(r.Bridges, CmdCompact))
	d.Register(vibekit.CmdSteer, bind2(r.Bridges, r.TurnOutcome, CmdSteer))
	d.Register(vibekit.CmdSteerClear, bind1(r.Bridges, CmdSteerClear))
	d.Register(vibekit.CmdCreateHook, bind1(r.Workspace, CmdCreateHook))

	// The four tab commands. Each takes ONLY the coordinator, which is the point:
	// the ordering, the capacity reservation and the event live in one type, so a
	// handler has no way to reach either store and get them in the wrong order.
	d.Register(vibekit.CmdOpenTab, bind1(mem, CmdOpenTab))
	d.Register(vibekit.CmdCloseTab, bind1(mem, CmdCloseTab))
	d.Register(vibekit.CmdReorderTabs, bind1(mem, CmdReorderTabs))
	d.Register(vibekit.CmdPinTab, bind1(mem, CmdPinTab))

	// The two composer writers, side by side because they are one concern: each
	// persists half of what a chat's composer holds and both broadcast the same
	// draft_changed frame.
	d.Register(vibekit.CmdSetDraft, bind2(r.Chats, r.Bus, CmdSetDraft))
	d.Register(vibekit.CmdSetAttachments, bind2(r.Chats, r.Bus, CmdSetAttachments))
	d.Register(vibekit.CmdDeleteChat, bind1(mem, CmdDeleteChat))

	d.Register(vibekit.CmdPermissionResponse, bind2(r.Bridges, r.Perms, CmdPermission))
	d.Register(vibekit.CmdElicitationResponse, bind2(r.Bridges, r.Perms, CmdElicitationResponse))
	d.Register(vibekit.CmdUserInputResponse, bind2(r.Bridges, r.Perms, CmdUserInputResponse))
	d.Register(vibekit.CmdRewindChat, bind2(r.Bridges, r.Chats, CmdRewindChat))
	d.Register(vibekit.CmdSetEffort, bind2(r.Bridges, r.Chats, CmdSetEffort))
	d.Register(vibekit.CmdSetMode, bind3(r.Bridges, r.Chats, r.Bus, CmdSetMode))
	d.Register(vibekit.CmdSetSupervisedMode, bind2(r.Bridges, r.Chats, CmdSetSupervisedMode))

	d.Register(vibekit.CmdCancel, bind3(r.Bridges, r.Perms, r.Terminals, CmdCancel))
	// There is no close_chat registration. The × on a chat tab is close_tab, and
	// the teardown that command used to be is now internal machinery the
	// coordinator calls for every chat tab it closes (see closeChatTeardown).
	d.Register(vibekit.CmdForkChat, bind4(r.Bridges, r.Chats, r.Workspace, mem, CmdForkChat))

	d.Register(vibekit.CmdPrompt, bind1(&promptRoles{
		bridges:     r.Bridges,
		chats:       r.Chats,
		bus:         r.Bus,
		workspace:   r.Workspace,
		lifecycle:   r.Lifecycle,
		mcp:         r.MCP,
		turnOutcome: r.TurnOutcome,
		tokens:      r.Tokens,
	}, CmdPrompt))
	return mem
}

// bind1 adapts a one-role handler into the Handler signature, closing over the
// role it was registered with.
//
// Fixed arities rather than one variadic binder: the type parameters are what
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

// bind4 adapts a four-role handler into the Handler signature. One member:
// fork_chat, which needs the create ledger beside its three roles.
func bind4[A, B, C, D any](a A, b B, c C, d D, fn func(context.Context, A, B, C, D, *vibekit.ClientCommand) (any, error)) Handler {
	return func(ctx context.Context, cmd *vibekit.ClientCommand) (any, error) {
		return fn(ctx, a, b, c, d, cmd)
	}
}
