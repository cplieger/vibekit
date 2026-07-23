package translate

import (
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/api"
)

// HandleUserInput processes a _kiro/userInput request from KAS (2.14+):
// the agent's user_input tool asked a structured question (plan-mode
// clarification, spec gate) and KAS forwarded it because we advertised
// the _meta.kiro.userInput initialize capability. We surface a question
// dialog and the eventual reply is sent by CmdUserInputResponse via
// bridge.Respond ({action:"answered", answer} — anything else advances
// the agent to its next phase upstream).
//
// The request is a real JSON-RPC request (envelope id present), routed
// here like _kiro/mcp/elicitation; the correlation id is msg.ID. The
// pending-permissions tracker replays the dialog on reconnect, exactly
// like a permission prompt. The question also arrives as a pending
// tool_call (kind "other", _meta.kiro.toolId "user_input") that KAS
// completes itself once answered — no tool bookkeeping here.
func (t *Translator) HandleUserInput(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	if msg.ID == nil {
		// The request must be answerable; without an id we cannot route
		// a response, so drop rather than show a dialog whose answer
		// can't reach the agent (KAS would stall the question forever).
		slog.Warn("user input request missing id", "chat_id", chatID)
		return
	}
	type wireSubOption struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	type wireOption struct {
		Title           string          `json:"title"`
		Description     string          `json:"description"`
		SubOptionsLabel string          `json:"subOptionsLabel"`
		SubOptions      []wireSubOption `json:"subOptions"`
		Recommended     bool            `json:"recommended"`
	}
	type userInputParams struct {
		SessionID  string       `json:"sessionId"`
		ToolCallID string       `json:"toolCallId"`
		Question   string       `json:"question"`
		Options    []wireOption `json:"options"`
	}
	p, ok := unmarshalParams[userInputParams](msg, api.MethodKiroUserInput)
	if !ok {
		return
	}

	options := make([]api.UserInputOption, 0, len(p.Options))
	for i := range p.Options {
		o := &p.Options[i]
		subs := make([]api.UserInputSubOption, 0, len(o.SubOptions))
		for _, s := range o.SubOptions {
			subs = append(subs, api.UserInputSubOption{Title: s.Title, Description: s.Description})
		}
		options = append(options, api.UserInputOption{
			Title:           o.Title,
			Description:     o.Description,
			SubOptionsLabel: o.SubOptionsLabel,
			SubOptions:      subs,
			Recommended:     o.Recommended,
		})
	}

	subSessionID := t.deriveSubSession(chatID, p.SessionID)
	reqID := *msg.ID

	evt := api.NewEvent(api.EventUserInputNeeded, chatID, api.UserInputNeededPayload{
		RequestID:    reqID,
		Question:     p.Question,
		Options:      options,
		ToolCallID:   p.ToolCallID,
		SubSessionID: subSessionID,
	})
	t.deps.Broadcast(ctx, evt)
	t.deps.PendingPermsAdd(reqID, evt)
	t.deps.Broadcast(ctx, api.NewEvent(api.EventWorkingLabel, chatID, api.WorkingLabelPayload{Label: api.WorkingLabelInput}))
	t.deps.NotifyPush(ctx, "The agent has a question", api.PushKindPermission)
}
