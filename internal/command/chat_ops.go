package command

// Chat CRUD commands: create, delete, cancel, and permission forwarding.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/api"
)

// CmdCreateChat creates a new chat with the given metadata.
func CmdCreateChat(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p api.CreateChatCommand
	if len(cmd.Payload) > 0 {
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
			return
		}
	}
	name := p.Name
	if name == "" {
		name = api.DefaultChatName
	}
	if len(name) > api.MaxChatNameBytes {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}
	if !ValidIdent(p.Model) {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}
	err := d.Chat().ChatStore().Mutate(ctx, cmd.ChatID, func(c *api.Chat, exists bool) bool {
		if exists {
			return false
		}
		c.Name = name
		c.Model = p.Model
		return true
	})
	if err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	d.RespondOK(w, cmd.RequestID)
}

// CmdDeleteChat removes a chat: tear down its side effects, then delete the
// record.
//
// It used to cascade through DeleteFamily, whose whole subject was rewind
// CHILDREN — a chat could own other chats, so deletion needed an ordering
// contract (children first, so no crash window left a child pointing at a
// deleted parent) and a truthful partial-failure response (`failed_children`).
// A rewind reverts the chat it is in now, so no chat owns another and there is
// nothing to order or to half-fail. The transition, its ordering guarantee and
// the `failed_children` response are all gone rather than kept as a
// single-element loop.
func CmdDeleteChat(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	d.Chat().CleanupChatState(ctx, cmd.ChatID)
	if err := d.Chat().ChatStore().Delete(ctx, cmd.ChatID); err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	slog.Info("chat deleted", "chat_id", cmd.ChatID)
	d.RespondOK(w, cmd.RequestID)
}

// CmdCancel cancels the active turn, if any.
func CmdCancel(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	// Only the pending PERMISSIONS are cleared. There is no staging queue to
	// flush and no per-turn trust to drop: KAS owns the write gate, and cancelling
	// a turn reverts its own approval (measured — session/cancel is the documented
	// escape from an unanswered approval and it reverts correctly).
	d.PendingPerms().ClearPendingPermsForChat(cmd.ChatID)

	sb := d.Bridge().GetBridge(cmd.ChatID)
	if sb == nil {
		d.RespondOK(w, cmd.RequestID)
		return
	}
	if err := sb.Notify(ctx, api.MethodCancel, SessionParams(sb)); err != nil {
		slog.Error("cancel failed", "chat_id", cmd.ChatID, keyError, err)
	}
	d.RespondOK(w, cmd.RequestID)
}

// CmdPermission forwards the user's permission dialog choice to kiro-cli.
func CmdPermission(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	sb := d.Bridge().GetBridge(cmd.ChatID)
	if sb == nil {
		d.RespondErr(w, http.StatusBadRequest, errNoBridge)
		return
	}
	var p api.PermissionResponseCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}
	// A turn approval answers on the SAME reply, with per-file decisions in
	// _meta. Built through one helper so the omitted-id-means-reject rule lives in
	// exactly one place.
	outcome := api.PermissionOutcomeWithFileDecisions(p.OptionID, p.FileDecisions)
	if err := sb.Respond(ctx, p.RequestID, outcome, nil); err != nil {
		slog.Error("permission response failed", "chat_id", cmd.ChatID, keyError, err)
	}
	d.PendingPerms().RemovePendingPerm(p.RequestID)
	d.RespondOK(w, cmd.RequestID)
}
