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

// CmdDeleteChat removes a chat and cascades to rewind children via the
// store's DeleteFamily transition (children first, parent last, per-
// record side-effect teardown through the prepare hook). The response
// is truthful on partial failure: surviving children are reported
// instead of the old unconditional OK, and a failed parent delete is a
// 500 (its children are already gone at that point — retrying deletes
// the parent alone).
func CmdDeleteChat(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	failedChildren, err := d.Chat().ChatStore().DeleteFamily(ctx, cmd.ChatID, func(id api.ChatID) {
		d.Chat().CleanupChatState(ctx, id)
	})
	if err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	slog.Info("chat deleted", "chat_id", cmd.ChatID, "failed_children", len(failedChildren))
	if len(failedChildren) > 0 {
		d.Respond(w, cmd.RequestID, responseWith(map[string]any{
			"failed_children": failedChildren,
		}))
		return
	}
	d.RespondOK(w, cmd.RequestID)
}

// CmdCancel cancels the active turn, if any.
func CmdCancel(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	d.Supervised().FlushPendingForChat(ctx, cmd.ChatID, api.ClearReasonCancelled)
	d.Supervised().SupervisedClearTrust(cmd.ChatID, api.ClearReasonCancelled)
	d.Supervised().ClearPendingPermsForChat(cmd.ChatID)

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
	if err := sb.Respond(ctx, p.RequestID, api.PermissionOutcomeSelected(p.OptionID), nil); err != nil {
		slog.Error("permission response failed", "chat_id", cmd.ChatID, keyError, err)
	}
	d.Supervised().RemovePendingPerm(p.RequestID)
	d.RespondOK(w, cmd.RequestID)
}
