package command

// Rewind lifecycle commands: fork from a historical turn, promote a
// rewind to replace its parent, or discard a rewind and return to the
// parent. Replaces the old tangent system (fork/merge/discard) with a
// simpler model: no parent freeze, no merge — just branch + promote
// or discard.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"vibekit/internal/api"
)

// CmdRewindChat creates a new chat branched from a specific turn of
// the current chat. Uses kiro-cli's native `session/fork` ACP method
// to fork the ACP session at the given turn index, then persists a
// new vibekit chat with truncated history and `parent_chat_id` set.
//
// Unlike the old tangent system, the parent is NOT frozen — both
// chats continue independently.
func CmdRewindChat(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand)  { //nolint:revive // dispatcher handler signature
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p api.RewindChatCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || p.TurnIndex < 0 {
		d.RespondErr(w, http.StatusBadRequest, errInvalidPayload)
		return
	}

	parent, ok := deps.ChatStore().Get(ctx, cmd.ChatID)
	if !ok {
		d.RespondErr(w, http.StatusNotFound, errChatNotFound)
		return
	}

	// Validate turn index is within bounds.
	if p.TurnIndex >= len(parent.Messages) {
		d.RespondErr(w, http.StatusBadRequest,
			errors.New("turn_index out of range"))
		return
	}

	// Truncate messages to the specified turn (inclusive).
	truncated := make([]api.Message, p.TurnIndex+1)
	copy(truncated, parent.Messages[:p.TurnIndex+1])

	// Try session/fork on the parent's bridge for ACP session continuity.
	// If it succeeds, the new chat gets the forked session ID so its
	// bridge can session/load it (model "remembers" earlier turns).
	var forkedSessionID string
	bridge := deps.GetBridge(cmd.ChatID)
	if bridge != nil {
		resp, err := bridge.Call(ctx, "session/fork", map[string]any{
			"turnIndex": p.TurnIndex,
		})
		if err == nil && resp != nil && resp.Result != nil {
			var result struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(resp.Result, &result)
			forkedSessionID = result.SessionID
		}
	}

	// Create the rewind chat with truncated history.
	rewindID := api.NewChatID()
	now := time.Now().UnixMilli()
	err := deps.ChatStore().Mutate(ctx, rewindID, func(c *api.Chat, exists bool) bool {
		if exists {
			return false
		}
		c.Name = "Rewind: " + TruncateRunes(parent.Name, 25)
		if len(c.Name) > api.MaxChatNameBytes {
			c.Name = c.Name[:api.MaxChatNameBytes]
		}
		c.Agent = parent.Agent
		c.Model = parent.Model
		c.ACPSessionID = forkedSessionID
		c.ParentChatID = cmd.ChatID
		c.RewindFromTurn = p.TurnIndex
		c.Messages = truncated
		c.MessageCount = len(truncated)
		c.CreatedAt = now
		c.UpdatedAt = now
		return true
	})
	if err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}

	slog.Info("rewind chat created",
		"parent", cmd.ChatID, "rewind", rewindID, "turn", p.TurnIndex)
	d.Respond(w, cmd.RequestID, map[string]any{
		"ok":        true,
		"rewind_id": string(rewindID),
	})
}

// CmdPromoteRewindChat promotes a rewind chat to replace its parent.
// Deletes the parent chat and clears the rewind's parent_chat_id so
// it becomes a top-level chat.
func CmdPromoteRewindChat(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand)  { //nolint:revive // dispatcher handler signature
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}

	chat, ok := deps.ChatStore().Get(ctx, cmd.ChatID)
	if !ok {
		d.RespondErr(w, http.StatusNotFound, errChatNotFound)
		return
	}
	if chat.ParentChatID == "" {
		d.RespondErr(w, http.StatusBadRequest,
			errors.New("not a rewind chat (no parent)"))
		return
	}

	parentID := chat.ParentChatID

	// Delete the parent.
	deps.CleanupChatState(ctx, parentID)
	if err := deps.ChatStore().Delete(ctx, parentID); err != nil {
		slog.Error("promote rewind: delete parent", "parent", parentID, keyError, err)
		// Continue — the promote still succeeds even if parent cleanup
		// partially fails (orphaned files are cleaned by GC).
	}

	// Clear parent_chat_id on self.
	_ = deps.ChatStore().Mutate(ctx, cmd.ChatID, func(c *api.Chat, exists bool) bool {
		if !exists {
			return false
		}
		c.ParentChatID = ""
		c.RewindFromTurn = 0
		return true
	})

	slog.Info("rewind promoted", "rewind", cmd.ChatID, "deleted_parent", parentID)
	d.Respond(w, cmd.RequestID, map[string]any{"ok": true})
}

// CmdDiscardRewindChat discards a rewind chat and returns to the parent.
// Deletes the rewind chat; the parent is unaffected (it was never frozen).
func CmdDiscardRewindChat(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand)  { //nolint:revive // dispatcher handler signature
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}

	chat, ok := deps.ChatStore().Get(ctx, cmd.ChatID)
	if !ok {
		d.RespondErr(w, http.StatusNotFound, errChatNotFound)
		return
	}
	if chat.ParentChatID == "" {
		d.RespondErr(w, http.StatusBadRequest,
			errors.New("not a rewind chat (no parent)"))
		return
	}

	parentID := chat.ParentChatID

	deps.CleanupChatState(ctx, cmd.ChatID)
	if err := deps.ChatStore().Delete(ctx, cmd.ChatID); err != nil {
		slog.Error("discard rewind: delete", "rewind", cmd.ChatID, keyError, err)
	}

	slog.Info("rewind discarded", "rewind", cmd.ChatID, "parent", parentID)
	d.Respond(w, cmd.RequestID, map[string]any{
		"ok":        true,
		"parent_id": string(parentID),
	})
}

// CmdSetEffort dispatches the /effort slash command to kiro-cli via
// the running bridge's _kiro.dev/commands/execute path. Vibekit owns
// persistence of the effort level; this command applies it to the
// active session.
func CmdSetEffort(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand)  { //nolint:revive // dispatcher handler signature
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p api.SetEffortCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || !p.Level.Valid() {
		d.RespondErr(w, http.StatusBadRequest, errInvalidPayload)
		return
	}

	bridge := deps.GetBridge(cmd.ChatID)
	if bridge == nil {
		d.RespondErr(w, http.StatusConflict,
			errors.New("no active bridge for this chat"))
		return
	}

	// Dispatch /effort via the internal slash-execute path.
	result, err := bridge.Call(ctx, "_kiro.dev/commands/execute", map[string]any{
		keyCommand: map[string]any{
			keyCommand: "effort",
			"args":     []string{string(p.Level)},
		},
	})
	if err != nil {
		slog.Warn("set_effort: bridge call failed", "chat", cmd.ChatID, keyError, err)
		d.RespondErr(w, http.StatusBadGateway, err)
		return
	}
	_ = result // success; kiro-cli applies the effort level to the session

	slog.Info("effort set", "chat", cmd.ChatID, "level", p.Level)
	d.Respond(w, cmd.RequestID, map[string]any{"ok": true, "level": p.Level})
}
