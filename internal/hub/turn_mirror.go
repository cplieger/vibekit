// Connect-time turn-state synthesis (P6): the turn mirror.
//
// A client that reconnects (or first loads) mid-turn used to see a
// blank streaming transcript until turn_ended, and the gap handler
// could only GUESS at busy state (the documented eager thinking-clear
// tradeoff). The mirror closes both: it maintains, per chat, a replica
// of the in-flight assistant turn, and streamInitialState synthesizes
// one turn_state event per busy chat from it at connect.
//
// ARCHITECTURE: the mirror is fed exclusively by the emit funnel — the
// exact event stream clients consume — never by reading buffer.Buffer.
// The buffer's fields are guarded by a single-writer dispatch
// invariant (its mutex covers the block array only); snapshotting it
// from the SSE-connect goroutine would be a data race or would force
// the whole translate layer under a coarser lock. Mirroring the
// events instead gives a state that is BY CONSTRUCTION what a
// never-disconnected client would have rendered, with its own tiny
// lock and zero changes to the streaming hot path's concurrency
// contract.
//
// Ordering: emit applies an event to the mirror BEFORE publishing it,
// so a snapshot taken at connect time is always >= the ring content
// the client just replayed. Chunks that race the snapshot (published
// after the ring replay but folded into the snapshot) are deduplicated
// client-side via the chunk_seq watermark (MessageChunkPayload.Seq).

package hub

import (
	"slices"
	"sync"

	"github.com/cplieger/vibekit/internal/api"
)

// mirroredTurn is one chat's in-flight turn replica.
type mirroredTurn struct {
	status   api.ChatStatusPayload
	msg      api.Message
	chunkSeq int64
}

// turnMirror holds the per-chat replicas. All methods are safe for
// concurrent use.
type turnMirror struct {
	turns map[api.ChatID]*mirroredTurn
	mu    sync.Mutex
}

func newTurnMirror() *turnMirror {
	return &turnMirror{turns: make(map[api.ChatID]*mirroredTurn)}
}

// Apply folds one broadcast event into the mirror. Called by emit for
// every event, before publication.
func (tm *turnMirror) Apply(evt api.ServerEvent) {
	if evt.ChatID == "" {
		return
	}
	switch evt.Type {
	case api.EventMessageCreated:
		tm.applyCreated(evt)
	case api.EventMessageChunk:
		tm.applyChunk(evt)
	case api.EventToolCall:
		tm.applyToolCall(evt)
	case api.EventToolCallUpdate:
		tm.applyToolCallUpdate(evt)
	case api.EventCodeReferences:
		tm.applyCodeReferences(evt)
	case api.EventMessageUpdated:
		tm.applyMessageUpdated(evt)
	case api.EventChatStatus:
		tm.applyChatStatus(evt)
	case api.EventTurnEnded, api.EventChatDeleted:
		tm.mu.Lock()
		delete(tm.turns, evt.ChatID)
		tm.mu.Unlock()
	default:
	}
}

// Snapshot returns a copy of the chat's in-flight turn state suitable
// for marshaling after the lock is released: the slices are cloned
// (Block/ToolCall elements are value types whose strings are
// immutable), so later Apply calls cannot race the caller.
func (tm *turnMirror) Snapshot(chatID api.ChatID) (api.TurnStatePayload, bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t, ok := tm.turns[chatID]
	if !ok {
		return api.TurnStatePayload{}, false
	}
	out := api.TurnStatePayload{
		ChunkSeq:    t.chunkSeq,
		Status:      t.status.Status,
		Description: t.status.Description,
	}
	// A replica created by a status-only event carries no message yet;
	// emit the busy signal without one (an empty-id message would be
	// meaningless to the client's upsert-by-id store).
	if t.msg.ID != "" {
		msg := t.msg
		msg.Blocks = slices.Clone(t.msg.Blocks)
		msg.ToolCalls = slices.Clone(t.msg.ToolCalls)
		msg.CodeReferences = slices.Clone(t.msg.CodeReferences)
		if t.msg.Refusal != nil {
			r := *t.msg.Refusal
			msg.Refusal = &r
		}
		out.Message = &msg
	}
	return out, true
}

// getOrInit returns the chat's replica, creating an empty one when the
// first observed event isn't message_created (out-of-order tolerance,
// mirroring the client store's chunk-before-created handling).
// Callers hold tm.mu.
func (tm *turnMirror) getOrInit(chatID api.ChatID, msgID string) *mirroredTurn {
	t, ok := tm.turns[chatID]
	if !ok || (msgID != "" && t.msg.ID != "" && t.msg.ID != msgID) {
		// New turn (or first sighting): a differing message id means a
		// fresh turn started — replace the stale replica.
		t = &mirroredTurn{msg: api.Message{ID: msgID, Role: api.RoleAssistant}}
		tm.turns[chatID] = t
	}
	if t.msg.ID == "" {
		t.msg.ID = msgID
	}
	return t
}

