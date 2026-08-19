package hub

// Internal hub methods for prompt handling. Called by command_deps.go.

import (
	"encoding/json"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// LatchTurnModel stamps the dispatching model onto the chat's turn buffer.
//
// GetOrInit rather than Get: at dispatch the turn has produced no frame yet, so
// the buffer usually does not exist. An empty buffer is indistinguishable from
// no buffer everywhere it is read — isEmptyTurn above, Snapshot's own guard, the
// connect-time turn_state replay — because all three key on content, not
// presence.
func (h *Hub) LatchTurnModel(chatID vibekit.ChatID, model string) {
	h.bridge.assistantBufs.GetOrInit(chatID).SetModel(model)
}

// isEmptyTurn returns true if the prompt response reports end_turn AND we
// received no streamed content for this chat (the assistant buffer is empty).
// On v3 the prompt response carries only stopReason/usage — content only ever
// arrives via session/update — so the buffer is the authoritative content
// signal (a v2-era `content` array in the response is gone).
func (h *Hub) isEmptyTurn(resp *vibekit.RPCResponse, chatID vibekit.ChatID) bool {
	if resp == nil || resp.Result == nil {
		return false
	}
	var result struct {
		StopReason vibekit.StopReason `json:"stopReason"`
	}
	if json.Unmarshal(resp.Result, &result) != nil {
		return false
	}
	if result.StopReason != vibekit.StopReasonEndTurn {
		return false
	}
	buf := h.bridge.assistantBufs.Get(chatID)
	if buf == nil {
		return true
	}
	// Reasoning and Blocks are part of "emitted anything", not just Content and
	// ToolCalls. A turn that streamed only thinking (an agent_thought_chunk with
	// no agent_message_chunk and no tool call) is not an empty turn: recreating
	// the session and resending would spend a second model call to reproduce work
	// the user already watched arrive. Blocks is checked as well because it is
	// the canonical chronological array the client renders from, so a future
	// block kind that lands in neither builder still counts as content.
	return buf.Content.Len() == 0 &&
		buf.Reasoning.Len() == 0 &&
		len(buf.ToolCalls) == 0 &&
		len(buf.Blocks) == 0
}
