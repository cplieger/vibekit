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
	"github.com/cplieger/vibekit/internal/vibekit"
)

// emit publishes an event describing frames that folded into buf, unless that turn
// is MUTED (buffer.Buffer.muted owns why).
//
// One funnel rather than a check at each of the eight turn-content broadcast
// sites: the version that enumerated sites is the version that left three leaks.
// An event that does not describe folded content goes straight to the bus.
func (t *Translator) emit(ctx context.Context, buf *buffer.Buffer, evt vibekit.ServerEvent) {
	if buf != nil && buf.Muted() {
		return
	}
	t.bus.Broadcast(ctx, evt)
}

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
	// The ONE discriminator there is, and it must run before the buffer is read:
	// a revision hands the folded buffer to the agent's own turn and gives the
	// pre-open a fresh one, so a target taken first would fold this frame into the
	// wrong turn.
	if chunk.Meta.Kiro.AgentInitiated {
		t.turns.ReviseTurnBinding(ctx, chatID)
	}
	buf := t.buffers.TurnFoldTarget(ctx, chatID)
	t.ensureTurnStarted(ctx, chatID, buf)

	// A workflow STEP's frames arrive on this chat's connection with an EMPTY
	// agentSubtaskId (KAS stamps that only on tool frames), so without this the
	// step's prose extends the parent agent's trailing block — empty matches
	// empty. The step's own `_meta.kiro.workflow` supplies an instance-unique
	// key instead; see ACPWorkflowMeta.SubtaskID.
	subtask := chunk.Meta.Kiro.AgentSubtaskID
	workflowSubtask := chunk.Meta.Kiro.Workflow.SubtaskID()
	if workflowSubtask != "" {
		subtask = workflowSubtask
	}

	// kiro-cli's security filter cancelled a tool call: this chunk IS the whole
	// notice, and no session/prompt response is coming (see interrupt_marker.go).
	// Detected BEFORE the steer filter so the two never eat each other's text,
	// and ACTED ON at the very end of this function, because the notice has to
	// reach the transcript before the turn is torn down.
	//
	// Skipped for a workflow STEP frame. A step's chunks arrive on the launching
	// chat's connection, but vibekit issued no session/prompt for the step, so
	// there is no prompt call to release and the only stop verb is run-scoped.
	// The run's own hour ceiling catches it instead — which mis-attributes the
	// cause on the History row, and is the follow-up rather than a reason to
	// cancel the parent chat's live turn from a step's frame.
	interrupted := !isReasoning && workflowSubtask == "" && isInterruptSentinel(chunk.Content.Text)

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
		t.broadcastSteerAcks(ctx, chatID, buf, acks)
		if text == "" {
			// The whole delta is either withheld as a marker candidate or was a
			// marker in full. Returning here rather than broadcasting an empty
			// delta keeps the sequence counter honest and avoids an empty block
			// for text that may never be shown.
			return
		}
	}

	totalLen := buf.BufferedBytes()
	if totalLen+len(text) > maxBufferBytes {
		t.announceTruncation(ctx, chatID, buf, subtask, totalLen)
		return
	}
	// Mirror the delta into the chronological block array, which also
	// accumulates it into the turn's content (or reasoning) builder. For runs of
	// same-kind chunks the helper extends this subtask's newest block; a switch
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

	// LAST, and the ordering is the contract rather than tidiness. The host's
	// teardown takes the buffer, so anything appended after it is lost — the
	// notice has to be in the buffer and on the wire before the turn can end, or
	// the user is left with a turn that stopped and no sentence saying why.
	//
	// No nil guard on the role, deliberately: requireWired panics at construction
	// for an unwired one, and a silently-nil interrupt here would put the wedge
	// back with nothing to say it had.
	if interrupted {
		slog.Warn("kiro-cli interrupted its own tool use; ending the turn",
			"chat_id", chatID, "reason", interruptReason)
		t.turnInterrupt.InterruptTurn(chatID, interruptReason)
	}
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

// HandlePlan persists the agent's plan as ONE row per turn.
//
// ACP resends the whole entries array on every plan update, so this is an upsert
// rather than an append: the store overwrites this turn's plan row when there is
// one. See chat.Store.UpsertTurnPlan for why the row is derived from the turn
// boundary rather than remembered, and for what appending per frame cost.
//
// The id is minted here even on the update path and then discarded by the store,
// because minting is the translator's (it owns newMsgID) while deciding which
// row wins is the store's — the alternative is a read here to find out whether
// an id is needed, which is a second trip and a race with nothing to gain.
func (t *Translator) HandlePlan(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage) {
	var p ACPPlanWire
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	// A plan is turn CONTENT, so it takes the turn's fold target and obeys the same
	// mute every other content frame obeys. This row is PERSISTED rather than
	// broadcast, which is why the emit funnel could not cover it: a prime that
	// emitted a plan wrote a transcript row while every other frame of that turn was
	// suppressed, so the one turn guaranteed to leave no trace left this one.
	if buf := t.buffers.TurnFoldTarget(ctx, chatID); buf != nil && buf.Muted() {
		return
	}
	msg := vibekit.Message{
		ID:   t.newMsgID(),
		Role: vibekit.RoleAssistant,
		Ts:   time.Now().UnixMilli(),
		Plan: p.Entries,
	}
	err := t.chats.UpsertTurnPlan(ctx, chatID, &msg)
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
