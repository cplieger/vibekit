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

	"github.com/cplieger/vibekit/internal/vibekit"
)

// CmdSetMode switches the chat's active session mode. If a live bridge
// exists the switch is applied immediately via session/set_mode; the
// chosen mode is then persisted on the chat and broadcast so every
// connected client's pill flips. For a chat whose bridge hasn't started
// yet (empty chat, first prompt not sent) there is nothing to switch
// live — the mode is persisted and applied when the bridge's session/new
// completes (spawnBridge threads chat.CurrentModeID into StartOpts.Mode).
func CmdSetMode(d *Dispatcher, bridges BridgeAccess, chats ChatAccess, ctx context.Context, w http.ResponseWriter, cmd *vibekit.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p vibekit.SetModeCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || p.ModeID == "" {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}

	// Switch live first (fail fast) when a bridge is running. When there
	// is no bridge yet the persisted mode below is applied at session/new.
	if bridge := bridges.GetBridge(cmd.ChatID); bridge != nil {
		if _, err := bridge.Call(ctx, vibekit.MethodSetMode, SessionParams(bridge, map[string]any{"modeId": p.ModeID})); err != nil {
			slog.Warn("set_mode: bridge call failed", "chat", cmd.ChatID, keyError, err)
			d.RespondErr(w, http.StatusBadGateway, err)
			return
		}
	}

	var changed bool
	if err := chats.ChatStore().Mutate(ctx, cmd.ChatID, func(c *vibekit.Chat, ex bool) bool {
		if !ex {
			// New chat whose first prompt hasn't been sent — the record
			// exists only client-side. Auto-create it (mirroring
			// cmdCreateChat's seeding) so the picked mode survives to
			// session/new via StartOpts.Mode. Without this every mode
			// pick on a fresh chat 404'd and the pill silently rolled
			// back to Default. Tombstoned ids are refused by Mutate.
			c.Name = vibekit.DefaultChatName
			c.CurrentModeID = p.ModeID
			changed = true
			return true
		}
		if c.CurrentModeID == p.ModeID {
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
		if _, ok := chats.ChatStore().Get(ctx, cmd.ChatID); !ok {
			// Only reachable for a tombstoned id (Mutate refuses to
			// resurrect a just-deleted chat).
			d.RespondErr(w, http.StatusNotFound, ErrChatNotFound)
			return
		}
	}
	if changed {
		chats.Broadcast(ctx, vibekit.NewEvent(vibekit.EventModeChanged, cmd.ChatID, vibekit.ModeChangedPayload(p)))
	}
	slog.Info("mode set", "chat", cmd.ChatID, "mode", p.ModeID)
	d.Respond(w, cmd.RequestID, responseWith(map[string]any{"mode_id": p.ModeID}))
}
