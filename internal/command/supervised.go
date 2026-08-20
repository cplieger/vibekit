package command

// Supervised mode on v3: a VALUE, not an enforcement.
//
// vibekit used to hold every agent write in memory, mirror it, broadcast it, and
// wait for a per-file verdict before letting it reach disk. All of that is
// deleted. KAS has a turn-approval gate — `autopilot: false` — and it reviews a
// whole turn at once, so this command's entire job is now to set that option and
// persist the user's choice.
//
// The trade is real and stated in the task: writes land BEFORE review, so a build
// watcher or test runner sees rejected content for the duration of the review,
// where vibekit's staged write never touched disk. Batching forces it — hold
// writes in memory until turn-end review and an agent that writes then reads back
// reads stale content.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// CmdSetSupervisedMode records the chat's supervised choice and applies it to a
// running session.
//
// Applied live as well as persisted because `autopilot` is a session config
// option: a chat toggled mid-session would otherwise keep whatever it started
// with until the next session, which is the kind of silent lag that makes a
// safety toggle untrustworthy. On a chat with no bridge yet the persisted value
// is enough — `spawnBridge` passes it at `session/new`.
func CmdSetSupervisedMode(d *Dispatcher, bridges BridgeAccess, chats ChatAccess, ctx context.Context, w http.ResponseWriter, cmd *vibekit.ClientCommand) { //nolint:revive // dispatcher handler signature
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p vibekit.SetSupervisedModeCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}

	if err := chats.ChatStore().Mutate(ctx, cmd.ChatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists || c.SupervisedMode == p.Enabled {
			return false
		}
		c.SupervisedMode = p.Enabled
		return true
	}); err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}

	// Best-effort on the live session, and logged at ERROR when it fails while
	// ENABLING: the user asked to review writes and would not be asked. The
	// disabling direction failing is a nuisance, not a hazard.
	if bridge := bridges.GetBridge(cmd.ChatID); bridge != nil {
		if _, err := bridge.Call(ctx, vibekit.MethodSetConfigOption, SessionParams(bridge, map[string]any{
			"configId": vibekit.ConfigOptionAutopilot,
			"value":    !p.Enabled,
		})); err != nil {
			if p.Enabled {
				slog.Error("supervised mode not applied to the live session; it will NOT ask before writing",
					"chat", cmd.ChatID, keyError, err)
			} else {
				slog.Warn("supervised mode not lifted on the live session", "chat", cmd.ChatID, keyError, err)
			}
		}
	}

	slog.Info("supervised mode set", "chat", cmd.ChatID, "enabled", p.Enabled)
	d.Respond(w, responseWith(map[string]any{"enabled": p.Enabled}))
}
