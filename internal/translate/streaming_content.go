package translate

// Content streaming handlers: text chunks, plans, mode updates.

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/vibekit"
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
func (t *Translator) HandleAssistantChunk(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage, isReasoning bool) {
	var chunk ACPChunkWire
	if json.Unmarshal(raw, &chunk) != nil || chunk.Content.Type != vibekit.ContentTypeText || chunk.Content.Text == "" {
		return
	}
	buf := t.deps.BufferStore().GetOrInit(chatID)
	t.ensureTurnStarted(ctx, chatID, buf)

	// A workflow STEP's frames arrive on this chat's connection with an EMPTY
	// agentSubtaskId (KAS stamps that only on tool frames), so without this the
	// step's prose extends the parent agent's trailing block — empty matches
	// empty. The step's own `_meta.kiro.workflow` supplies an instance-unique
	// key instead; see ACPWorkflowMeta.SubtaskID.
	subtask := chunk.Meta.Kiro.AgentSubtaskID
	if wf := chunk.Meta.Kiro.Workflow.SubtaskID(); wf != "" {
		subtask = wf
	}

	// Strip the steering acknowledgement marker before anything reads the text.
	// All three consumers below — the content builder, the block array and the
	// SSE delta — take the same string, so filtering once here covers the live
	// render, the persisted message and the mid-turn reconnect snapshot together.
	//
	// Text only: KAS's own recordSteeringAcks reads the marker from text entries
	// and never from reasoning, and one stream means one carry (see
	// steer_marker.go).
	text := chunk.Content.Text
	if !isReasoning {
		prev, _ := buf.SteerCarry()
		var carry string
		var acks []steerAck
		text, carry, acks = stripSteerAcks(prev, text)
		buf.SetSteerCarry(carry, subtask)
		// BEFORE the empty-text return below, not after: a marker closing a
		// response usually arrives as its own delta, which is exactly the case
		// that returns early, so a broadcast placed after it would never fire
		// for the common shape.
		t.broadcastSteerAcks(ctx, chatID, acks)
		if text == "" {
			// The whole delta is either withheld as a marker candidate or was a
			// marker in full. Returning here rather than broadcasting an empty
			// delta keeps the sequence counter honest and avoids an empty block
			// for text that may never be shown.
			return
		}
	}

	totalLen := buf.Content.Len() + buf.Reasoning.Len()
	if totalLen+len(text) > maxBufferBytes {
		t.announceTruncation(ctx, chatID, buf, subtask, totalLen)
		return
	}
	if isReasoning {
		buf.Reasoning.WriteString(text)
	} else {
		buf.Content.WriteString(text)
	}
	// Mirror the delta into the chronological block array. For runs of
	// same-kind chunks the helper extends the trailing block; a switch
	// from text to thinking (or vice versa) starts a new block. Tool
	// calls between chunks bump the next text run to its own block via
	// HandleToolCall's AppendToolUseBlock call.
	var blockIndex int
	var seq int64
	if isReasoning {
		blockIndex, seq = buf.AppendThinkingDelta(text, subtask)
	} else {
		blockIndex, seq = buf.AppendTextDelta(text, subtask)
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
	t.deps.Broadcast(ctx, vibekit.NewEvent(vibekit.EventMessageChunk, chatID,
		vibekit.MessageChunkPayload{
			MessageID:      buf.MessageID,
			Delta:          text,
			IsReasoning:    isReasoning,
			BlockIndex:     blockIndex,
			Seq:            seq,
			AgentSubtaskID: subtask,
			Refusal:        refusal,
		}))
}

// broadcastSteerAcks reports what the agent said it did about each steer whose
// acknowledgement marker just closed.
//
// It re-broadcasts steer_injected rather than introducing a second event type,
// because the ack is a further fact about a steer the client already tracks by
// id and the store's merge is already keyed on that id. A new event would have
// bought nothing but a second handler and a second wire registration.
//
// Text is deliberately EMPTY on this frame. The steer's own text lives in KAS's
// buffer, not here, so the honest payload carries only what this layer learned;
// the client merges by id and never overwrites the text it already holds.
func (t *Translator) broadcastSteerAcks(ctx context.Context, chatID vibekit.ChatID, acks []steerAck) {
	for _, ack := range acks {
		if ack.SteerID == "" || ack.Text == "" {
			continue
		}
		t.deps.Broadcast(ctx, vibekit.NewEvent(vibekit.EventSteerInjected, chatID, vibekit.SteerInjectedPayload{
			SteerID: ack.SteerID,
			Ack:     ack.Text,
		}))
	}
}

// announceTruncation says once, in both directions, that a turn outgrew the
// buffer and the rest of it is being dropped.
//
// Dropping is the only option — the cap exists so one pathological turn cannot
// OOM the process — but dropping SILENTLY was the defect: the reply stopped
// mid-sentence with nothing in the transcript and nothing in the log to say why,
// which reads as a hang or as the model giving up.
//
// Exactly once, because frames keep arriving after the cap is hit and a notice
// per frame would be a worse defect than the silence it replaced. The marker goes
// where the user is looking and the log line goes where an operator is.
func (t *Translator) announceTruncation(
	ctx context.Context, chatID vibekit.ChatID, buf *buffer.Buffer, subtask string, buffered int,
) {
	if !buf.MarkOverCap() {
		return
	}
	const notice = "\n\n[Reply truncated: this turn exceeded vibekit's 32 MiB buffer.]"
	buf.Content.WriteString(notice)
	blockIndex, seq := buf.AppendTextDelta(notice, subtask)
	slog.Warn("turn exceeded the assistant buffer cap; dropping the remainder",
		"chat_id", chatID, "message_id", buf.MessageID, "buffered_bytes", buffered)
	t.deps.Broadcast(ctx, vibekit.NewEvent(vibekit.EventMessageChunk, chatID,
		vibekit.MessageChunkPayload{
			MessageID:  buf.MessageID,
			Delta:      notice,
			BlockIndex: blockIndex,
			Seq:        seq,
		}))
}

// refusalInfo maps a chunk's _meta.kiro.refusal block to the domain shape.
// The explanation field is dropped — it duplicates the chunk text. A refusal
// block with no category and no recommended model still marks the turn (the
// callout renders without a chip; KAS sends all fields optional).
func refusalInfo(chunk *ACPChunkWire) *vibekit.RefusalInfo {
	r := chunk.Meta.Kiro.Refusal
	if r == nil {
		return nil
	}
	return &vibekit.RefusalInfo{
		Category:         r.Category,
		RecommendedModel: r.RecommendedModel,
	}
}

// HandlePlan persists a plan message directly.
func (t *Translator) HandlePlan(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage) {
	var p ACPPlanWire
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	msg := vibekit.Message{
		ID:   t.newMsgID(),
		Role: vibekit.RoleAssistant,
		Ts:   time.Now().UnixMilli(),
		Plan: p.Entries,
	}
	if err := t.deps.ChatRecords().AppendMessage(ctx, chatID, &msg); err != nil {
		slog.Error("persist plan", "chat_id", chatID, "error", err)
	}
	if ctx.Err() != nil {
		return
	}
	allDone := true
	for _, e := range p.Entries {
		if e.Status != vibekit.PlanCompleted {
			allDone = false
			break
		}
	}
	var plan []vibekit.PlanEntry
	if !allDone {
		plan = p.Entries
	}
	if err := t.deps.ChatRecords().Mutate(ctx, chatID, func(c *vibekit.Chat, ex bool) bool {
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
func (t *Translator) HandleModeUpdate(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage) {
	var p ACPModeUpdateWire
	if json.Unmarshal(raw, &p) != nil || p.ModeID == "" {
		return
	}
	changed := false
	if err := t.deps.ChatRecords().Mutate(ctx, chatID, func(c *vibekit.Chat, ex bool) bool {
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
		t.deps.Broadcast(ctx, vibekit.NewEvent(vibekit.EventModeChanged, chatID, vibekit.ModeChangedPayload{ModeID: p.ModeID}))
	}
}
