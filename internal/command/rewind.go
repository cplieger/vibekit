package command

// Rewind lifecycle commands: fork from a historical turn, promote a
// rewind to replace its parent, or discard a rewind and return to the
// parent. The parent is never frozen and there is no merge step — a
// branch is either promoted or discarded.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// CmdRewindChat creates a new chat branched from a specific turn of
// the current chat. Uses kiro-cli's native `session/fork` ACP method
// to fork the ACP session at the given turn index, then persists a
// new vibekit chat with truncated history and `parent_chat_id` set.
//
// The parent is NOT frozen — both chats continue independently.
func CmdRewindChat(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // dispatcher handler signature
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p api.RewindChatCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || p.TurnIndex < 0 {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}

	parent, ok := deps.ChatStore().Get(ctx, cmd.ChatID)
	if !ok {
		d.RespondErr(w, http.StatusNotFound, ErrChatNotFound)
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
		// v3 (KAS) session/fork keys the fork point on _meta.kiro.messageId
		// (a transcript message id, not a turn index) and requires cwd +
		// sessionId. The id space is populated by forwarding the user
		// message id on session/prompt (see BuildPromptParams); KAS only
		// knows the ids vibekit supplied, i.e. user turns.
		//
		// createdReason (kiro-cli 2.16+) labels the child session in KAS's
		// own metadata/roster: human | rewind | subagent | tangent. Stamping
		// "rewind" makes vibekit's branches self-describing to anything
		// reading that roster (KAS's session list, a future run board)
		// instead of looking like a plain user-created session. Ignored by
		// older engines, and unlike "tangent" it does NOT set titleSetByUser
		// — so the agent's own title derivation still applies to the branch.
		resp, err := bridge.Call(ctx, api.MethodSessionFork, SessionParams(bridge, map[string]any{
			"cwd": deps.WorkDir(),
			"_meta": map[string]any{"kiro": map[string]any{
				"messageId":     forkMessageID(parent.Messages, p.TurnIndex),
				"createdReason": api.CreatedReasonRewind,
			}},
		}))
		if err == nil && resp != nil && resp.Result != nil {
			var result struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(resp.Result, &result)
			forkedSessionID = result.SessionID
		} else if err != nil {
			slog.Warn("session/fork failed; rewind starts a fresh session", "chat", cmd.ChatID, keyError, err)
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
		c.CurrentModeID = parent.CurrentModeID
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

// forkMessageID returns the transcript message id KAS should fork at:
// the message at turnIndex, or the nearest preceding user message. KAS
// only knows the ids vibekit supplied on session/prompt (user turns), so
// assistant/event turns resolve back to their originating user message.
// Empty when no user message precedes turnIndex (KAS then forks at the
// session head). Callers guarantee 0 <= turnIndex < len(messages).
func forkMessageID(messages []api.Message, turnIndex int) string {
	for i := turnIndex; i >= 0; i-- {
		if messages[i].Role == api.RoleUser && messages[i].ID != "" {
			return messages[i].ID
		}
	}
	return ""
}

// CmdPromoteRewindChat promotes a rewind chat to replace its parent
// via the store's PromoteRewind transition (validated + cleared under
// one per-chat lock), then deletes the parent. The transition's order
// is crash-safe: an interruption between the clear and the parent
// delete leaves a promoted top-level chat plus a surviving parent
// (benign — the user can delete it) rather than a chat whose
// ParentChatID references a deleted parent. A failed clear aborts the
// promote with an error instead of reporting success while the
// relationship is intact.
func CmdPromoteRewindChat(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // dispatcher handler signature
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}

	parentID, err := deps.ChatStore().PromoteRewind(ctx, cmd.ChatID)
	switch {
	case errors.Is(err, api.ErrChatNotFound):
		d.RespondErr(w, http.StatusNotFound, ErrChatNotFound)
		return
	case errors.Is(err, api.ErrNotRewind):
		d.RespondErr(w, http.StatusBadRequest, errNotRewindChat)
		return
	case err != nil:
		slog.Error("promote rewind: clear parent link", "rewind", cmd.ChatID, keyError, err)
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}

	// Delete the parent. A failure here is benign-but-visible: the
	// promote already took effect (the chat is top-level), so respond OK
	// and log — the surviving parent stays listed and can be deleted
	// manually.
	deps.CleanupChatState(ctx, parentID)
	if err := deps.ChatStore().Delete(ctx, parentID); err != nil {
		slog.Error("promote rewind: delete parent", "parent", parentID, keyError, err)
	}

	slog.Info("rewind promoted", "rewind", cmd.ChatID, "deleted_parent", parentID)
	d.Respond(w, cmd.RequestID, responseWith(nil))
}

// CmdDiscardRewindChat discards a rewind chat and returns to the parent.
// Deletes the rewind chat; the parent is unaffected.
func CmdDiscardRewindChat(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // dispatcher handler signature
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}

	chat, ok := deps.ChatStore().Get(ctx, cmd.ChatID)
	if !ok {
		d.RespondErr(w, http.StatusNotFound, ErrChatNotFound)
		return
	}
	if chat.ParentChatID == "" {
		d.RespondErr(w, http.StatusBadRequest,
			errNotRewindChat)
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

// CmdSetEffort applies a reasoning-effort level to the active session. On
// v3 (KAS) effort is a session config option, so this dispatches
// session/set_config_option (configId "effortLevel"). Vibekit owns
// persistence of the effort level; a new session's initial effort is
// seeded at acp launch via StartOpts.Effort instead.
func CmdSetEffort(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // dispatcher handler signature
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p api.SetEffortCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || !p.Level.Valid() {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}

	bridge := deps.GetBridge(cmd.ChatID)
	if bridge == nil {
		d.RespondErr(w, http.StatusConflict, errNoBridge)
		return
	}

	if _, err := bridge.Call(ctx, api.MethodSetConfigOption, SessionParams(bridge, map[string]any{
		"configId": api.ConfigOptionEffort,
		"value":    string(p.Level),
	})); err != nil {
		slog.Warn("set_effort: bridge call failed", "chat", cmd.ChatID, keyError, err)
		d.RespondErr(w, http.StatusBadGateway, err)
		return
	}

	slog.Info("effort set", "chat", cmd.ChatID, "level", p.Level)
	d.Respond(w, cmd.RequestID, responseWith(map[string]any{"level": p.Level}))
}
