package command

// Chat CRUD commands: create, delete, cancel, permission forwarding,
// checkpoint restore, and undo.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/api"
	chktypes "github.com/cplieger/vibekit/internal/checkpoint/types"
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

// CmdRestoreCheckpoint rolls the workspace back to the given tag.
func CmdRestoreCheckpoint(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	if d.Checkpoint().Checkpoints() == nil {
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
	parsedTag, err := chktypes.ParseTag(p.Tag)
	if err != nil {
		api.BadRequest(w, "invalid tag format")
		return
	}
	release, ok := d.guardIdleBridge(w, cmd.ChatID)
	if !ok {
		return
	}
	defer release()
	messageCount, err := d.Checkpoint().Checkpoints().Restore(ctx, cmd.ChatID, parsedTag)
	if err != nil {
		respondCheckpointErr(w, err)
		return
	}
	mutErr := d.Chat().ChatStore().Mutate(ctx, cmd.ChatID, func(c *api.Chat, exists bool) bool {
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
	d.Chat().Broadcast(ctx, api.NewEvent(api.EventCheckpointRestored, cmd.ChatID, api.CheckpointRestoredPayload{
		Tag:          p.Tag,
		MessageCount: messageCount,
	}))
	slog.Info("checkpoint restored", "chat_id", cmd.ChatID, "tag", p.Tag, "messages", messageCount)
	d.Respond(w, cmd.RequestID, responseWith(map[string]any{
		"tag":           p.Tag,
		"message_count": messageCount,
	}))
}

// CmdUndoEdit restores a single file to its contents at the given tag.
func CmdUndoEdit(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	if d.Checkpoint().Checkpoints() == nil {
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
	parsedTag, err := chktypes.ParseTag(p.Tag)
	if err != nil {
		api.BadRequest(w, "invalid tag format")
		return
	}
	release, ok := d.guardIdleBridge(w, cmd.ChatID)
	if !ok {
		return
	}
	defer release()
	if err := d.Checkpoint().Checkpoints().CheckoutFile(ctx, cmd.ChatID, parsedTag, p.FilePath); err != nil {
		respondCheckpointErr(w, err)
		return
	}
	slog.Info("undo edit", "chat_id", cmd.ChatID, "tag", p.Tag, "file", p.FilePath)
	d.RespondOK(w, cmd.RequestID)
}

// guardIdleBridge rejects a checkpoint mutation with 409 when a turn is
// in flight on the chat's bridge, and otherwise acquires the prompt lock
// so the mutation can't race a concurrently-starting prompt. Restore and
// per-file undo both write the workspace + truncate the transcript, so
// they must not interleave with the agent's writes / the assistant-buffer
// flush at turn_ended (mirrors CmdPrompt's TryAcquireForPrompt guard).
//
// Returns (release, true) when the caller may proceed — release MUST be
// deferred — or (nil, false) when a 409 was already written and the
// caller must return. With no bridge there is no turn to race, so it
// proceeds with a no-op release.
func (d *Dispatcher) guardIdleBridge(w http.ResponseWriter, chatID api.ChatID) (func(), bool) {
	sb := d.Bridge().GetBridge(chatID)
	if sb == nil {
		return func() {}, true
	}
	if !sb.TryAcquireForPrompt() {
		d.RespondErr(w, http.StatusConflict, errBusy)
		return nil, false
	}
	return sb.ReleaseAfterPrompt, true
}

// respondCheckpointErr maps a checkpoint operation error to HTTP status:
// an unknown tag is a 404 (matching the GET checkpoint handlers), any
// other failure is a 500.
func respondCheckpointErr(w http.ResponseWriter, err error) {
	if errors.Is(err, chktypes.ErrTagNotFound) {
		api.NotFound(w, "tag not found")
		return
	}
	api.InternalError(w, err)
}
