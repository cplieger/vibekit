package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"vibekit/internal/api"
	"vibekit/internal/buffer"
	"vibekit/internal/push"
)

const stopReasonCancelled = api.StopReasonCancelled

// notifyPush sends a push notification if the push service is
// configured and has active subscribers.
func (h *Hub) notifyPush(ctx context.Context, body string, kind api.PushKind) {
	if h.push == nil || !h.push.HasSubscribers() {
		return
	}
	h.lifecycle.inflight.Go(func() {
		h.push.Send(ctx, push.DefaultTitle, body, kind)
	})
}

// takeBuffer returns and removes the chat's assistant buffer.
func (h *Hub) takeBuffer(chatID api.ChatID) (*buffer.Buffer, bool) {
	return h.bridge.assistantBufs.Take(chatID)
}

// emitTurnEndedWithStats finalizes any in-flight assistant message
// (persist to file, emit message_appended), then broadcasts
// turn_ended with the credit delta and elapsed time.
func (h *Hub) emitTurnEndedWithStats(ctx context.Context, chatID api.ChatID, resp *api.RPCResponse, creditsDelta, elapsedMs float64) {
	stopReason := extractStopReason(resp)

	var changedFiles map[string]*api.FileChange

	if buf, ok := h.takeBuffer(chatID); ok && buf.Started {
		changedFiles = buf.ChangedFiles
		h.closeAndRemovePartial(chatID, buf)
		if stopReason == stopReasonCancelled {
			changed := buf.MarkCancelledToolsFailed()
			for i := range changed {
				h.Broadcast(ctx, api.NewEvent(api.EventToolCallUpdate, chatID, api.ToolCallUpdatePayload{MessageID: buf.MessageID, ToolCall: changed[i]}))
			}
		}

		msg := api.Message{
			ID:        buf.MessageID,
			Role:      api.RoleAssistant,
			Ts:        time.Now().UnixMilli(),
			Content:   buf.Content.String(),
			ToolCalls: buf.ToolCalls,
		}
		if buf.IsReasoning {
			msg.OperationType = api.OperationTypeReasoning
		}
		if err := h.chatStore.AppendMessage(ctx, chatID, &msg); err != nil {
			slog.Error("persist assistant turn", "chat_id", chatID, "error", err)
		}
	}

	if stopReason == stopReasonCancelled {
		evt := api.Message{
			ID:        newMessageID(),
			Role:      api.RoleEvent,
			Ts:        time.Now().UnixMilli(),
			EventKind: api.EventCancelled,
		}
		if err := h.chatStore.AppendMessage(ctx, chatID, &evt); err != nil {
			slog.Error("persist cancel event", "chat_id", chatID, "error", err)
		}
	}

	if _, stillExists := h.chatStore.Get(ctx, chatID); stillExists {
		h.Broadcast(ctx, api.NewEvent(api.EventTurnEnded, chatID, api.TurnEndedPayload{
			StopReason:   stopReason,
			CreditsDelta: creditsDelta,
			ElapsedMs:    elapsedMs,
			ChangedFiles: changedFiles,
		}))
	}

	trustReason := api.ClearReasonTurnEnded
	if stopReason == stopReasonCancelled {
		trustReason = api.ClearReasonCancelled
	}
	h.perm.supervised.ClearTrust(chatID, trustReason)

	if stopReason != stopReasonCancelled {
		h.notifyPush(ctx, "Agent finished", api.PushKindAgentFinished)
	}
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
