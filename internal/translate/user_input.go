package translate

import (
	"context"
	"log/slog"
	"strings"

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
	type userInputParams struct {
		SessionID  string                `json:"sessionId"`
		ToolCallID string                `json:"toolCallId"`
		Question   string                `json:"question"`
		Options    []wireUserInputOption `json:"options"`
	}
	p, ok := unmarshalParams[userInputParams](msg, api.MethodKiroUserInput)
	if !ok {
		return
	}

	options := sanitizeUserInputOptions(p.Options)

	subSessionID := t.deriveSubSession(chatID, p.SessionID)
	reqID := *msg.ID

	step := t.stepRef(p.SessionID)
	evt := api.NewEvent(api.EventUserInputNeeded, chatID, api.UserInputNeededPayload{
		RequestID:    reqID,
		Question:     p.Question,
		Options:      options,
		ToolCallID:   p.ToolCallID,
		SubSessionID: subSessionID,
		RunID:        step.WorkflowID,
		NodeID:       step.NodeID,
	})
	t.deps.Broadcast(ctx, evt)
	t.deps.PendingPermsAdd(reqID, evt)
	t.deps.Broadcast(ctx, api.NewEvent(api.EventWorkingLabel, chatID, api.WorkingLabelPayload{Label: api.WorkingLabelInput}))
	t.deps.NotifyPush(ctx, "The agent has a question", api.PushKindPermission)
}

// wireUserInputOption / wireUserInputSubOption are KAS's `_kiro/userInput` option
// shapes. At package scope rather than inside the handler so the sanitizer below
// can name them.
type wireUserInputSubOption struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

type wireUserInputOption struct {
	Title           string                   `json:"title"`
	Description     string                   `json:"description"`
	SubOptionsLabel string                   `json:"subOptionsLabel"`
	SubOptions      []wireUserInputSubOption `json:"subOptions"`
	Recommended     bool                     `json:"recommended"`
}

// Bounds on what a userInput question may put on screen. The agent composes these
// options, so they are model output arriving over a trusted channel — exactly the
// shape that gets forwarded unchecked. Three things go wrong without bounds and
// none of them fails loudly:
//
//   - a long list pushes the composer off screen, and the dock has no scroll of
//     its own
//   - an empty title renders a card the user cannot read, and the ANSWER is the
//     title text, so choosing it sends "" back to the agent
//   - two identical titles make the answer ambiguous by construction, because the
//     reply carries the title rather than an index
const (
	maxUserInputOptions    = 24
	maxUserInputSubOptions = 24
)

// sanitizeUserInputOptions drops what cannot be answered and bounds what can.
//
// Dropping rather than refusing the whole question: one unusable option among
// five still leaves a question worth asking, and refusing outright would leave
// the agent waiting on a request vibekit had already decided not to show.
func sanitizeUserInputOptions(in []wireUserInputOption) []api.UserInputOption {
	options := make([]api.UserInputOption, 0, min(len(in), maxUserInputOptions))
	seen := make(map[string]struct{}, len(in))
	for i := range in {
		o := &in[i]
		title := strings.TrimSpace(o.Title)
		if title == "" {
			continue
		}
		if _, dup := seen[title]; dup {
			continue
		}
		seen[title] = struct{}{}
		options = append(options, api.UserInputOption{
			Title:           title,
			Description:     o.Description,
			SubOptionsLabel: o.SubOptionsLabel,
			SubOptions:      sanitizeUserInputSubOptions(o.SubOptions),
			Recommended:     o.Recommended,
		})
		if len(options) == maxUserInputOptions {
			break
		}
	}
	return options
}

// sanitizeUserInputSubOptions applies the same three rules one level down. Split
// out to keep the parent inside the complexity budget.
func sanitizeUserInputSubOptions(in []wireUserInputSubOption) []api.UserInputSubOption {
	subs := make([]api.UserInputSubOption, 0, min(len(in), maxUserInputSubOptions))
	seen := make(map[string]struct{}, len(in))
	for _, sub := range in {
		title := strings.TrimSpace(sub.Title)
		if title == "" {
			continue
		}
		if _, dup := seen[title]; dup {
			continue
		}
		seen[title] = struct{}{}
		subs = append(subs, api.UserInputSubOption{Title: title, Description: sub.Description})
		if len(subs) == maxUserInputSubOptions {
			break
		}
	}
	return subs
}