func (tm *turnMirror) applyCreated(evt api.ServerEvent) {
	msg, ok := evt.Payload.(api.Message)
	if !ok || msg.Role != api.RoleAssistant {
		return
	}
	tm.mu.Lock()
	tm.turns[evt.ChatID] = &mirroredTurn{msg: msg}
	tm.mu.Unlock()
}

func (tm *turnMirror) applyChunk(evt api.ServerEvent) {
	p, ok := evt.Payload.(api.MessageChunkPayload)
	if !ok {
		return
	}
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t := tm.getOrInit(evt.ChatID, p.MessageID)
	if p.IsReasoning {
		t.msg.Reasoning += p.Delta
	} else {
		t.msg.Content += p.Delta
	}
	if p.Refusal != nil {
		t.msg.Refusal = p.Refusal
	}
	applyBlockDelta(&t.msg, &p)
	if p.Seq > t.chunkSeq {
		t.chunkSeq = p.Seq
	}
}

// applyBlockDelta mirrors the client store's block accumulation: the
// delta lands on the server-addressed block index, padding gaps
// defensively (same tolerance as the client).
func applyBlockDelta(msg *api.Message, p *api.MessageChunkPayload) {
	kind := api.BlockText
	if p.IsReasoning {
		kind = api.BlockThinking
	}
	for len(msg.Blocks) <= p.BlockIndex {
		msg.Blocks = append(msg.Blocks, api.Block{Type: kind, AgentSubtaskID: p.AgentSubtaskID})
	}
	b := &msg.Blocks[p.BlockIndex]
	if p.IsReasoning {
		b.Thinking += p.Delta
	} else {
		b.Text += p.Delta
	}
}

func (tm *turnMirror) applyToolCall(evt api.ServerEvent) {
	p, ok := evt.Payload.(api.ToolCallPayload)
	if !ok {
		return
	}
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t := tm.getOrInit(evt.ChatID, p.MessageID)
	t.msg.ToolCalls = append(t.msg.ToolCalls, p.ToolCall)
	for len(t.msg.Blocks) <= p.BlockIndex {
		t.msg.Blocks = append(t.msg.Blocks, api.Block{})
	}
	t.msg.Blocks[p.BlockIndex] = api.Block{
		Type:           api.BlockToolUse,
		ToolCallID:     p.ToolCall.ID,
		AgentSubtaskID: p.ToolCall.AgentSubtaskID,
	}
}

func (tm *turnMirror) applyToolCallUpdate(evt api.ServerEvent) {
	p, ok := evt.Payload.(api.ToolCallUpdatePayload)
	if !ok {
		return
	}
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t, exists := tm.turns[evt.ChatID]
	if !exists || t.msg.ID != p.MessageID {
		return
	}
	for i := range t.msg.ToolCalls {
		if t.msg.ToolCalls[i].ID == p.ToolCall.ID {
			t.msg.ToolCalls[i] = p.ToolCall
			return
		}
	}
}

func (tm *turnMirror) applyCodeReferences(evt api.ServerEvent) {
	p, ok := evt.Payload.(api.CodeReferencesPayload)
	if !ok {
		return
	}
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t, exists := tm.turns[evt.ChatID]
	if !exists || t.msg.ID != p.MessageID {
		return
	}
	// The live event carries the full deduped list each time; replace.
	t.msg.CodeReferences = p.References
}

// applyMessageUpdated folds a full-message swap (tool status rewrites
// re-embedding into the parent assistant message) into the replica —
// but only for the in-flight message id; updates to historical
// messages are irrelevant to the turn snapshot.
func (tm *turnMirror) applyMessageUpdated(evt api.ServerEvent) {
	var msg api.Message
	switch m := evt.Payload.(type) {
	case api.Message:
		msg = m
	case *api.Message:
		if m == nil {
			return
		}
		msg = *m
	default:
		return
	}
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t, exists := tm.turns[evt.ChatID]
	if !exists || t.msg.ID != msg.ID {
		return
	}
	t.msg = msg
}

func (tm *turnMirror) applyChatStatus(evt api.ServerEvent) {
	p, ok := evt.Payload.(api.ChatStatusPayload)
	if !ok {
		return
	}
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t, exists := tm.turns[evt.ChatID]
	if !exists {
		// Status can precede the first content chunk (agent declares
		// intent before producing output). Track it on an empty
		// replica so the connect snapshot for a busy-but-quiet chat
		// still carries the label.
		t = &mirroredTurn{}
		tm.turns[evt.ChatID] = t
	}
	t.status = p
}
