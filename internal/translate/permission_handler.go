package translate

import (
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/api"
)

// permOptionWire decodes one inbound permission option. ACP sends the id
// as camelCase `optionId`; the SSE-facing api.PermissionOption tags it
// `option_id`, and Go's case-insensitive match does not bridge the
// underscore — so we decode from this wire struct and map to the SSE type.
type permOptionWire struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// HandlePermissionRequest processes session/request_permission from kiro-cli.
//
// The v3 params object is FLAT — {sessionId, toolCall{...}, options[]} —
// and the JSON-RPC correlation id is on the envelope (msg.ID), NOT inside
// params. unmarshalParams decodes msg.Params directly, so the decode struct
// must match those fields at top level; a `params`-wrapped struct (or an
// `id` field read from params) decodes to all-zero, yielding an empty dialog
// and request_id=0 (the outcome would then be answered on id 0, wedging the
// tool call and disabling the shell auto-policy). Mirror HandleElicitationCreate,
// which decodes flat and reads *msg.ID.
func (t *Translator) HandlePermissionRequest(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	if msg.ID == nil {
		// Without an id we cannot route the outcome back to the agent, so
		// drop rather than show a dialog whose answer can never arrive.
		slog.Warn("permission request missing id", "chat_id", chatID)
		return
	}
	type permReq struct {
		SessionID string `json:"sessionId"`
		ToolCall  struct {
			ToolCallID string       `json:"toolCallId"`
			Title      string       `json:"title"`
			Kind       api.ToolKind `json:"kind"`
		} `json:"toolCall"`
		Options []permOptionWire `json:"options"`
	}
	req, ok := unmarshalParams[permReq](msg, "session/request_permission")
	if !ok {
		return
	}
	reqID := *msg.ID

	subSessionID := t.deriveSubSession(chatID, req.SessionID)

	options := make([]api.PermissionOption, len(req.Options))
	for i, o := range req.Options {
		options[i] = api.PermissionOption{OptionID: o.OptionID, Name: o.Name, Kind: o.Kind}
	}

	// No secondary shell classifier: kiro-cli's native Cedar policy
	// already auto-resolved everything it could — a request that
	// reaches vibekit is a genuine ask and always surfaces to the user.

	evt := api.NewEvent(api.EventPermissionNeeded, chatID, api.PermissionNeededPayload{
		RequestID:    reqID,
		ToolCallID:   req.ToolCall.ToolCallID,
		Title:        req.ToolCall.Title,
		Kind:         req.ToolCall.Kind,
		SubSessionID: subSessionID,
		Options:      options,
	})
	t.deps.Broadcast(ctx, evt)
	t.deps.PendingPermsAdd(reqID, evt)
	t.deps.Broadcast(ctx, api.NewEvent(api.EventWorkingLabel, chatID, api.WorkingLabelPayload{Label: api.WorkingLabelApproval}))
	t.deps.NotifyPush(ctx, "Permission needed", api.PushKindPermission)
}
