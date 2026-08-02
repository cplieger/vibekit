package translate

// Content streaming handlers: text chunks, plans, mode updates.

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/cplieger/vibekit/internal/api"
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
	t.ensureTurnStarted(ctx, chatID, buf)
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
	var seq int64
	if isReasoning {
		blockIndex, seq = buf.AppendThinkingDelta(chunk.Content.Text, chunk.Meta.Kiro.AgentSubtaskID)
	} else {
		blockIndex, seq = buf.AppendTextDelta(chunk.Content.Text, chunk.Meta.Kiro.AgentSubtaskID)
	}
	// A model refusal (kiro-cli 2.13): the explanation is this chunk's text;
	// the update-level _meta.kiro.refusal classifies it. Stamp the buffer so
	// the metadata persists onto the final assistant message, and forward it
	// on the chunk payload so the live renderer styles the callout
	// immediately. Refusals are parent-agent text (never reasoning), but
	// gate anyway so a stray tagged thought can't mark the turn.
	refusal := refusalInfo(&chunk)
	if refusal != nil && !isReasoning {
		buf.SetRefusal(refusal)
	} else {
		refusal = nil
	}
	t.deps.Broadcast(ctx, api.NewEvent(api.EventMessageChunk, chatID,
		api.MessageChunkPayload{
			MessageID:      buf.MessageID,
			Delta:          chunk.Content.Text,
			IsReasoning:    isReasoning,
			BlockIndex:     blockIndex,
			Seq:            seq,
			AgentSubtaskID: chunk.Meta.Kiro.AgentSubtaskID,
			Refusal:        refusal,
		}))
}

// refusalInfo maps a chunk's _meta.kiro.refusal block to the domain shape.
// The explanation field is dropped — it duplicates the chunk text. A refusal
// block with no category and no recommended model still marks the turn (the
// callout renders without a chip; KAS sends all fields optional).
func refusalInfo(chunk *ACPChunkWire) *api.RefusalInfo {
	r := chunk.Meta.Kiro.Refusal
	if r == nil {
		return nil
	}
	return &api.RefusalInfo{
		Category:         r.Category,
		RecommendedModel: r.RecommendedModel,
	}
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
