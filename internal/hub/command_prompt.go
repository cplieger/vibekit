package hub

// Internal hub methods for prompt handling. Called by command_deps.go.

import (
	"encoding/json"

	"github.com/cplieger/vibekit/internal/api"
)

// isEmptyTurn returns true if the prompt response indicates end_turn
// with no content AND we received no streamed notifications for this
// chat (the assistant buffer is empty).
func (h *Hub) isEmptyTurn(resp *api.RPCResponse, chatID api.ChatID) bool {
	if resp == nil || resp.Result == nil {
		return false
	}
	var result struct {
		StopReason api.StopReason `json:"stopReason"`
		Content    []any          `json:"content"`
	}
	if json.Unmarshal(resp.Result, &result) != nil {
		return false
	}
	if result.StopReason != api.StopReasonEndTurn || len(result.Content) != 0 {
		return false
	}
	buf := h.bridge.assistantBufs.Get(chatID)
	if buf == nil {
		return true
	}
	return buf.Content.Len() == 0 && len(buf.ToolCalls) == 0
}
