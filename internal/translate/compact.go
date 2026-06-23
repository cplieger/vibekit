package translate

// _kiro.dev/compaction/status handler.

import (
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/api"
)

// CompactionStatus is a typed string enum for compaction status values.
type CompactionStatus string

// CompactionStarted and the following constants define the valid CompactionStatus values for compaction lifecycle events.
const (
	CompactionStarted   CompactionStatus = "started"
	CompactionCompleted CompactionStatus = "completed"
	CompactionFailed    CompactionStatus = "failed"
)

// contextRecoveryMeta is the typed _meta payload that signals kiro-cli
// to skip this prompt during compaction. The struct ensures the nested
// key names ("kiro", "contextRecovery") are compile-time checked.
type contextRecoveryMeta struct {
	Kiro struct {
		ContextRecovery bool `json:"contextRecovery"`
	} `json:"kiro"`
}

// newContextRecoveryMeta returns the _meta value for context-recovery prompts.
func newContextRecoveryMeta() contextRecoveryMeta {
	var m contextRecoveryMeta
	m.Kiro.ContextRecovery = true
	return m
}

// Valid reports whether s is one of the known compaction statuses.
func (s CompactionStatus) Valid() bool {
	switch s {
	case CompactionStarted, CompactionCompleted, CompactionFailed:
		return true
	}
	return false
}

// HandleCompactionStatus processes compaction status notifications.
func (t *Translator) HandleCompactionStatus(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	p, ok := unmarshalParams[struct {
		Summary *string `json:"summary"`
		Status  struct {
			Type  CompactionStatus `json:"type"`
			Error string           `json:"error"`
		} `json:"status"`
	}](msg, "compaction/status")
	if !ok {
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
		evt := t.newEventMessage(api.EventCompacted, summary)
		if err := t.deps.ChatStore().AppendMessage(ctx, chatID, &evt); err != nil {
			slog.Error("compaction: append event", "chat_id", chatID, "error", err)
		}
		if ctx.Err() != nil {
			return
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
		evt := t.newEventMessage(api.EventCompactFailed, errMsg)
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
		"_meta":  newContextRecoveryMeta(),
	}); err != nil {
		slog.Debug("context recovery: notify failed", "chat_id", chatID, "error", err)
	}
}
