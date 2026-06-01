package translate

// Content streaming handlers: text chunks, plans, mode updates, steering.

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"vibekit/internal/api"
)

// maxBufferBytes caps the per-turn content buffer at 32 MiB. Prevents
// OOM from a pathological agent turn (e.g. cat of a large binary).
// kiro-cli has its own output limits, so this is defense-in-depth.
const maxBufferBytes = 32 << 20

// HandleAssistantChunk streams a text delta to clients and accumulates
// it for later persistence. Reasoning chunks (isReasoning=true) flow
// into buf.Reasoning; regular content chunks flow into buf.Content.
// The IsReasoning flag is forwarded on the SSE so the client routes
// each delta to the correct bubble (reasoning details vs content).
func (t *Translator) HandleAssistantChunk(ctx context.Context, chatID api.ChatID, raw json.RawMessage, isReasoning bool) {
	var chunk ACPChunkWire
	if json.Unmarshal(raw, &chunk) != nil || chunk.Content.Type != api.ContentTypeText || chunk.Content.Text == "" {
		return
	}
	buf := t.deps.BufferStore().GetOrInit(chatID)
	t.ensureTurnStarted(ctx, chatID, buf, true)
	totalLen := buf.Content.Len() + buf.Reasoning.Len()
	if totalLen+len(chunk.Content.Text) > maxBufferBytes {
		return
	}
	if isReasoning {
		buf.Reasoning.WriteString(chunk.Content.Text)
	} else {
		buf.Content.WriteString(chunk.Content.Text)
	}
	// Mirror the delta into the chronological block array. For runs of
	// same-kind chunks the helper extends the trailing block; a switch
	// from text to thinking (or vice versa) starts a new block. Tool
	// calls between chunks bump the next text run to its own block via
	// HandleToolCall's AppendToolUseBlock call.
	var blockIndex int
	if isReasoning {
		blockIndex = buf.AppendThinkingDelta(chunk.Content.Text)
	} else {
		blockIndex = buf.AppendTextDelta(chunk.Content.Text)
	}
	buf.WritePartial(ctx)
	t.deps.Broadcast(ctx, api.NewEvent(api.EventMessageChunk, chatID,
		api.MessageChunkPayload{
			MessageID:   buf.MessageID,
			Delta:       chunk.Content.Text,
			IsReasoning: isReasoning,
			BlockIndex:  blockIndex,
		}))
}

// HandlePlan persists a plan message directly.
func (t *Translator) HandlePlan(ctx context.Context, chatID api.ChatID, raw json.RawMessage) {
	var p ACPPlanWire
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	msg := api.Message{
		ID:   t.newMsgID(),
		Role: api.RoleAssistant,
		Ts:   time.Now().UnixMilli(),
		Plan: p.Entries,
	}
	if err := t.deps.ChatStore().AppendMessage(ctx, chatID, &msg); err != nil {
		slog.Error("persist plan", "chat_id", chatID, "error", err)
	}
	if ctx.Err() != nil {
		return
	}
	allDone := true
	for _, e := range p.Entries {
		if e.Status != api.PlanCompleted {
			allDone = false
			break
		}
	}
	var plan []api.PlanEntry
	if !allDone {
		plan = p.Entries
	}
	if err := t.deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.CurrentPlan = plan
		return true
	}); err != nil {
		slog.Error("persist plan update", "chat_id", chatID, "error", err)
	}
}

// HandleModeUpdate persists the agent's new mode and broadcasts mode_changed.
func (t *Translator) HandleModeUpdate(ctx context.Context, chatID api.ChatID, raw json.RawMessage) {
	var p ACPModeUpdateWire
	if json.Unmarshal(raw, &p) != nil || p.ModeID == "" {
		return
	}
	changed := false
	if err := t.deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
		if !ex || c.CurrentModeID == p.ModeID {
			return false
		}
		c.CurrentModeID = p.ModeID
		changed = true
		return true
	}); err != nil {
		slog.Error("mode update persist", "chat_id", chatID, "error", err)
	}
	if changed {
		t.deps.Broadcast(ctx, api.NewEvent(api.EventModeChanged, chatID, api.ModeChangedPayload{ModeID: p.ModeID}))
	}
}

// HandleSteeringInclusion processes steering_inclusion events.
func (t *Translator) HandleSteeringInclusion(ctx context.Context, chatID api.ChatID, raw json.RawMessage) {
	var p ACPSteeringWire
	if json.Unmarshal(raw, &p) != nil || len(p.Documents) == 0 {
		return
	}
	names := make([]string, 0, len(p.Documents))
	for _, d := range p.Documents {
		name := d.Name
		if name == "" {
			name = d.Path
		}
		if name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return
	}
	t.deps.Broadcast(ctx, api.NewEvent(api.EventSteeringLoaded, chatID, api.SteeringLoadedPayload{Documents: names}))
}
