package command

// User-initiated session mode switch (v3). On v3 (KAS) every role — the
// bundled workflow modes (Default/Spec/Plan/…) AND workspace custom
// agents (.kiro/agents/*) — is an entry in the session's availableModes,
// switched in place via session/set_mode with no teardown, no priming.
// This replaces v2's session-locked agent selection for the picker.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/chat"
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
	if err := applySessionConfig(ctx, bridges, cmd.ChatID, "set_mode",
		vibekit.MethodSetMode, map[string]any{"modeId": p.ModeID}); err != nil {
		return nil, err
	}

	// Whether anything changed, and nothing more: a refused write is reported by
	// Mutate's error, not by this flag.
	var changed bool
	if err := chats.Mutate(ctx, cmd.ChatID, func(c *vibekit.Chat, ex bool) bool {
		if !ex {
			// New chat whose first prompt hasn't been sent — auto-create
			// so the picked mode survives to session/new via
			// StartOpts.Mode. Without this every pick on a fresh chat
			// 404'd. Tombstoned ids are refused by Mutate.
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
		// A tombstoned id names a chat deleted in the last ten minutes,
		// which the store refuses to resurrect — the whole 404 condition.
		// A no-op mutation (a repeat pick already in force) is a success.
		if errors.Is(err, chat.ErrTombstoned) {
			return nil, StatusError(http.StatusNotFound, ErrChatNotFound)
		}
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	if changed {
		bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventModeChanged, cmd.ChatID, vibekit.ModeChangedPayload(p)))
	}
	slog.Info("mode set", "chat", cmd.ChatID, "mode", p.ModeID)
	return responseWith(map[string]any{"mode_id": p.ModeID}), nil
}

// applySessionConfig sends one live session-config change for chat and grades the
// outcome, so all THREE config commands — set_mode, set_effort and
// set_supervised_mode — answer a cold spawn the same way. A nil error means the
// caller may persist: either the change landed on a session, or there is no session
// yet to land it on.
//
// Named rather than left at "both", or the supervised caller reads as a stray. It was
// the last path with its own bespoke block, and that block FAILED OPEN: it discarded
// the outcome and answered 200 whatever the session said.
//
// A bridge that EXISTS but has not STARTED is that second case, not a failure. The
// manager registers the record before Start so concurrent opens coalesce, so a mode,
// effort or supervised click during a cold spawn — which unpacks a ~240 MB KAS
// runtime — holds a
// bridge whose write refuses with vibekit.ErrBridgeNotStarted, and so does one whose
// Start FAILED (spawnBridge's setup error removes the record and then releases the
// starting state, so a holder that raced the removal keeps an idle bridge with no
// session behind it). Both are chats with no session, which is exactly the state the
// bridgeless path already handles by persisting for the session door.
//
// The lie the fail-fast rule protects against cannot happen there, and that is why
// this is safe rather than merely convenient: persistNewSessionMetadata re-reads the
// record AFTER Start, resets it to the mode the session actually took, and
// reportModeNotApplied names the divergence to the user with a retry instruction;
// BridgeCoordinator.repairEffort re-asserts the level on the next prompt. What the
// rule still catches is a refusal by the SESSION — KAS declining a level this model
// does not offer — which stays a 502 and is never persisted.
//
// WAITING for the spawn was priced and refused. The window is bounded by a 120s
// handshake budget, so the prompt path's own 20s AdmissionWait would still refuse on a
// first boot while holding an HTTP request for the whole wait, and a second bound plus
// a wake channel would be two more things to keep in step with that budget.
func applySessionConfig(
	ctx context.Context,
	bridges BridgeAccess,
	chatID vibekit.ChatID,
	verb, method string,
	params map[string]any,
) error {
	bridge := bridges.Bridge(chatID)
	if bridge == nil {
		return nil
	}
	_, err := bridge.Call(ctx, method, SessionParams(bridge, params))
	switch {
	case err == nil:
		return nil
	case errors.Is(err, vibekit.ErrBridgeNotStarted):
		slog.Debug(verb+": no session yet; persisting for the session door",
			"chat", chatID, keyError, err)
		return nil
	default:
		slog.Warn(verb+": bridge call failed", "chat", chatID, keyError, err)
		return StatusError(http.StatusBadGateway, err)
	}
}
