package translate

// Content streaming handlers: text chunks, plans, mode updates.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/durable"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// emit publishes an event describing frames that folded into buf, unless that
// turn is muted (buffer.Buffer.muted owns why). One funnel rather than a check
// per broadcast site: an event not describing folded content goes to the bus.
func (t *Translator) emit(ctx context.Context, buf *buffer.Buffer, evt vibekit.ServerEvent) {
	if buf != nil && buf.Muted() {
		return
	}
	t.bus.Broadcast(ctx, evt)
}

// maxBufferBytes caps the per-turn content buffer, so a pathological turn (a cat
// of a large binary) cannot OOM the process. kiro-cli has its own limits too.
const maxBufferBytes = 32 << 20

// HandleAssistantChunk streams a text delta to clients and accumulates it for
// later persistence. isReasoning selects buf.Reasoning over buf.Content and is
// forwarded on the SSE so the client routes the delta to the right bubble.
func (t *Translator) HandleAssistantChunk(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage, isReasoning bool) {
	var chunk ACPChunkWire
	if json.Unmarshal(raw, &chunk) != nil || chunk.Content.Type != vibekit.ContentTypeText || chunk.Content.Text == "" {
		return
	}
	// Must run before the buffer is read: a revision hands the folded buffer to
	// the agent's own turn and gives the pre-open a fresh one, so a target taken
	// first would fold this frame into the wrong turn.
	if chunk.Meta.Kiro.AgentInitiated {
		t.turns.ReviseTurnBinding(ctx, chatID)
	}
	// A step's frames arrive with an EMPTY agentSubtaskId (KAS stamps that only on
	// tool frames), so without this the step's prose extends the parent agent's
	// trailing block. Read BEFORE the fold: it also decides what kind of turn a
	// fold with no open turn opens, and the FRAME's marker survives a cold registry.
	subtask := chunk.Meta.Kiro.AgentSubtaskID
	workflowSubtask := chunk.Meta.Kiro.Workflow.SubtaskID()
	if workflowSubtask != "" {
		subtask = workflowSubtask
	}

	buf := t.buffers.TurnFoldTarget(ctx, chatID, foldSource(workflowSubtask != ""))
	t.ensureTurnStarted(ctx, chatID, buf)

	// kiro-cli's security filter cancelled a tool call: this chunk is the whole
	// notice and no session/prompt response is coming. Detected before the steer
	// filter so the two never eat each other's text. Skipped for a step frame,
	// where vibekit issued no prompt call to release; the run ceiling catches it.
	interrupted := !isReasoning && workflowSubtask == "" && isInterruptSentinel(chunk.Content.Text)

	// Strip the steering acknowledgement marker before anything reads the text:
	// all three consumers below take the same string. Text only, because KAS's
	// recordSteeringAcks reads the marker from text entries and never reasoning.
	text := chunk.Content.Text
	if !isReasoning {
		prev, _ := buf.SteerCarry()
		var carry string
		var acks []steerAck
		text, carry, acks = stripSteerAcks(prev, text)
		buf.SetSteerCarry(carry, subtask)
		// BEFORE the empty-text return below: a marker closing a response usually
		// arrives as its own delta, which is exactly the case that returns early.
		t.broadcastSteerAcks(ctx, chatID, buf, acks)
		if text == "" {
			// The delta was withheld as a marker candidate or was a marker in full.
			// Returning keeps the sequence counter honest and adds no empty block.
			return
		}
	}

	totalLen := buf.BufferedBytes()
	if totalLen+len(text) > maxBufferBytes {
		t.announceTruncation(ctx, chatID, buf, subtask, totalLen)
		return
	}
	// Mirror the delta into the block array, which also accumulates it into the
	// turn's builder. A run of same-kind chunks extends this subtask's newest
	// block; a text/thinking switch, or an intervening tool call, starts a new one.
	var blockIndex int
	var seq int64
	if isReasoning {
		blockIndex, seq = buf.AppendThinkingDelta(text, subtask)
	} else {
		blockIndex, seq = buf.AppendTextDelta(text, subtask)
	}
	// A refusal's explanation is this chunk's text and _meta.kiro.refusal
	// classifies it. Stamp the buffer so it persists, forward it so the callout
	// styles live. Gated on !isReasoning so a stray tagged thought cannot mark it.
	refusal := refusalInfo(&chunk)
	if refusal != nil && !isReasoning {
		buf.SetRefusal(refusal)
	} else {
		refusal = nil
	}
	t.emit(ctx, buf, vibekit.NewEvent(vibekit.EventMessageChunk, chatID,
		vibekit.MessageChunkPayload{
			MessageID:      buf.MessageID,
			Delta:          text,
			IsReasoning:    isReasoning,
			BlockIndex:     blockIndex,
			Seq:            seq,
			AgentSubtaskID: subtask,
			Refusal:        refusal,
		}))

	// Last, and the ordering is the contract: the host's teardown takes the
	// buffer, so the notice must be in it and on the wire before the turn ends.
	if interrupted {
		slog.Warn("kiro-cli interrupted its own tool use; ending the turn",
			"chat_id", chatID, "reason", interruptReason)
		t.turnInterrupt.InterruptTurn(chatID, interruptReason)
	}
}

