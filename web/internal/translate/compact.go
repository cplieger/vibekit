package translate

// _kiro.dev/compaction/status handler.

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"vibekit/internal/api"
)

// CompactionStatus is a typed string enum for compaction status values.
type CompactionStatus string

const (
	CompactionStarted   CompactionStatus = "started"
	CompactionCompleted CompactionStatus = "completed"
	CompactionFailed    CompactionStatus = "failed"
)

// HandleCompactionStatus processes compaction status notifications.
func (t *Translator) HandleCompactionStatus(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var p struct {
		Summary *string `json:"summary"`
		Status  struct {
			Type  CompactionStatus `json:"type"`
			Error string           `json:"error"`
		} `json:"status"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return
	}
	switch p.Status.Type {
	case CompactionStarted:
		t.deps.Broadcast(ctx, api.NewEvent(api.EventCompactionStarted, chatID, api.CompactionStartedPayload{}))
	case CompactionCompleted:
		summary := ""
		if p.Summary != nil {
			summary = *p.Summary
		}
		evt := api.Message{
			ID:        t.deps.NewMessageID(),
			Role:      api.RoleEvent,
			Ts:        time.Now().UnixMilli(),
			EventKind: api.EventCompacted,
			Content:   summary,
		}
		if err := t.deps.ChatStore().AppendMessage(ctx, chatID, &evt); err != nil {
			slog.Error("compaction: append event", "chat_id", chatID, "error", err)
		}
		if err := t.deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
			if !ex {
				return false
			}
			c.CompactionWatermark = evt.ID
			return true
		}); err != nil {
			slog.Error("compaction: set watermark", "chat_id", chatID, "error", err)
		}
		t.injectContextRecovery(ctx, chatID)
	case CompactionFailed:
		errMsg := p.Status.Error
		if errMsg == "" {
			errMsg = "compaction failed"
		}
		evt := api.Message{
			ID:        t.deps.NewMessageID(),
			Role:      api.RoleEvent,
			Ts:        time.Now().UnixMilli(),
			EventKind: api.EventCompactFailed,
			Content:   errMsg,
		}
		if err := t.deps.ChatStore().AppendMessage(ctx, chatID, &evt); err != nil {
			slog.Error("compaction: append failed event", "chat_id", chatID, "error", err)
		}
		t.deps.Broadcast(ctx, api.NewEvent(api.EventError, chatID, api.ErrorPayload{Code: api.ErrCodeCompactionFailed, Message: errMsg}))
	default:
		slog.Warn("compaction: unknown status type", "type", p.Status.Type, "chat_id", chatID)
	}
}

// injectContextRecovery sends a lightweight orientation block to the
// bridge after compaction so the model has workspace context.
func (t *Translator) injectContextRecovery(ctx context.Context, chatID api.ChatID) {
	chat, chatOK := t.deps.ChatStore().Get(ctx, chatID)
	if !chatOK {
		return
	}
	block := "[Context recovery after compaction]\n" +
		"Working directory: " + t.deps.WorkDir() + "\n" +
		"Chat: " + chat.Name + "\n" +
		"Agent: " + chat.Agent + "\n" +
		"Model: " + chat.Model
	if err := t.deps.BridgeNotify(ctx, chatID, api.MethodPrompt, map[string]any{
		"prompt": []map[string]any{api.TextBlock(block)},
		"_meta":  map[string]any{"kiro": map[string]any{"contextRecovery": true}},
	}); err != nil {
		slog.Debug("context recovery: notify failed", "chat_id", chatID, "error", err)
	}
}
