package translate

import (
	"context"
	"encoding/json"
	"log/slog"

	"vibekit/internal/api"
	"vibekit/internal/permissions"
)

// HandlePermissionRequest processes session/request_permission from kiro-cli.
func (t *Translator) HandlePermissionRequest(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var req struct {
		Params struct {
			SessionID string `json:"sessionId"`
			ToolCall  struct {
				ToolCallID string       `json:"toolCallId"`
				Title      string       `json:"title"`
				Kind       api.ToolKind `json:"kind"`
			} `json:"toolCall"`
			Options []api.PermissionOption `json:"options"`
		} `json:"params"`
		ID int64 `json:"id"`
	}
	if json.Unmarshal(msg.Params, &req) != nil {
		return
	}

	subSessionID := ""
	parent := t.deps.ParentACPSession(chatID)
	if req.Params.SessionID != "" && parent != "" && req.Params.SessionID != parent {
		subSessionID = req.Params.SessionID
	}

	// Auto-approve crew permissions when the chat has the flag set.
	if subSessionID != "" && chatID != "" {
		if t.tryAutoApproveCrew(ctx, chatID, subSessionID, req.ID, req.Params.Options, req.Params.ToolCall.Title) {
			return
		}
	}

	// Shell safety tier: auto-approve or auto-reject shell commands.
	if req.Params.ToolCall.Kind == api.ToolKindExecute && subSessionID == "" && t.deps.ConfigDir() != "" {
		if t.tryShellPolicy(ctx, chatID, req.ID, req.Params.ToolCall.Title, req.Params.Options) {
			return
		}
	}

	evt := api.NewEvent(api.EventPermissionNeeded, chatID, api.PermissionNeededPayload{
		RequestID:    req.ID,
		ToolCallID:   req.Params.ToolCall.ToolCallID,
		Title:        req.Params.ToolCall.Title,
		Kind:         req.Params.ToolCall.Kind,
		SubSessionID: subSessionID,
		Options:      req.Params.Options,
	})
	t.deps.Broadcast(ctx, evt)
	t.deps.PendingPermsAdd(req.ID, evt)
	t.deps.Broadcast(ctx, api.NewEvent(api.EventWorkingLabel, chatID, api.WorkingLabelPayload{Label: api.WorkingLabelApproval}))
	t.deps.NotifyPush(ctx, "Permission needed", api.PushKindPermission)
}

// tryAutoApproveCrew auto-approves crew permissions when the chat has
// AutoApproveCrew set. Returns true if the permission was handled.
func (t *Translator) tryAutoApproveCrew(ctx context.Context, chatID api.ChatID, subSessionID string, reqID int64, options []api.PermissionOption, toolTitle string) bool {
	chat, ok := t.deps.ChatStore().Get(ctx, chatID)
	if !ok || !chat.AutoApproveCrew {
		return false
	}
	optionID := FindAllowOnce(options)
	if optionID == "" {
		return false
	}
	if err := t.deps.BridgeRespond(ctx, chatID, reqID, PermissionOutcomeSelected(optionID), nil); err != nil {
		slog.Error("auto-approve crew: respond failed",
			"chat_id", chatID, "error", err)
		return false
	}
	t.deps.PendingPermsRemove(reqID)
	slog.Info("auto-approved crew permission",
		"chat_id", chatID,
		"sub_session", subSessionID,
		"tool", toolTitle)
	return true
}

// tryShellPolicy evaluates the shell safety tier and auto-approves or
// auto-rejects shell commands. Returns true if the permission was handled.
func (t *Translator) tryShellPolicy(ctx context.Context, chatID api.ChatID, reqID int64, command string, options []api.PermissionOption) bool {
	decision := permissions.EvaluateShellCommand(ctx, t.deps.ConfigDir(), command, t.deps.PermissionRules())
	switch decision {
	case permissions.ShellAllow:
		optionID := FindAllowOnce(options)
		if optionID == "" {
			return false
		}
		if err := t.deps.BridgeRespond(ctx, chatID, reqID, PermissionOutcomeSelected(optionID), nil); err == nil {
			slog.Info("shell policy auto-approved", "chat_id", chatID, "command", command)
			return true
		}
	case permissions.ShellDeny:
		if err := t.deps.BridgeRespond(ctx, chatID, reqID, PermissionOutcomeCancelled(), nil); err != nil {
			slog.Error("shell policy deny: respond failed", "chat_id", chatID, "error", err)
		}
		slog.Info("shell policy denied", "chat_id", chatID, "command", command)
		return true
	}
	return false
}
