package hub

import (
	"context"
	"encoding/json"
	"log/slog"

	"vibekit/internal/api"
	"vibekit/internal/buffer"
)

const stopReasonCancelled = api.StopReasonCancelled

// notifyPush delegates to the BridgeCoordinator.
func (h *Hub) notifyPush(ctx context.Context, body string, kind api.PushKind) {
	h.coord.NotifyPush(ctx, body, kind)
}

// takeBuffer returns and removes the chat's assistant buffer.
func (h *Hub) takeBuffer(chatID api.ChatID) (*buffer.Buffer, bool) {
	return h.coord.TakeBuffer(chatID)
}

// emitTurnEndedWithStats delegates to the BridgeCoordinator.
func (h *Hub) emitTurnEndedWithStats(ctx context.Context, chatID api.ChatID, resp *api.RPCResponse, creditsDelta, elapsedMs float64) {
	h.coord.EmitTurnEndedWithStats(ctx, chatID, resp, creditsDelta, elapsedMs, h.closeAndRemovePartial)
}

func extractStopReason(resp *api.RPCResponse) api.StopReason {
	if resp == nil || resp.Result == nil {
		return ""
	}
	var result struct {
		StopReason api.StopReason `json:"stopReason"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		slog.Debug("turn_ended: parse result", "error", err)
		return ""
	}
	return result.StopReason
}
