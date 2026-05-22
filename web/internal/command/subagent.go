package command

// User-initiated subagent commands: spawn, message, terminate, attach, list.

import (
	"context"
	"encoding/json"
	"net/http"

	"vibekit/internal/api"
)

// ACP method constants for subagent operations.
const (
	methodSpawn       = "session/spawn"
	methodMessageSend = "message/send"
	methodTerminate   = "session/terminate"
	methodAttach      = "session/attach"
	methodList        = "session/list"
)

// CmdSpawnSubagent spawns a new subagent session.
func CmdSpawnSubagent(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p api.SpawnSubagentCommand
	if len(cmd.Payload) > 0 {
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			d.RespondErr(w, http.StatusBadRequest, errInvalidPayload)
			return
		}
	}
	if p.Task == "" {
		d.RespondErr(w, http.StatusBadRequest, errTaskRequired)
		return
	}

	sb := deps.GetBridge(cmd.ChatID)
	if sb == nil {
		d.RespondErr(w, http.StatusConflict, errNoBridge)
		return
	}

	resp, err := sb.Call(ctx, methodSpawn, SessionParams(sb, map[string]any{
		"task":  p.Task,
		keyName: p.Name,
	}))
	if err != nil {
		deps.Broadcast(ctx, api.NewEvent(api.EventError, cmd.ChatID, api.ErrorPayload{Code: api.ErrCodeSpawnFailed, Message: err.Error()}))
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}

	var result struct {
		SessionID string `json:"sessionId"`
		Name      string `json:"name"`
	}
	if resp.Result != nil {
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			d.RespondErr(w, http.StatusInternalServerError, err)
			return
		}
	}

	d.Respond(w, cmd.RequestID, map[string]any{
		"ok":         true,
		"session_id": result.SessionID,
		keyName:      result.Name,
	})
}

// CmdMessageSubagent sends a message to a subagent session.
func CmdMessageSubagent(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p api.MessageSubagentCommand
	if len(cmd.Payload) > 0 {
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			d.RespondErr(w, http.StatusBadRequest, errInvalidPayload)
			return
		}
	}
	if p.SubSessionID == "" || p.Text == "" {
		d.RespondErr(w, http.StatusBadRequest, errSubSessionAndText)
		return
	}

	sb := deps.GetBridge(cmd.ChatID)
	if sb == nil {
		d.RespondErr(w, http.StatusConflict, errNoBridge)
		return
	}

	_, err := sb.Call(ctx, methodMessageSend, map[string]any{
		keySessionID: p.SubSessionID,
		"content":    p.Text,
	})
	if err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}

	d.Respond(w, cmd.RequestID, map[string]bool{"ok": true})
}

// CmdSetAutoApproveCrew toggles the auto-approve crew flag.
func CmdSetAutoApproveCrew(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p struct {
		Enabled bool `json:"enabled"`
	}
	if len(cmd.Payload) > 0 {
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			d.RespondErr(w, http.StatusBadRequest, errInvalidPayload)
			return
		}
	}
	var changed bool
	if err := deps.ChatStore().Mutate(ctx, cmd.ChatID, func(c *api.Chat, ex bool) bool {
		if !ex || c.AutoApproveCrew == p.Enabled {
			return false
		}
		c.AutoApproveCrew = p.Enabled
		changed = true
		return true
	}); err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	if !changed {
		if _, ok := deps.ChatStore().Get(ctx, cmd.ChatID); !ok {
			d.RespondErr(w, http.StatusNotFound, errChatNotFound)
			return
		}
	}
	d.Respond(w, cmd.RequestID, map[string]bool{"ok": true})
}

// CmdTerminateSubagent terminates a subagent session.
func CmdTerminateSubagent(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	callSubagentMethod(d, ctx, w, cmd, methodTerminate)
}

// CmdAttachSubagent attaches to a subagent session.
func CmdAttachSubagent(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	callSubagentMethod(d, ctx, w, cmd, methodAttach)
}

// callSubagentMethod is the shared handler for terminate/attach.
func callSubagentMethod(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand, method string) { //nolint:revive // context-as-argument: dispatcher handler signature
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p struct {
		SubSessionID string `json:"sub_session_id"`
	}
	if len(cmd.Payload) > 0 {
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			d.RespondErr(w, http.StatusBadRequest, errInvalidPayload)
			return
		}
	}
	if p.SubSessionID == "" {
		d.RespondErr(w, http.StatusBadRequest, errSubSessionAndText)
		return
	}

	sb := deps.GetBridge(cmd.ChatID)
	if sb == nil {
		d.RespondErr(w, http.StatusConflict, errNoBridge)
		return
	}

	_, err := sb.Call(ctx, method, map[string]any{
		keySessionID: p.SubSessionID,
	})
	if err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	d.Respond(w, cmd.RequestID, map[string]bool{"ok": true})
}

// CmdListSessions lists active subagent sessions.
func CmdListSessions(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}

	sb := deps.GetBridge(cmd.ChatID)
	if sb == nil {
		d.Respond(w, cmd.RequestID, map[string]any{keySessions: []any{}})
		return
	}

	resp, err := sb.Call(ctx, methodList, map[string]any{
		"cwd": deps.WorkDir(),
	})
	if err != nil {
		d.Respond(w, cmd.RequestID, map[string]any{keySessions: []any{}})
		return
	}

	if resp.Result != nil {
		var result map[string]any
		if err := json.Unmarshal(resp.Result, &result); err == nil {
			d.Respond(w, cmd.RequestID, result)
			return
		}
	}
	d.Respond(w, cmd.RequestID, map[string]any{keySessions: []any{}})
}
