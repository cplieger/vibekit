package command

// Tangent (fork/merge/discard) lifecycle commands.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"vibekit/internal/api"
)

// CmdForkChat creates a tangent (side conversation) from the current chat.
func CmdForkChat(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) {
	deps := d.Deps()
	p, ok := parseForkPayload(d, w, cmd)
	if !ok {
		return
	}
	parent, ok := fetchForkParent(d, ctx, w, deps, cmd.ChatID)
	if !ok {
		return
	}

	var collision bool
	err := deps.ChatStore().Mutate(ctx, api.ChatID(p.TangentID), func(c *api.Chat, exists bool) bool {
		if exists {
			collision = true
			return false
		}
		name := TruncateRunes(parent.Name, 30)
		if name != parent.Name {
			name += ellipsis
		}
		c.Name = "Tangent: " + name
		if len(c.Name) > api.MaxChatNameBytes {
			c.Name = c.Name[:api.MaxChatNameBytes]
		}
		c.Agent = parent.Agent
		c.Model = parent.Model
		c.IsTangent = true
		c.ParentChatID = cmd.ChatID
		return true
	})
	if err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	if collision {
		d.RespondErr(w, http.StatusConflict,
			errors.New("tangent id already in use"))
		return
	}

	var frozen bool
	err = deps.ChatStore().Mutate(ctx, cmd.ChatID, func(c *api.Chat, exists bool) bool {
		if !exists {
			return false
		}
		c.Frozen = true
		frozen = true
		return true
	})
	if err != nil || !frozen {
		if delErr := deps.ChatStore().Delete(ctx, api.ChatID(p.TangentID)); delErr != nil {
			slog.Error("fork: tangent rollback failed",
				"tangent", p.TangentID, keyError, delErr)
		}
		if err != nil {
			d.RespondErr(w, http.StatusInternalServerError, err)
			return
		}
		d.RespondErr(w, http.StatusNotFound,
			errors.New("parent chat was deleted during fork"))
		return
	}

	slog.Info("tangent created", "parent", cmd.ChatID, "tangent", p.TangentID)
	d.Respond(w, cmd.RequestID, map[string]any{"ok": true, "tangent_id": p.TangentID})
}

// parseForkPayload unmarshals and validates the ForkChat request body.
// Returns (payload, true) on success, or ({}, false) after writing a
// 4xx error response.
func parseForkPayload(d *Dispatcher, w http.ResponseWriter, cmd *api.ClientCommand) (api.ForkChatCommand, bool) {
	if !d.RequireChatID(w, cmd) {
		return api.ForkChatCommand{}, false
	}
	var p api.ForkChatCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || p.TangentID == "" {
		d.RespondErr(w, http.StatusBadRequest, errInvalidPayload)
		return api.ForkChatCommand{}, false
	}
	if !validChatID(api.ChatID(p.TangentID)) {
		d.RespondErr(w, http.StatusBadRequest, errInvalidPayload)
		return api.ForkChatCommand{}, false
	}
	if api.ChatID(p.TangentID) == cmd.ChatID {
		d.RespondErr(w, http.StatusBadRequest, errors.New("tangent_id must differ from chat_id"))
		return api.ForkChatCommand{}, false
	}
	return p, true
}

// fetchForkParent loads the parent chat and enforces the precondition
// (not frozen, not itself a tangent). Returns the parent and true on
// success, or nil+false after writing a 4xx/5xx response.
func fetchForkParent(d *Dispatcher, ctx context.Context, w http.ResponseWriter, deps Dependencies, chatID api.ChatID) (*api.Chat, bool) {
	parent, ok := deps.ChatStore().Get(ctx, chatID)
	if !ok {
		d.RespondErr(w, http.StatusNotFound, errChatNotFound)
		return nil, false
	}
	if parent.Frozen {
		d.RespondErr(w, http.StatusConflict, errors.New("chat already has an active tangent"))
		return nil, false
	}
	if parent.IsTangent {
		d.RespondErr(w, http.StatusConflict, errors.New("cannot fork a tangent chat"))
		return nil, false
	}
	return parent, true
}