// broadcastSteerAcks reports what the agent said it did about each steer whose
// acknowledgement marker just closed. Re-broadcasts steer_injected rather than a
// new type, since the ack is a further fact about a steer the client tracks by
// id; Text is empty because the client already holds it and must not lose it.
func (t *Translator) broadcastSteerAcks(ctx context.Context, chatID vibekit.ChatID, buf *buffer.Buffer, acks []steerAck) {
	for _, ack := range acks {
		if ack.SteerID == "" || ack.Text == "" {
			continue
		}
		t.emit(ctx, buf, vibekit.NewEvent(vibekit.EventSteerInjected, chatID, vibekit.SteerInjectedPayload{
			SteerID: ack.SteerID,
			Ack:     ack.Text,
		}))
	}
}

// announceTruncation says once, in both directions, that a turn outgrew the
// buffer and the rest is being dropped. Dropping is the only option — the cap
// exists so one turn cannot OOM the process — but dropping silently stopped the
// reply mid-sentence with nothing saying why. Once, because frames keep arriving.
func (t *Translator) announceTruncation(
	ctx context.Context, chatID vibekit.ChatID, buf *buffer.Buffer, subtask string, buffered int,
) {
	if !buf.MarkOverCap() {
		return
	}
	const notice = "\n\n[Reply truncated: this turn exceeded vibekit's 32 MiB buffer.]"
	blockIndex, seq := buf.AppendTextDelta(notice, subtask)
	slog.Warn("turn exceeded the assistant buffer cap; dropping the remainder",
		"chat_id", chatID, "message_id", buf.MessageID, "buffered_bytes", buffered)
	t.emit(ctx, buf, vibekit.NewEvent(vibekit.EventMessageChunk, chatID,
		vibekit.MessageChunkPayload{
			MessageID:  buf.MessageID,
			Delta:      notice,
			BlockIndex: blockIndex,
			Seq:        seq,
		}))
}

// refusalInfo maps a chunk's _meta.kiro.refusal block to the domain shape. The
// explanation field is dropped as a duplicate of the chunk text; a block with no
// category and no recommended model still marks the turn (every field optional).
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

// HandlePlan persists the agent's plan as one row per turn. ACP resends the
// whole entries array on every update, so this upserts rather than appends; see
// chat.Store.UpsertTurnPlan for what appending per frame cost.
func (t *Translator) HandlePlan(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage) {
	var p ACPPlanWire
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	// A plan is turn content, so it obeys the same mute: a prime that emitted one
	// would otherwise write a row while every other frame was suppressed.
	if buf := t.buffers.TurnFoldTarget(ctx, chatID, vibekit.TurnSourceWireTurnStart); buf != nil && buf.Muted() {
		return
	}
	msg := vibekit.Message{
		ID:   t.newMsgID(),
		Role: vibekit.RoleAssistant,
		Ts:   time.Now().UnixMilli(),
		Plan: p.Entries,
	}
	err := t.chats.UpsertTurnPlan(durable.Context(ctx), chatID, &msg)
	if errors.Is(err, chat.ErrTombstoned) {
		return
	}
	if err != nil {
		slog.Error("persist plan", "chat_id", chatID, "error", err)
	}
}

// HandleModeUpdate persists the agent's new mode and broadcasts mode_changed.
func (t *Translator) HandleModeUpdate(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage) {
	var p ACPModeUpdateWire
	if json.Unmarshal(raw, &p) != nil || p.ModeID == "" {
		return
	}
	changed := false
	err := t.chats.Mutate(ctx, chatID, func(c *vibekit.Chat, ex bool) bool {
		if !ex || c.CurrentModeID == p.ModeID {
			return false
		}
		c.CurrentModeID = p.ModeID
		changed = true
		return true
	})
	if errors.Is(err, chat.ErrTombstoned) {
		return
	}
	if err != nil {
		slog.Error("mode update persist", "chat_id", chatID, "error", err)
	}
	if changed {
		t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventModeChanged, chatID, vibekit.ModeChangedPayload{ModeID: p.ModeID}))
	}
}
