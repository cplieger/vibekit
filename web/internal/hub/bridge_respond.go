// Bridge response helpers shared by all ACP request handlers.

package hub

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"

	"vibekit/internal/api"
)

func parseRequest(msg *api.RPCResponse, v any) error {
	if msg.Params == nil {
		return io.ErrUnexpectedEOF
	}
	return json.Unmarshal(msg.Params, v)
}

func respondOK(ctx context.Context, h *Hub, chatID api.ChatID, msg *api.RPCResponse, result any) {
	if msg.ID == nil {
		return
	}
	sb := h.bridge.mgr.get(chatID)
	if sb == nil {
		return
	}
	if err := sb.bridge.Respond(ctx, *msg.ID, result, nil); err != nil {
		slog.Warn("respondOK: bridge respond failed", "error", err)
	}
}

func respondErr(ctx context.Context, h *Hub, chatID api.ChatID, msg *api.RPCResponse, errMsg string) {
	if msg.ID == nil {
		return
	}
	sb := h.bridge.mgr.get(chatID)
	if sb == nil {
		return
	}
	if err := sb.bridge.Respond(ctx, *msg.ID, nil, &api.RPCError{Code: -1, Message: errMsg}); err != nil {
		slog.Warn("respondErr: bridge respond failed", "error", err)
	}
}
