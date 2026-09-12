package command

// Supervised mode on v3: a VALUE, not an enforcement.
//
// vibekit used to hold every agent write in memory, mirror it, broadcast it, and
// wait for a per-file verdict before letting it reach disk. All of that is
// deleted. KAS has a turn-approval gate — `autopilot: "off"` — and it reviews a
// whole turn at once, so this command's entire job is now to set that option and
// persist the user's choice, IN THAT ORDER.
//
// The order is the property, not an implementation detail: the record must never hold
// a value the session refused, because the record is what the header, the checkbox and
// every other client read. Persisting first and calling best-effort is what made a
// refused toggle invisible.
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
func CmdSetSupervisedMode(ctx context.Context, bridges BridgeAccess, chats ChatStore, cmd *vibekit.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p vibekit.SetSupervisedModeCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}

	// ASSERT FIRST, persist only on success. Both siblings (CmdSetMode, CmdSetEffort)
	// answer this shape through applySessionConfig, whose own doc states the rule this
	// path used to break: a refusal is REPORTED rather than persisted as a level the
	// session never took. Reversed, a refused toggle left the record,
	// ChatHeader.supervised_mode and every client's checkbox saying supervised over a
	// session in autopilot — with one ERROR log line as the only signal, and the
	// client's own optimistic rollback unreachable because a 200 is not an error.
	//
	// Making this a caller rather than guarding the old block is what makes the
	// fail-open unrepresentable: there is no longer a code path that persists without
	// having graded the assert. A cold-spawning bridge answers ErrBridgeNotStarted,
	// which applySessionConfig grades as benign and nil, so the choice still persists
	// for the session door — the case the two-arm ERROR block was mis-reporting as a
	// write-review failure on an ordinary first-boot click.
	if err := applySessionConfig(ctx, bridges, cmd.ChatID, "set_supervised_mode",
		vibekit.MethodSetConfigOption, map[string]any{
			"configId": vibekit.ConfigOptionAutopilot,
			"value":    autopilotValue(p.Enabled),
		}); err != nil {
		return nil, err
	}

	if err := chats.Mutate(ctx, cmd.ChatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists || c.SupervisedMode == p.Enabled {
			return false
		}
		c.SupervisedMode = p.Enabled
		return true
	}); err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}

	slog.Info("supervised mode set", "chat", cmd.ChatID, "enabled", p.Enabled)
	return responseWith(map[string]any{"enabled": p.Enabled}), nil
}

// autopilotValue maps supervised mode onto KAS's `autopilot` option: supervised
// ON means autopilot OFF, because the option names the behaviour being turned
// off rather than the switch the user flipped.
//
// The value is a STRING because the option is a select over "on" and "off". A
// JSON boolean satisfies neither arm of the request union without a
// `type:"boolean"` discriminator, so it is refused with -32602 and the session
// stays in autopilot — in BOTH directions, which is why this is a two-arm helper
// and not a special case for enabling. Stated once here so this sender and the
// session door cannot disagree about the spelling.
func autopilotValue(supervised bool) string {
	if supervised {
		return vibekit.ConfigValueAutopilotOff
	}
	return vibekit.ConfigValueAutopilotOn
}
