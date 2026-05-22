package translate

// Subagent session notifications: activity, list_update, inbox.

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"vibekit/internal/api"
)

// HandleSessionActivity routes per-subagent activity events.
func (t *Translator) HandleSessionActivity(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var p struct {
		Event     map[string]any `json:"event"`
		SessionID string         `json:"sessionId"`
	}
	if json.Unmarshal(msg.Params, &p) != nil || p.SessionID == "" {
		return
	}
	parent := t.deps.ParentACPSession(chatID)
	if p.SessionID == parent {
		return
	}
	t.deps.Broadcast(ctx, api.NewEvent(api.EventSubagentActivity, chatID, api.SubagentActivityPayload{
		SubSessionID: p.SessionID,
		Event:        p.Event,
	}))
}

// HandleSessionListUpdate receives the full session inventory.
func (t *Translator) HandleSessionListUpdate(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var p struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if json.Unmarshal(msg.Params, &p) != nil {
		return
	}
	t.deps.Broadcast(ctx, api.NewEvent(api.EventSessionListUpdated, chatID, api.SessionListUpdatedPayload{Sessions: p.Sessions}))
}

// HandleInboxNotification receives subagent-to-parent messages.
func (t *Translator) HandleInboxNotification(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var p map[string]any
	if json.Unmarshal(msg.Params, &p) != nil {
		return
	}
	fromSession, _ := p["fromSessionId"].(string)
	content, _ := p["content"].(string)          
	if content == "" {
		raw, err := json.Marshal(p)
		if err == nil {
			content = string(raw)
		}
	}
	evt := api.Message{
		ID:        NewMessageID(),
		Role:      api.RoleEvent,
		Ts:        time.Now().UnixMilli(),
		EventKind: api.EventInbox,
		Content:   content,
	}
	if err := t.deps.ChatStore().AppendMessage(ctx, chatID, &evt); err != nil {
		slog.Error("inbox: append event", "chat_id", chatID, "error", err)
		return
	}
	_ = fromSession // available for future per-subagent attribution
}
