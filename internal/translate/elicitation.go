package translate

import (
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// HandleElicitationCreate processes a _kiro/mcp/elicitation request from
// KAS. An MCP server has asked for structured input mid-tool-call; KAS
// forwarded it to us because we advertised the elicitation client
// capability. We surface a form to the user and the eventual reply is
// sent by CmdElicitationResponse via bridge.Respond.
//
// The request is a real JSON-RPC request (envelope id present), routed
// here the same way fs/read_text_file is — so the correlation id is
// msg.ID. On v3 the elicitation body is NESTED under an "elicitation"
// object; sessionId/toolCallId stay top-level. We reuse the
// pending-permissions tracker for SSE replay so a dialog survives a
// reconnect, exactly like a permission prompt. There is no v3
// elicitation-complete method (upstream cancel is not signalled).
func (t *Translator) HandleElicitationCreate(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	if msg.ID == nil {
		// The request must be answerable; without an id we cannot route
		// a response, so drop rather than show a dialog the user's
		// answer can't reach.
		slog.Warn("elicitation missing id", "chat_id", chatID)
		return
	}
	type elicitBody struct {
		RequestedSchema *vibekit.ElicitationRequestSchema `json:"requestedSchema"`
		Mode            string                            `json:"mode"`
		Message         string                            `json:"message"`
		URL             string                            `json:"url"`
	}
	type elicitParams struct {
		SessionID   string     `json:"sessionId"`
		ToolCallID  string     `json:"toolCallId"`
		Elicitation elicitBody `json:"elicitation"`
	}
	p, ok := unmarshalParams[elicitParams](msg, vibekit.MethodElicitationCreate)
	if !ok {
		return
	}

	subSessionID := t.deriveSubSession(chatID, p.SessionID)
	reqID := *msg.ID

	step := t.stepRef(p.SessionID)
	evt := vibekit.NewEvent(vibekit.EventElicitationNeeded, chatID, vibekit.ElicitationNeededPayload{
		RequestID:       reqID,
		Mode:            p.Elicitation.Mode,
		Message:         p.Elicitation.Message,
		URL:             p.Elicitation.URL,
		ToolCallID:      p.ToolCallID,
		SubSessionID:    subSessionID,
		RunID:           step.WorkflowID,
		NodeID:          step.NodeID,
		RequestedSchema: p.Elicitation.RequestedSchema,
	})
	t.bus.Broadcast(ctx, evt)
	t.pendingPerms.PendingPermsAdd(reqID, evt)
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventWorkingLabel, chatID, vibekit.WorkingLabelPayload{Label: vibekit.WorkingLabelInput}))
	t.push.NotifyPush(ctx, "Input needed", vibekit.PushKindPermission, chatID)
}
