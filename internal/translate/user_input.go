package translate

import (
	"context"
	"log/slog"
	"strings"

	"github.com/cplieger/vibekit/internal/vibekit"
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
func (t *Translator) HandleUserInput(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
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
	reqID := *msg.ID
	p, err := decodeParams[userInputParams](msg)
	if err != nil {
		t.refuseAsk(ctx, chatID, vibekit.MethodKiroUserInput, reqID,
			vibekit.UserInputResult{Action: vibekit.UserInputActionDismissed}, err)
		return
	}

	options := sanitizeUserInputOptions(p.Options)

	subSessionID := t.deriveSubSession(chatID, p.SessionID)

	step := t.steps.refFor(p.SessionID)
	evt := vibekit.NewEvent(vibekit.EventUserInputNeeded, chatID, vibekit.UserInputNeededPayload{
		RequestID: reqID,
		// The question is the other half of the decision surface the options
		// are: it is what the human is answering, the agent composes it, and
		// nothing on the wire bounds it. Same rule as a permission title.
		Question:     displayText(p.Question),
		Options:      options,
		ToolCallID:   p.ToolCallID,
		SubSessionID: subSessionID,
		RunID:        step.WorkflowID,
		NodeID:       step.NodeID,
	})
	t.bus.Broadcast(ctx, evt)
	t.pendingPerms.PendingPermsAdd(reqID, evt)
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventWorkingLabel, chatID, vibekit.WorkingLabelPayload{Label: vibekit.WorkingLabelInput}))
	t.push.NotifyPush(ctx, "The agent has a question", vibekit.PushKindPermission, chatID)
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

// sanitizeUserInputOptions drops what cannot be answered, defuses what is shown,
// and bounds what is left.
//
// Dropping rather than refusing the whole question: one unusable option among
// five still leaves a question worth asking, and refusing outright would leave
// the agent waiting on a request vibekit had already decided not to show.
//
// DISPLAYTEXT RUNS BEFORE TRIMSPACE AND BEFORE THE DEDUP, and that order is
// load-bearing in both directions. The preset turns each unsafe rune into a
// SPACE, so a title of nothing but a Bidi override sanitizes to " " — trimming
// afterwards empties it and the empty-title rule drops it, where trimming first
// would leave a card rendering as blank space that answers "\u202e" when chosen
// (measured on runesafe v1.4.2: sanitize-then-trim gives "", trim-then-sanitize
// gives " "). And two titles differing only in invisible controls dedup to one
// only if the dedup sees the sanitized form; keyed on the raw text they would
// both survive as visually identical cards, which is exactly the ambiguity the
// dedup exists to prevent — the reply carries the title, not an index.
//
// That last point is also why sanitizing the title is not merely cosmetic here:
// the title IS the answer sent back to the agent, so the text the human read and
// the text the agent receives have to be the same string.
func sanitizeUserInputOptions(in []wireUserInputOption) []vibekit.UserInputOption {
	options := make([]vibekit.UserInputOption, 0, min(len(in), maxUserInputOptions))
	seen := make(map[string]struct{}, len(in))
	for i := range in {
		o := &in[i]
		title := strings.TrimSpace(displayText(o.Title))
		if title == "" {
			continue
		}
		if _, dup := seen[title]; dup {
			continue
		}
		seen[title] = struct{}{}
		options = append(options, vibekit.UserInputOption{
			Title:           title,
			Description:     displayText(o.Description),
			SubOptionsLabel: displayText(o.SubOptionsLabel),
			SubOptions:      sanitizeUserInputSubOptions(o.SubOptions),
			Recommended:     o.Recommended,
		})
		if len(options) == maxUserInputOptions {
			break
		}
	}
	return options
}

// sanitizeUserInputSubOptions applies the same rules one level down. Split
// out to keep the parent inside the complexity budget.
func sanitizeUserInputSubOptions(in []wireUserInputSubOption) []vibekit.UserInputSubOption {
	subs := make([]vibekit.UserInputSubOption, 0, min(len(in), maxUserInputSubOptions))
	seen := make(map[string]struct{}, len(in))
	for _, sub := range in {
		title := strings.TrimSpace(displayText(sub.Title))
		if title == "" {
			continue
		}
		if _, dup := seen[title]; dup {
			continue
		}
		seen[title] = struct{}{}
		subs = append(subs, vibekit.UserInputSubOption{Title: title, Description: displayText(sub.Description)})
		if len(subs) == maxUserInputSubOptions {
			break
		}
	}
	return subs
}
