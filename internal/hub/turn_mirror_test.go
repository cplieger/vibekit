package hub

import (
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// mirrorWith replays a sequence of events into a fresh mirror.
func mirrorWith(events ...api.ServerEvent) *turnMirror {
	tm := newTurnMirror()
	for _, evt := range events {
		tm.Apply(evt)
	}
	return tm
}

func chunkEvt(chatID api.ChatID, msgID, delta string, reasoning bool, blockIdx int, seq int64) api.ServerEvent {
	return api.NewEvent(api.EventMessageChunk, chatID, api.MessageChunkPayload{
		MessageID: msgID, Delta: delta, IsReasoning: reasoning, BlockIndex: blockIdx, Seq: seq,
	})
}

// TestTurnMirror_ChunksAccumulate pins the core replica behavior: the
// snapshot equals what a never-disconnected client would have rendered
// (content, reasoning, chronological blocks) and carries the last
// chunk's seq as the watermark.
func TestTurnMirror_ChunksAccumulate(t *testing.T) {
	tm := mirrorWith(
		api.NewEvent(api.EventMessageCreated, "c1", api.Message{ID: "a1", Role: api.RoleAssistant}),
		chunkEvt("c1", "a1", "think ", true, 0, 1),
		chunkEvt("c1", "a1", "hard", true, 0, 2),
		chunkEvt("c1", "a1", "hello ", false, 1, 3),
		chunkEvt("c1", "a1", "world", false, 1, 4),
	)

	snap, ok := tm.Snapshot("c1")
	if !ok || snap.Message == nil {
		t.Fatalf("Snapshot = (%+v, %v), want message", snap, ok)
	}
	m := snap.Message
	if m.ID != "a1" || m.Content != "hello world" || m.Reasoning != "think hard" {
		t.Errorf("message = %+v, want a1 / 'hello world' / 'think hard'", m)
	}
	if len(m.Blocks) != 2 ||
		m.Blocks[0].Type != api.BlockThinking || m.Blocks[0].Thinking != "think hard" ||
		m.Blocks[1].Type != api.BlockText || m.Blocks[1].Text != "hello world" {
		t.Errorf("blocks = %+v, want [thinking'think hard', text'hello world']", m.Blocks)
	}
	if snap.ChunkSeq != 4 {
		t.Errorf("ChunkSeq = %d, want 4 (last delta's seq)", snap.ChunkSeq)
	}
}

// TestTurnMirror_ToolCallsFoldAndUpdate pins tool_call append + block
// anchoring and the tool_call_update whole-value replacement.
func TestTurnMirror_ToolCallsFoldAndUpdate(t *testing.T) {
	tm := mirrorWith(
		api.NewEvent(api.EventMessageCreated, "c1", api.Message{ID: "a1", Role: api.RoleAssistant}),
		chunkEvt("c1", "a1", "running", false, 0, 1),
		api.NewEvent(api.EventToolCall, "c1", api.ToolCallPayload{
			MessageID: "a1", BlockIndex: 1,
			ToolCall: api.ToolCall{ID: "t1", Title: "ls", Status: api.ToolInProgress},
		}),
		api.NewEvent(api.EventToolCallUpdate, "c1", api.ToolCallUpdatePayload{
			MessageID: "a1",
			ToolCall:  api.ToolCall{ID: "t1", Title: "ls", Status: api.ToolCompleted, Output: "ok"},
		}),
	)

	snap, _ := tm.Snapshot("c1")
	m := snap.Message
	if m == nil || len(m.ToolCalls) != 1 {
		t.Fatalf("snapshot message/toolcalls = %+v", m)
	}
	if m.ToolCalls[0].Status != api.ToolCompleted || m.ToolCalls[0].Output != "ok" {
		t.Errorf("tool call not updated in place: %+v", m.ToolCalls[0])
	}
	if len(m.Blocks) != 2 || m.Blocks[1].Type != api.BlockToolUse || m.Blocks[1].ToolCallID != "t1" {
		t.Errorf("blocks = %+v, want tool_use block at index 1", m.Blocks)
	}
}

// TestTurnMirror_TurnEndedDrops pins the lifecycle: turn_ended and
// chat_deleted drop the replica; an idle chat replays nothing.
func TestTurnMirror_TurnEndedDrops(t *testing.T) {
	for _, evtType := range []api.EventType{api.EventTurnEnded, api.EventChatDeleted} {
		tm := mirrorWith(
			api.NewEvent(api.EventMessageCreated, "c1", api.Message{ID: "a1", Role: api.RoleAssistant}),
			chunkEvt("c1", "a1", "x", false, 0, 1),
			api.NewEvent(evtType, "c1", struct{}{}),
		)
		if _, ok := tm.Snapshot("c1"); ok {
			t.Errorf("%s: snapshot survived, want dropped", evtType)
		}
	}
}

// TestTurnMirror_NewTurnReplacesStale pins the self-healing property: a
// fresh turn's message_created replaces a replica that a missed
// turn_ended left behind.
func TestTurnMirror_NewTurnReplacesStale(t *testing.T) {
	tm := mirrorWith(
		api.NewEvent(api.EventMessageCreated, "c1", api.Message{ID: "a1", Role: api.RoleAssistant}),
		chunkEvt("c1", "a1", "old", false, 0, 7),
		// no turn_ended — e.g. the error path
		api.NewEvent(api.EventMessageCreated, "c1", api.Message{ID: "a2", Role: api.RoleAssistant}),
		chunkEvt("c1", "a2", "new", false, 0, 1),
	)
	snap, _ := tm.Snapshot("c1")
	if snap.Message == nil || snap.Message.ID != "a2" || snap.Message.Content != "new" {
		t.Errorf("snapshot = %+v, want fresh turn a2/'new'", snap.Message)
	}
	if snap.ChunkSeq != 1 {
		t.Errorf("ChunkSeq = %d, want 1 (fresh turn's counter)", snap.ChunkSeq)
	}
}

// TestTurnMirror_StatusOnly pins the busy-but-quiet case: an agent
// status with no content yet yields a snapshot with the label and NO
// message (an empty-id message would be meaningless to the client's
// upsert-by-id store).
func TestTurnMirror_StatusOnly(t *testing.T) {
	tm := mirrorWith(
		api.NewEvent(api.EventChatStatus, "c1", api.ChatStatusPayload{Status: "in_progress", Description: "reading files"}),
	)
	snap, ok := tm.Snapshot("c1")
	if !ok {
		t.Fatal("status-only replica should snapshot")
	}
	if snap.Message != nil {
		t.Errorf("Message = %+v, want nil for a content-less turn", snap.Message)
	}
	if snap.Status != "in_progress" || snap.Description != "reading files" {
		t.Errorf("status = %q/%q, want in_progress/reading files", snap.Status, snap.Description)
	}
}

// TestTurnMirror_MessageUpdatedSwaps pins the full-message swap path
// (tool status rewrites re-embed through message_updated with a
// *Message payload, as the chat store broadcasts it).
func TestTurnMirror_MessageUpdatedSwaps(t *testing.T) {
	updated := api.Message{ID: "a1", Role: api.RoleAssistant, Content: "rewritten"}
	tm := mirrorWith(
		api.NewEvent(api.EventMessageCreated, "c1", api.Message{ID: "a1", Role: api.RoleAssistant}),
		chunkEvt("c1", "a1", "original", false, 0, 1),
		api.NewEvent(api.EventMessageUpdated, "c1", &updated),
		// An update for a DIFFERENT (historical) message must not touch the replica.
		api.NewEvent(api.EventMessageUpdated, "c1", &api.Message{ID: "old-9", Content: "noise"}),
	)
	snap, _ := tm.Snapshot("c1")
	if snap.Message == nil || snap.Message.Content != "rewritten" {
		t.Errorf("snapshot = %+v, want swapped content", snap.Message)
	}
}

// TestReplayTurnState_GatedOnPromptingBridges pins the replay gate: a
// mirror replica only reaches clients while the chat's bridge holds
// the prompt slot, and a busy chat with no replica still emits the
// bare busy signal.
func TestReplayTurnState_GatedOnPromptingBridges(t *testing.T) {
	h, _, _ := newTestHub()

	// c1: prompting with mirrored content. c2: prompting, nothing
	// streamed yet. c3: idle with a STALE replica (missed cleanup).
	for _, id := range []api.ChatID{"c1", "c2", "c3"} {
		sb, _ := h.bridge.mgr.getOrInsert(id)
		sb.mu.Lock()
		sb.state = bridgePrompting
		sb.mu.Unlock()
	}
	if sb := h.bridge.mgr.get("c3"); sb != nil {
		sb.mu.Lock()
		sb.state = bridgeIdle
		sb.mu.Unlock()
	}
	h.sse.turnMirror.Apply(api.NewEvent(api.EventMessageCreated, "c1", api.Message{ID: "a1", Role: api.RoleAssistant}))
	h.sse.turnMirror.Apply(chunkEvt("c1", "a1", "hi", false, 0, 1))
	h.sse.turnMirror.Apply(api.NewEvent(api.EventMessageCreated, "c3", api.Message{ID: "a3", Role: api.RoleAssistant}))

	got := map[api.ChatID]api.TurnStatePayload{}
	err := h.replayTurnState(func(evt api.ServerEvent) error {
		if evt.Type != api.EventTurnState {
			t.Fatalf("unexpected event type %s", evt.Type)
		}
		got[evt.ChatID] = evt.Payload.(api.TurnStatePayload)
		return nil
	}, "")
	if err != nil {
		t.Fatalf("replayTurnState: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("replayed chats = %v, want exactly c1+c2 (idle c3 gated out)", got)
	}
	if p := got["c1"]; p.Message == nil || p.Message.Content != "hi" || p.ChunkSeq != 1 {
		t.Errorf("c1 payload = %+v, want mirrored message", p)
	}
	if p, ok := got["c2"]; !ok || p.Message != nil {
		t.Errorf("c2 payload = %+v (ok=%v), want bare busy signal", p, ok)
	}
	if _, ok := got["c3"]; ok {
		t.Error("idle c3 must never replay (the stale-mirror guard)")
	}

	// The chat filter narrows the replay to one chat.
	got = map[api.ChatID]api.TurnStatePayload{}
	if err := h.replayTurnState(func(evt api.ServerEvent) error {
		got[evt.ChatID] = evt.Payload.(api.TurnStatePayload)
		return nil
	}, "c2"); err != nil {
		t.Fatalf("replayTurnState filtered: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("filtered replay = %v, want only c2", got)
	}
}
