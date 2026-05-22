package hub

import (
	"context"
	"encoding/json"
	"testing"

	"vibekit/internal/api"
)

func TestHandleSessionActivity_DropsParentSession(t *testing.T) {
	h, cs, br := newTestHub()
	defer h.Shutdown()

	chatID := api.ChatID("test-chat-drop")
	_ = cs.Mutate(context.Background(), chatID, func(c *api.Chat, _ bool) bool {
		c.Name = "test"
		return true
	})

	// Register the bridge so parentACPSession returns its session ID.
	sb, _ := h.bridge.mgr.getOrInsert(chatID)
	sb.bridge = br
	sb.state = bridgeIdle
	sb.mu.Unlock()

	parentSess := string(br.SessionID())

	// Record replay buffer length before the call.
	before := h.sse.replayBuf.Len()

	params, _ := json.Marshal(map[string]any{
		"sessionId": parentSess,
		"event":     map[string]any{"type": "thinking"},
	})
	msg := &api.RPCResponse{Params: params}
	h.handleSessionActivity(context.Background(), chatID, msg)

	// No event should have been emitted for parent session.
	after := h.sse.replayBuf.Len()
	if after != before {
		t.Errorf("expected no broadcast for parent session, but replay buffer grew from %d to %d", before, after)
	}
}

func TestHandleSessionActivity_BroadcastsSubagentEvent(t *testing.T) {
	h, cs, br := newTestHub()
	defer h.Shutdown()

	chatID := api.ChatID("test-chat-sub")
	_ = cs.Mutate(context.Background(), chatID, func(c *api.Chat, _ bool) bool {
		c.Name = "test"
		return true
	})

	sb, _ := h.bridge.mgr.getOrInsert(chatID)
	sb.bridge = br
	sb.state = bridgeIdle
	sb.mu.Unlock()

	subSess := "sub-session-123"

	before := h.sse.replayBuf.Len()

	params, _ := json.Marshal(map[string]any{
		"sessionId": subSess,
		"event":     map[string]any{"type": "tool_use"},
	})
	msg := &api.RPCResponse{Params: params}
	h.handleSessionActivity(context.Background(), chatID, msg)

	after := h.sse.replayBuf.Len()
	if after <= before {
		t.Error("expected broadcast for subagent session, but replay buffer did not grow")
	}
}
