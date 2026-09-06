package command

// Rewind: revert this chat to a past turn. There is no branch, no promote and no
// discard — a fork makes a second chat, and rewind edits the one you are in. One
// KAS call does it: `_kiro/checkpoint/revertMultiple` drops the addressed user
// message and everything after it, rolls the files back, and tombstones the cut.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// CmdRewindChat reverts the chat to a past turn via KAS's own checkpoint
// machinery, then truncates vibekit's record to match. The truncation is not
// redundant: mergeProjection preserves anything newer than a replay's last
// message, because an absent tail is normally a durability gap, and a revert is
// the one case where it is intended. Mid-turn is KAS's refusal, forwarded.
func CmdRewindChat(ctx context.Context, bridges BridgeAccess, chats ChatStore, cmd *vibekit.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p vibekit.RewindChatCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || p.MessageID == "" {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}

	chat, ok := chats.Get(ctx, cmd.ChatID)
	if !ok {
		return nil, StatusError(http.StatusNotFound, ErrChatNotFound)
	}
	idx := userMessageIndex(chat.Messages, p.MessageID)
	if idx < 0 {
		return nil, StatusError(http.StatusBadRequest, errRewindTargetNotFound)
	}

	bridge, err := resumeForRevert(ctx, bridges, cmd.ChatID, chat.ACPSessionID)
	if err != nil {
		return nil, err
	}

	result, status, err := revertToMessage(ctx, bridge, p.MessageID)
	if err != nil {
		slog.Warn("rewind: revert failed", "chat", cmd.ChatID, "status", status, keyError, err)
		return nil, StatusError(status, err)
	}

	// Cut at idx, not idx+1: the addressed message is discarded with its
	// successors (KAS slices from the target inclusive), so the prompt at
	// that turn is gone and has to be retyped.
	if mErr := chats.Mutate(ctx, cmd.ChatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			return false
		}
		if idx >= len(c.Messages) {
			return false
		}
		c.Messages = c.Messages[:idx]
		c.MessageCount = len(c.Messages)
		return true
	}); mErr != nil {
		slog.Error("rewind: truncate record", "chat", cmd.ChatID, keyError, mErr)
		return nil, StatusError(http.StatusInternalServerError, mErr)
	}

	slog.Info("chat rewound",
		"chat", cmd.ChatID, "message", p.MessageID,
		"dropped_messages", len(chat.Messages)-idx,
		"restored_files", len(result.AffectedFiles), "total_files", result.TotalFiles)
	return responseWith(map[string]any{
		"restored_files": result.AffectedFiles,
	}), nil
}

// resumeForRevert hands back a bridge whose session is the one the target message
// lives in and whose replay has already been adopted — the two conditions under
// which truncating is safe. `want` is read BEFORE the resume, because a failed
// session/load falls through to session/new and retires that id.
func resumeForRevert(ctx context.Context, bridges BridgeAccess, chatID vibekit.ChatID, want string) (Bridge, error) {
	if want == "" {
		return nil, StatusError(http.StatusConflict, errRewindNoSession)
	}

	// Empty model on purpose: spawnBridge then keeps the chat's own, so a rewind
	// can never silently change which model the chat runs.
	bridge, err := bridges.OpenBridge(ctx, chatID, "")
	if err != nil || bridge == nil {
		// The spawn's own error is logged, not forwarded: it names vibekit's
		// internals, and the client renders the reason verbatim to the user.
		slog.Warn("rewind: no bridge to revert on", "chat", chatID, keyError, err)
		return nil, StatusError(http.StatusBadGateway, errRewindNoBridge)
	}
	if string(bridge.SessionID()) != want {
		// The resume fell through to a fresh session, which holds none of this
		// transcript: KAS would refuse the id or roll back the wrong thing.
		slog.Warn("rewind: original session not resumed",
			"chat", chatID, "want", want, "got", bridge.SessionID())
		return nil, StatusError(http.StatusConflict, errRewindSessionNotResumed)
	}

	// A resume replays into a projection swapped in on the Forward goroutine, and
	// mergeProjection returns its messages wholesale — so a swap landing after the
	// truncation hands every reverted turn straight back. Refuse rather than cut.
	if err := bridges.AwaitReplayAdopted(ctx, chatID); err != nil {
		slog.Warn("rewind: replay not adopted, refusing to truncate",
			"chat", chatID, keyError, err)
		// One refusal for both causes: the other is the caller's own context, and
		// a caller that walked away reads nothing anyway.
		return nil, StatusError(http.StatusServiceUnavailable, errRewindReplayPending)
	}
	return bridge, nil
}

