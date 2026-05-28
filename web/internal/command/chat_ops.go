package command

// Chat CRUD commands: create, delete, cancel, permission forwarding,
// checkpoint restore, and undo.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"vibekit/internal/api"
	"vibekit/internal/checkpoint"
	"vibekit/internal/translate"
)

// ACP method constants used by chat-ops handlers.
const (
	methodCancel = api.MethodCancel
)

// CmdCreateChat creates a new chat with the given metadata.
func CmdCreateChat(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p api.CreateChatCommand
	if len(cmd.Payload) > 0 {
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			d.RespondErr(w, http.StatusBadRequest, errInvalidPayload)
			return
		}
	}
	name := p.Name
	if name == "" {
		name = api.DefaultChatName
	}
	if len(name) > api.MaxChatNameBytes {
		d.RespondErr(w, http.StatusBadRequest, errInvalidPayload)
		return
	}
	if !ValidIdent(p.Agent) || !ValidIdent(p.Model) {
		d.RespondErr(w, http.StatusBadRequest, errInvalidPayload)
		return
	}
	err := deps.ChatStore().Mutate(ctx, cmd.ChatID, func(c *api.Chat, exists bool) bool {
		if exists {
			return false
		}
		c.Name = name
		c.Agent = p.Agent
		c.Model = p.Model
		return true
	})
	if err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	d.RespondOK(w, cmd.RequestID)
}

// CmdDeleteChat removes a chat and cascades to rewind children.
func CmdDeleteChat(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	deps := d.Deps()
	// Delete any rewind children (chats whose parent_chat_id points here).
	var childChats []string
	chatList := deps.ChatStore().List(ctx)
	for i := range chatList {
		hdr := &chatList[i]
		if hdr.ParentChatID == cmd.ChatID {
			childChats = append(childChats, hdr.ID)
		}
	}

	deps.CleanupChatState(ctx, cmd.ChatID)
	if err := deps.ChatStore().Delete(ctx, cmd.ChatID); err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}

	for _, childID := range childChats {
		deps.CleanupChatState(ctx, api.ChatID(childID))
		if err := deps.ChatStore().Delete(ctx, api.ChatID(childID)); err != nil {
			slog.Error("delete chat: cascade child",
				"parent", cmd.ChatID, "child", childID, keyError, err)
		}
	}

	slog.Info("chat deleted", "chat_id", cmd.ChatID, "cascade_children", len(childChats))
	d.RespondOK(w, cmd.RequestID)
}

// CmdCancel cancels the active turn, if any.
func CmdCancel(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	deps := d.Deps()
	deps.FlushPendingForChat(ctx, cmd.ChatID, api.ClearReasonCancelled)
	deps.SupervisedClearTrust(cmd.ChatID, api.ClearReasonCancelled)
	deps.ClearPendingPermsForChat(cmd.ChatID)

	sb := deps.GetBridge(cmd.ChatID)
	if sb == nil {
		d.RespondOK(w, cmd.RequestID)
		return
	}
	if err := sb.Notify(ctx, methodCancel, SessionParams(sb)); err != nil {
		slog.Error("cancel failed", "chat_id", cmd.ChatID, keyError, err)
	}
	d.RespondOK(w, cmd.RequestID)
}

// CmdPermission forwards the user's permission dialog choice to kiro-cli.
func CmdPermission(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	deps := d.Deps()
	sb := deps.GetBridge(cmd.ChatID)
	if sb == nil {
		d.RespondErr(w, http.StatusBadRequest, errNoBridge)
		return
	}
	var p api.PermissionResponseCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		d.RespondErr(w, http.StatusBadRequest, errInvalidPayload)
		return
	}
	if err := sb.Respond(ctx, p.RequestID, translate.PermissionOutcomeSelected(p.OptionID), nil); err != nil {
		slog.Error("permission response failed", "chat_id", cmd.ChatID, keyError, err)
	}
	deps.RemovePendingPerm(p.RequestID)
	d.RespondOK(w, cmd.RequestID)
}

// CmdRestoreCheckpoint rolls the workspace back to the given tag.
func CmdRestoreCheckpoint(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	deps := d.Deps()
	if deps.Checkpoints() == nil {
		api.BadRequest(w, "checkpoints not available")
		return
	}
	var p struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || p.Tag == "" {
		api.BadRequest(w, "tag is required")
		return
	}
	parsedTag, err := checkpoint.ParseTag(p.Tag)
	if err != nil {
		api.BadRequest(w, "invalid tag format")
		return
	}
	messageCount, err := deps.Checkpoints().Restore(ctx, cmd.ChatID, parsedTag)
	if err != nil {
		api.InternalError(w, err)
		return
	}
	mutErr := deps.ChatStore().Mutate(ctx, cmd.ChatID, func(c *api.Chat, exists bool) bool {
		if !exists {
			return false
		}
		if messageCount < 0 {
			messageCount = 0
		}
		if messageCount > len(c.Messages) {
			messageCount = len(c.Messages)
		}
		c.Messages = c.Messages[:messageCount]
		return true
	})
	if mutErr != nil {
		api.InternalError(w, mutErr)
		return
	}
	deps.Broadcast(ctx, api.NewEvent(api.EventCheckpointRestored, cmd.ChatID, api.CheckpointRestoredPayload{
		Tag:          p.Tag,
		MessageCount: messageCount,
	}))
	slog.Info("checkpoint restored", "chat_id", cmd.ChatID, "tag", p.Tag, "messages", messageCount)
	d.Respond(w, cmd.RequestID, map[string]any{
		"ok":            true,
		"tag":           p.Tag,
		"message_count": messageCount,
	})
}

// CmdUndoEdit restores a single file to its contents at the given tag.
func CmdUndoEdit(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	deps := d.Deps()
	if deps.Checkpoints() == nil {
		api.BadRequest(w, "checkpoints not available")
		return
	}
	var p struct {
		Tag      string `json:"tag"`
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || p.Tag == "" || p.FilePath == "" {
		api.BadRequest(w, "tag and file_path are required")
		return
	}
	parsedTag, err := checkpoint.ParseTag(p.Tag)
	if err != nil {
		api.BadRequest(w, "invalid tag format")
		return
	}
	if err := deps.Checkpoints().CheckoutFile(ctx, cmd.ChatID, parsedTag, p.FilePath); err != nil {
		api.InternalError(w, err)
		return
	}
	slog.Info("undo edit", "chat_id", cmd.ChatID, "tag", p.Tag, "file", p.FilePath)
	d.RespondOK(w, cmd.RequestID)
}
