package agent

// Internal agent methods for prompt handling. Called by command_deps.go.

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
func (rt *Runtime) LatchTurnModel(chatID vibekit.ChatID, model string) {
	rt.bridge.assistantBufs.GetOrInit(chatID).SetModel(model)
}

// isEmptyTurn returns true if the prompt response reports end_turn AND we
// received no streamed content for this chat (the assistant buffer is empty).
// On v3 the prompt response carries only stopReason/usage — content only ever
// arrives via session/update — so the buffer is the authoritative content
// signal (a v2-era `content` array in the response is gone).
func (rt *Runtime) isEmptyTurn(resp *vibekit.RPCResponse, chatID vibekit.ChatID) bool {
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
	buf := rt.bridge.assistantBufs.Get(chatID)
	if buf == nil {
		return true
	}
	// Through the buffer's own accessor, which takes the mutex the four
	// accumulators are guarded by. This function runs on the prompt's goroutine
	// while the dispatch loop is still appending to that buffer, so reading the
	// exported fields directly was a data race — and the rule for WHICH fields
	// count belongs with the fields, not here.
	return buf.EmittedNothing()
}