// revertResult is KAS's reply to a revert. affectedFiles are the paths it put
// back (or removed, for a file the discarded turns created).
type revertResult struct {
	Error         string   `json:"error"`
	AffectedFiles []string `json:"affectedFiles"`
	TotalFiles    int      `json:"totalFiles"`
	Success       bool     `json:"success"`
}

// revertToMessage performs the KAS round trip and normalises its two failure
// channels into one error plus the status to report: a transport failure comes
// back as an error, a refusal KAS can explain as `success:false` with a reason,
// forwarded verbatim since it is more specific than anything vibekit can infer.
func revertToMessage(ctx context.Context, bridge sessionCaller, messageID string) (revertResult, int, error) {
	var result revertResult
	resp, err := bridge.Call(ctx, vibekit.MethodCheckpointRevertMultiple, SessionParams(bridge, map[string]any{
		"messageId": messageID,
	}))
	if err != nil {
		return result, http.StatusBadGateway, err
	}
	if resp != nil && resp.Result != nil {
		_ = json.Unmarshal(resp.Result, &result)
	}
	if !result.Success {
		return result, http.StatusConflict, errors.New(cmp.Or(result.Error, "revert failed"))
	}
	return result, http.StatusOK, nil
}

// userMessageIndex locates the revert target, returning -1 when absent.
// User-only because KAS requires it, and only user messages share an id space
// with KAS — an assistant turn carries KAS's own id.
func userMessageIndex(messages []vibekit.Message, id string) int {
	for i := range messages {
		if messages[i].ID == id && messages[i].Role == vibekit.RoleUser {
			return i
		}
	}
	return -1
}

// CmdSetEffort sets the chat's reasoning-effort level. On v3 effort is a session
// config option, so a running session is switched in place; the level is then
// persisted on the chat and applied to later sessions through StartOpts.Effort.
// Per-chat, so two chats can disagree and a model switch discards nothing. A
// bridgeless chat is not a 409, and auto-create mirrors CmdSetMode.
func CmdSetEffort(ctx context.Context, bridges BridgeAccess, chats ChatStore, cmd *vibekit.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p vibekit.SetEffortCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || !p.Level.Valid() {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}

	// Switch live first (fail fast) when a bridge is running, so a refusal is
	// reported rather than persisted as a level the session never took.
	if bridge := bridges.Bridge(cmd.ChatID); bridge != nil {
		if _, err := bridge.Call(ctx, vibekit.MethodSetConfigOption, SessionParams(bridge, map[string]any{
			"configId": vibekit.ConfigOptionEffort,
			"value":    string(p.Level),
		})); err != nil {
			slog.Warn("set_effort: bridge call failed", "chat", cmd.ChatID, keyError, err)
			return nil, StatusError(http.StatusBadGateway, err)
		}
	}

	if err := chats.Mutate(ctx, cmd.ChatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			c.Name = vibekit.DefaultChatName
			c.Effort = string(p.Level)
			return true
		}
		if c.Effort == string(p.Level) {
			return false
		}
		c.Effort = string(p.Level)
		return true
	}); err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}

	slog.Info("effort set", "chat", cmd.ChatID, "level", p.Level)
	return responseWith(map[string]any{"level": p.Level}), nil
}
