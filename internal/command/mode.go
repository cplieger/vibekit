package command

// User-initiated session mode switch (v3). On v3 (KAS) every role — the
// bundled workflow modes (Default/Spec/Plan/…) AND workspace custom
// agents (.kiro/agents/*) — is an entry in the session's availableModes,
// switched in place via session/set_mode with no teardown, no priming.
// This replaces v2's session-locked agent selection for the picker.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/api"
)

// CmdSetMode switches the chat's active session mode. If a live bridge
// exists the switch is applied immediately via session/set_mode; the
// chosen mode is then persisted on the chat and broadcast so every
// connected client's pill flips. For a chat whose bridge hasn't started
// yet (empty chat, first prompt not sent) there is nothing to switch
// live — the mode is persisted and applied when the bridge's session/new
// completes (spawnBridge threads chat.CurrentModeID into StartOpts.Mode).
func CmdSetMode(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p api.SetModeCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || p.ModeID == "" {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}

	// Switch live first (fail fast) when a bridge is running. When there
	// is no bridge yet the persisted mode below is applied at session/new.
	if bridge := deps.GetBridge(cmd.ChatID); bridge != nil {
		if _, err := bridge.Call(ctx, api.MethodSetMode, SessionParams(bridge, map[string]any{"modeId": p.ModeID})); err != nil {
			slog.Warn("set_mode: bridge call failed", "chat", cmd.ChatID, keyError, err)
			d.RespondErr(w, http.StatusBadGateway, err)
			return
		}
	}

	var changed bool
	if err := deps.ChatStore().Mutate(ctx, cmd.ChatID, func(c *api.Chat, ex bool) bool {
		if !ex || c.CurrentModeID == p.ModeID {
			return false
		}
		c.CurrentModeID = p.ModeID
		changed = true
		return true
	}); err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	if !changed {
		if _, ok := deps.ChatStore().Get(ctx, cmd.ChatID); !ok {
			d.RespondErr(w, http.StatusNotFound, ErrChatNotFound)
			return
		}
	}
	if changed {
		deps.Broadcast(ctx, api.NewEvent(api.EventModeChanged, cmd.ChatID, api.ModeChangedPayload(p)))
	}
	slog.Info("mode set", "chat", cmd.ChatID, "mode", p.ModeID)
	d.Respond(w, cmd.RequestID, responseWith(map[string]any{"mode_id": p.ModeID}))
}
