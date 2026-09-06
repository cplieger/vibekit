package command

import (
	"context"

	"github.com/cplieger/vibekit/internal/settings"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// RegisterDefaults populates the dispatcher with the standard command
// handlers and returns the membership coordinator it built. Called once
// from Runtime construction.
//
// The coordinator is returned rather than taken as a role, because the
// host needs it back for retention's two hooks — the open-tab predicate
// and the post-purge close — the only callers outside this package.
func RegisterDefaults(d *Dispatcher, r *Roles) *Membership {
	mem := NewMembership(&MembershipDeps{
		Chats:    r.Chats,
		Tabs:     r.Tabs,
		Bus:      r.Bus,
		Teardown: r.Teardown,
		// The chat-tab teardown, composed here from the three roles it
		// needs so close_tab and the retired close_chat command cannot
		// drift apart.
		CloseChat: func(ctx context.Context, chatID vibekit.ChatID) {
			closeChatTeardown(ctx, r.Bridges, r.Perms, r.Teardown, chatID)
		},
		// The delete grade of the same teardown, for a chat the
		// retention-off escalation erased inside the close: everything
		// travels on the captured session chain, since the record is gone
		// by the time this runs.
		DeleteChat: func(ctx context.Context, chatID vibekit.ChatID, sessionChain []string) {
			deleteChatTeardown(ctx, r.Bridges, r.Perms, r.Teardown, chatID, sessionChain)
		},
		// The close path's retention read, failing toward keeping —
		// deliberately not the purge's reader, whose 0-sentinel points the
		// other way.
		Retention: func(ctx context.Context) bool {
			return settings.RetentionEnabled(ctx, r.Workspace.ConfigDir)
		},
		Runs: r.Runs,
	})

	d.Register(vibekit.CmdCreateChat, bind1(mem, CmdCreateChat))
	d.Register(vibekit.CmdResumeSession, bind1(mem, CmdResumeSession))
	d.Register(vibekit.CmdCompact, bind1(r.Bridges, CmdCompact))
	d.Register(vibekit.CmdSteer, bind3(r.Bridges, r.TurnOutcome, r.Steers, CmdSteer))
	d.Register(vibekit.CmdSteerClear, bind1(r.Bridges, CmdSteerClear))
	d.Register(vibekit.CmdCreateHook, bind1(r.Workspace, CmdCreateHook))

	// The four tab commands take only the coordinator: the ordering, the
	// capacity reservation and the event live in one type.
	d.Register(vibekit.CmdOpenTab, bind1(mem, CmdOpenTab))
	d.Register(vibekit.CmdCloseTab, bind1(mem, CmdCloseTab))
	d.Register(vibekit.CmdReorderTabs, bind1(mem, CmdReorderTabs))
	d.Register(vibekit.CmdPinTab, bind1(mem, CmdPinTab))

	// The two composer writers, side by side: each persists half of what
	// a chat's composer holds and both broadcast the same draft_changed
	// frame.
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
	// There is no close_chat registration: the × on a chat tab is
	// close_tab, and the teardown is internal machinery closeChatTeardown.
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

// bind1 adapts a one-role handler into the Handler signature, closing over
// the role it was registered with. Fixed arities rather than one variadic
// binder, so the registration line compiles only when the roles handed
// over are the roles the handler declared.
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
