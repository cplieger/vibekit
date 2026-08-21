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
func CmdSetMode(ctx context.Context, bridges BridgeAccess, chats ChatStore, bus Broadcaster, cmd *vibekit.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p vibekit.SetModeCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || p.ModeID == "" {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}

	// Switch live first (fail fast) when a bridge is running. When there
	// is no bridge yet the persisted mode below is applied at session/new.
	if bridge := bridges.Bridge(cmd.ChatID); bridge != nil {
		if _, err := bridge.Call(ctx, vibekit.MethodSetMode, SessionParams(bridge, map[string]any{"modeId": p.ModeID})); err != nil {
			slog.Warn("set_mode: bridge call failed", "chat", cmd.ChatID, keyError, err)
			return nil, StatusError(http.StatusBadGateway, err)
		}
	}

	var changed bool
	if err := chats.Mutate(ctx, cmd.ChatID, func(c *vibekit.Chat, ex bool) bool {
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
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	if !changed {
		if _, ok := chats.Get(ctx, cmd.ChatID); !ok {
			// Only reachable for a tombstoned id (Mutate refuses to
			// resurrect a just-deleted chat).
			return nil, StatusError(http.StatusNotFound, ErrChatNotFound)
		}
	}
	if changed {
		bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventModeChanged, cmd.ChatID, vibekit.ModeChangedPayload(p)))
	}
	slog.Info("mode set", "chat", cmd.ChatID, "mode", p.ModeID)
	return responseWith(map[string]any{"mode_id": p.ModeID}), nil
}
