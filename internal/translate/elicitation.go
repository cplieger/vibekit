package translate

import (
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/api"
)

// HandleElicitationCreate processes an elicitation/create request from
// kiro-cli. An MCP server has asked for structured input mid-tool-call;
// kiro-cli forwarded it to us because we advertised the elicitation
// client capability. We surface a form to the user and the eventual
// reply is sent by CmdElicitationResponse via bridge.Respond.
//
// The request is a real JSON-RPC request (envelope id present), routed
// here the same way fs/read_text_file is — so the correlation id is
// msg.ID. We reuse the pending-permissions tracker for SSE replay so a
// dialog survives a reconnect, exactly like a permission prompt.
func (t *Translator) HandleElicitationCreate(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	if msg.ID == nil {
		// elicitation/create must be answerable; without an id we
		// cannot route a response, so drop rather than show a dialog
		// the user's answer can't reach.
		slog.Warn("elicitation/create missing id", "chat_id", chatID)
		return
	}
	type elicitParams struct {
		RequestedSchema *api.ElicitationRequestSchema `json:"requestedSchema"`
		Mode            string                        `json:"mode"`
		Message         string                        `json:"message"`
		URL             string                        `json:"url"`
		SessionID       string                        `json:"sessionId"`
		ToolCallID      string                        `json:"toolCallId"`
	}
	p, ok := unmarshalParams[elicitParams](msg, api.MethodElicitationCreate)
	if !ok {
		return
	}

	subSessionID := t.deriveSubSession(chatID, p.SessionID)
	reqID := *msg.ID

	evt := api.NewEvent(api.EventElicitationNeeded, chatID, api.ElicitationNeededPayload{
		RequestID:       reqID,
		Mode:            p.Mode,
		Message:         p.Message,
		URL:             p.URL,
		ToolCallID:      p.ToolCallID,
		SubSessionID:    subSessionID,
		RequestedSchema: p.RequestedSchema,
	})
	t.deps.Broadcast(ctx, evt)
	t.deps.PendingPermsAdd(reqID, evt)
	t.deps.Broadcast(ctx, api.NewEvent(api.EventWorkingLabel, chatID, api.WorkingLabelPayload{Label: api.WorkingLabelInput}))
	t.deps.NotifyPush(ctx, "Input needed", api.PushKindPermission)
}

// HandleElicitationComplete processes the elicitation/complete
// notification: an upstream cancellation (MCP server gave up, timeout,
// or another client answered). We tell the client to dismiss the dialog
// and drop the pending entry so reconnect replay won't resurrect it.
// kiro-cli is no longer waiting on our response, so we send none.
func (t *Translator) HandleElicitationComplete(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	p, ok := unmarshalParams[struct {
		RequestID int64 `json:"requestId"`
	}](msg, api.MethodElicitationComplete)
	if !ok {
		return
	}
	t.deps.PendingPermsRemove(p.RequestID)
	t.deps.Broadcast(ctx, api.NewEvent(api.EventElicitationComplete, chatID, api.ElicitationCompletePayload{
		RequestID: p.RequestID,
	}))
}