// CmdMergeTangent merges the last Q&A pair from the tangent back to the parent.
func CmdMergeTangent(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) {
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}
	tangent, ok := deps.ChatStore().Get(ctx, cmd.ChatID)
	if !ok {
		d.RespondErr(w, http.StatusNotFound, errChatNotFound)
		return
	}
	if !tangent.IsTangent || tangent.ParentChatID == "" {
		d.RespondErr(w, http.StatusBadRequest, errors.New("not a tangent chat"))
		return
	}

	parentID := tangent.ParentChatID
	if _, parentOK := deps.ChatStore().Get(ctx, parentID); !parentOK {
		d.RespondErr(w, http.StatusNotFound,
			errors.New("parent chat no longer exists"))
		return
	}

	if !MergeLastExchange(deps, ctx, parentID, tangent.Messages) {
		d.RespondErr(w, http.StatusBadRequest,
			errors.New("tangent has no complete user+assistant exchange to merge"))
		return
	}

	if err := deps.ChatStore().Mutate(ctx, parentID, func(c *api.Chat, exists bool) bool {
		if !exists {
			return false
		}
		c.Frozen = false
		return true
	}); err != nil {
		slog.Error("merge tangent: unfreeze parent", "parent", parentID, keyError, err)
	}

	deps.CleanupChatState(ctx, cmd.ChatID)
	if err := deps.ChatStore().Delete(ctx, cmd.ChatID); err != nil {
		slog.Error("merge tangent: delete tangent", "tangent", cmd.ChatID, keyError, err)
	}

	slog.Info("tangent merged", "tangent", cmd.ChatID, "parent", parentID)
	d.Respond(w, cmd.RequestID, map[string]any{"ok": true, "parent_id": parentID})
}

// CmdDiscardTangent discards the tangent and unfreezes the parent.
func CmdDiscardTangent(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) {
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}
	tangent, ok := deps.ChatStore().Get(ctx, cmd.ChatID)
	if !ok {
		d.RespondErr(w, http.StatusNotFound, errChatNotFound)
		return
	}
	if !tangent.IsTangent || tangent.ParentChatID == "" {
		d.RespondErr(w, http.StatusBadRequest, errors.New("not a tangent chat"))
		return
	}

	parentID := tangent.ParentChatID

	if err := deps.ChatStore().Mutate(ctx, parentID, func(c *api.Chat, exists bool) bool {
		if !exists {
			return false
		}
		c.Frozen = false
		return true
	}); err != nil {
		slog.Error("discard tangent: unfreeze parent", "parent", parentID, keyError, err)
	}

	deps.CleanupChatState(ctx, cmd.ChatID)
	if err := deps.ChatStore().Delete(ctx, cmd.ChatID); err != nil {
		slog.Error("discard tangent: delete tangent", "tangent", cmd.ChatID, keyError, err)
	}

	slog.Info("tangent discarded", "tangent", cmd.ChatID, "parent", parentID)
	d.Respond(w, cmd.RequestID, map[string]any{"ok": true, "parent_id": parentID})
}

// MergeLastExchange finds the last user + assistant message pair and
// appends them to the target chat.
func MergeLastExchange(deps Dependencies, ctx context.Context, targetChatID api.ChatID, msgs []api.Message) bool {
	var lastUser, lastAssistant *api.Message
	for i := range slices.Backward(msgs) {
		m := &msgs[i]
		if m.Role == api.RoleAssistant && lastAssistant == nil {
			lastAssistant = m
		}
		if m.Role == api.RoleUser && lastUser == nil {
			lastUser = m
		}
		if lastUser != nil && lastAssistant != nil {
			break
		}
	}
	if lastUser == nil || lastAssistant == nil {
		return false
	}
	var appended bool
	var userCopy, assistantCopy api.Message
	err := deps.ChatStore().Mutate(ctx, targetChatID, func(c *api.Chat, exists bool) bool {
		if !exists {
			return false
		}
		now := time.Now().UnixMilli()
		userTs := now
		if n := len(c.Messages); n > 0 && c.Messages[n-1].Ts >= userTs {
			userTs = c.Messages[n-1].Ts + 1
		}
		userCopy = *lastUser
		userCopy.Ts = userTs
		assistantCopy = *lastAssistant
		assistantCopy.Ts = userTs + 1
		c.Messages = append(c.Messages, userCopy, assistantCopy)
		appended = true
		return true
	})
	if err != nil {
		slog.Error("merge exchange: mutate parent", "target", targetChatID, keyError, err)
		return false
	}
	if !appended {
		return false
	}
	deps.Broadcast(ctx, api.NewEvent(api.EventMessageAppended, targetChatID, &userCopy))
	deps.Broadcast(ctx, api.NewEvent(api.EventMessageAppended, targetChatID, &assistantCopy))
	return true
}

// TruncateRunes truncates s to at most n runes.
func TruncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
