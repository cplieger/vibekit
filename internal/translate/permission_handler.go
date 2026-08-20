package translate

import (
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// permOptionWire decodes one inbound permission option. ACP sends the id
// as camelCase `optionId`; the SSE-facing vibekit.PermissionOption tags it
// `option_id`, and Go's case-insensitive match does not bridge the
// underscore — so we decode from this wire struct and map to the SSE type.
// approvalTypeTurn is the `_meta.kiro.type` marking a turn approval.
const approvalTypeTurn = "turn_approval"

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
func (t *Translator) HandlePermissionRequest(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	if msg.ID == nil {
		// Without an id we cannot route the outcome back to the agent, so
		// drop rather than show a dialog whose answer can never arrive.
		slog.Warn("permission request missing id", "chat_id", chatID)
		return
	}
	type permReq struct {
		SessionID string `json:"sessionId"`
		ToolCall  struct {
			ToolCallID string           `json:"toolCallId"`
			Title      string           `json:"title"`
			Kind       vibekit.ToolKind `json:"kind"`
		} `json:"toolCall"`
		Options []permOptionWire `json:"options"`
		// Meta carries a TURN APPROVAL when one is being asked for. A turn
		// approval is not a separate method — KAS raises it as an ordinary
		// session/request_permission and puts the file list here, so this is the
		// only thing distinguishing "may I run this tool" from "may I apply this
		// turn's writes".
		Meta struct {
			Kiro struct {
				Type  string `json:"type"`
				Files []struct {
					Path        string `json:"path"`
					SnapshotURI string `json:"snapshotUri"`
					ToolCallID  string `json:"toolCallId"`
				} `json:"files"`
			} `json:"kiro"`
		} `json:"_meta"`
	}
	req, ok := unmarshalParams[permReq](msg, "session/request_permission")
	if !ok {
		return
	}
	reqID := *msg.ID

	subSessionID := t.deriveSubSession(chatID, req.SessionID)

	options := make([]vibekit.PermissionOption, len(req.Options))
	for i, o := range req.Options {
		options[i] = vibekit.PermissionOption{OptionID: o.OptionID, Name: o.Name, Kind: o.Kind}
	}

	// No secondary shell classifier: kiro-cli's native Cedar policy
	// already auto-resolved everything it could — a request that
	// reaches vibekit is a genuine ask and always surfaces to the user.

	// A turn approval's files, workspace-relative. relPath because every other
	// path vibekit puts on the wire is relative and a client that had to handle
	// both would get it wrong somewhere.
	var files []vibekit.ApprovalFile
	if req.Meta.Kiro.Type == approvalTypeTurn {
		files = make([]vibekit.ApprovalFile, 0, len(req.Meta.Kiro.Files))
		for _, f := range req.Meta.Kiro.Files {
			files = append(files, vibekit.ApprovalFile{
				Path:        t.relPath(f.Path),
				SnapshotURI: f.SnapshotURI,
				ActionID:    f.ToolCallID,
			})
		}
	}

	// A workflow STEP's ask is attributed to its run, whichever bridge it
	// arrived on: the launching chat's for an agent-launched run, the run
	// bridge for a manual one. The run id is what lets a run tab render an ask
	// keyed to a different surface, and the node id is what makes the card say
	// WHO is asking.
	step := t.steps.refFor(req.SessionID)
	evt := vibekit.NewEvent(vibekit.EventPermissionNeeded, chatID, vibekit.PermissionNeededPayload{
		RequestID:    reqID,
		ToolCallID:   req.ToolCall.ToolCallID,
		Title:        req.ToolCall.Title,
		Kind:         req.ToolCall.Kind,
		SubSessionID: subSessionID,
		RunID:        step.WorkflowID,
		NodeID:       step.NodeID,
		Options:      options,
		Files:        files,
	})
	t.bus.Broadcast(ctx, evt)
	t.pendingPerms.PendingPermsAdd(reqID, evt)
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventWorkingLabel, chatID, vibekit.WorkingLabelPayload{Label: vibekit.WorkingLabelApproval}))
	t.push.NotifyPush(ctx, "Permission needed", vibekit.PushKindPermission, chatID)
}
