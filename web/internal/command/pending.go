package command

// Command handlers for Supervised mode: resolve pending changes,
// set supervised mode, trust/clear trust.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"vibekit/internal/api"
	"vibekit/internal/pending"
)

// CmdResolvePendingChange settles one staged op.
func CmdResolvePendingChange(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p api.ResolvePendingChangeCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}
	if p.ToolCallID == "" {
		d.RespondErr(w, http.StatusBadRequest, errResolveMissingID)
		return
	}
	switch p.Action {
	case api.PendingActionAccept, api.PendingActionReject:
	default:
		d.RespondErr(w, http.StatusBadRequest, errResolveBadAction)
		return
	}
	snap, err := d.Supervised().PendingStore().Resolve(ctx, p.ToolCallID, p.Action)
	switch {
	case errors.Is(err, pending.ErrUnknown):
		d.RespondErr(w, http.StatusNotFound, errResolveUnknown)
		return
	case err != nil:
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	d.Chat().Broadcast(ctx, api.NewEvent(api.EventPendingChangeResolved, cmd.ChatID, api.PendingChangeResolvedPayload{
		ToolCallID: snap.ToolCallID,
		Action:     p.Action,
		Path:       snap.Path,
	}))
	slog.Info("pending change resolved", "chat_id", cmd.ChatID,
		"tool_call_id", snap.ToolCallID, "path", snap.Path, "action", p.Action)
	d.RespondOK(w, cmd.RequestID)
}

// broadcastResolved emits EventPendingChangeResolved for each snapshot
// in the slice. Single source of truth for the broadcast loop shape.
func broadcastResolved(ctx context.Context, chat ChatAccess, chatID api.ChatID, snaps []api.PendingChange, action api.PendingAction) {
	for _, snap := range snaps {
		chat.Broadcast(ctx, api.NewEvent(api.EventPendingChangeResolved, chatID, api.PendingChangeResolvedPayload{
			ToolCallID: snap.ToolCallID,
			Action:     action,
			Path:       snap.Path,
		}))
	}
}

// CmdResolveAllPendingChanges settles every op in the chat.
func CmdResolveAllPendingChanges(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p api.ResolveAllPendingChangesCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}
	switch p.Action {
	case api.PendingActionAccept, api.PendingActionReject:
	default:
		d.RespondErr(w, http.StatusBadRequest, errResolveBadAction)
		return
	}

	list := d.Supervised().PendingStore().ListForChat(cmd.ChatID)
	if len(list) == 0 {
		d.Respond(w, cmd.RequestID, resolvedResponse{OK: true, Resolved: 0})
		return
	}

	if p.Action == api.PendingActionReject {
		snaps := d.Supervised().PendingStore().RejectAllForChat(cmd.ChatID)
		broadcastResolved(ctx, d.Chat(), cmd.ChatID, snaps, api.PendingActionReject)
		slog.Info("bulk pending changes resolved",
			"chat_id", cmd.ChatID, "action", p.Action, keyResolved, len(snaps))
		d.Respond(w, cmd.RequestID, resolvedResponse{OK: true, Resolved: len(snaps)})
		return
	}

	snaps := d.Supervised().PendingStore().AcceptAllForChat(cmd.ChatID)
	broadcastResolved(ctx, d.Chat(), cmd.ChatID, snaps, api.PendingActionAccept)
	slog.Info("bulk pending changes resolved",
		"chat_id", cmd.ChatID, "action", p.Action, keyResolved, len(snaps))
	d.Respond(w, cmd.RequestID, resolvedResponse{OK: true, Resolved: len(snaps)})
}

// CmdSetSupervisedMode toggles the chat's SupervisedMode flag.
func CmdSetSupervisedMode(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p api.SetSupervisedModeCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}

	var changed bool
	err := d.Chat().ChatStore().Mutate(ctx, cmd.ChatID, func(c *api.Chat, exists bool) bool {
		if !exists {
			return false
		}
		if c.SupervisedMode == p.Enabled {
			return false
		}
		c.SupervisedMode = p.Enabled
		changed = true
		return true
	})
	if err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	if !changed {
		if _, ok := d.Chat().ChatStore().Get(ctx, cmd.ChatID); !ok {
			d.RespondErr(w, http.StatusNotFound, ErrChatNotFound)
			return
		}
		d.RespondOK(w, cmd.RequestID)
		return
	}
	if !p.Enabled {
		d.Supervised().FlushPendingForChat(ctx, cmd.ChatID, api.ClearReasonModeDisabled)
		d.Supervised().SupervisedClearTrust(cmd.ChatID, api.ClearReasonModeDisabled)
	}
	slog.Info("supervised mode toggled",
		"chat_id", cmd.ChatID, "enabled", p.Enabled)
	d.RespondOK(w, cmd.RequestID)
}

// CmdResolvePendingChangePartial settles one staged op with merged text.
func CmdResolvePendingChangePartial(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p api.ResolvePendingChangePartialCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}
	if p.ToolCallID == "" {
		d.RespondErr(w, http.StatusBadRequest, errResolveMissingID)
		return
	}
	if len(p.MergedText) > pending.Cap {
		d.RespondErr(w, http.StatusRequestEntityTooLarge, errMergedTooLarge)
		return
	}
	snap, err := d.Supervised().PendingStore().ResolveWithText(ctx, p.ToolCallID, p.MergedText)
	switch {
	case errors.Is(err, pending.ErrUnknown):
		d.RespondErr(w, http.StatusNotFound, errResolveUnknown)
		return
	case errors.Is(err, pending.ErrMergeNotApplicable):
		d.RespondErr(w, http.StatusBadRequest, err)
		return
	case err != nil:
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	d.Chat().Broadcast(ctx, api.NewEvent(api.EventPendingChangeResolved, cmd.ChatID, api.PendingChangeResolvedPayload{
		ToolCallID: snap.ToolCallID,
		Action:     api.PendingActionAccept,
		Path:       snap.Path,
	}))
	slog.Info("pending change resolved (partial)",
		"chat_id", cmd.ChatID, "tool_call_id", snap.ToolCallID, "path", snap.Path)
	d.RespondOK(w, cmd.RequestID)
}

// CmdTrustPendingChanges enables per-turn trust and accepts all outstanding ops.
func CmdTrustPendingChanges(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	if !d.RequireChatID(w, cmd) {
		return
	}
	if !d.Supervised().ChatInSupervisedMode(ctx, cmd.ChatID) {
		d.RespondErr(w, http.StatusBadRequest,
			errors.New("chat is not in supervised mode"))
		return
	}
	d.Supervised().SupervisedSetTrust(cmd.ChatID)

	// Single-pass batch accept: drain current backlog once. New ops arriving
	// after the trust flag is set will be auto-accepted by the trust mechanism.
	snaps := d.Supervised().PendingStore().AcceptAllForChat(cmd.ChatID)
	broadcastResolved(ctx, d.Chat(), cmd.ChatID, snaps, api.PendingActionAccept)
	slog.Info("trust pending changes",
		"chat_id", cmd.ChatID, "accepted", len(snaps))
	d.Respond(w, cmd.RequestID, resolvedResponse{OK: true, Resolved: len(snaps)})
}

// CmdClearPendingTrust clears the per-turn trust flag.
func CmdClearPendingTrust(d *Dispatcher, _ context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	if !d.RequireChatID(w, cmd) {
		return
	}
	d.Supervised().SupervisedClearTrust(cmd.ChatID, api.ClearReasonUserCleared)
	slog.Info("clear pending trust", "chat_id", cmd.ChatID)
	d.RespondOK(w, cmd.RequestID)
}
