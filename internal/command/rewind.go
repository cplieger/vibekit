package command

// Rewind: revert this chat to a past turn. There is no branch, no promote
// and no discard — a fork makes a second chat, and rewind edits the one
// you are in.
//
// The operation is one KAS call. `_kiro/checkpoint/revertMultiple` drops
// the addressed user message and everything after it, rolls the files
// back from KAS's own snapshots, and appends a tombstone so the truncation
// is durable. Vibekit's Rewind and its former turn-level Restore are the
// same operation.

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
// machinery, then truncates vibekit's record to match.
//
// The truncation is not redundant with the revert: vibekit's chat file
// still holds the dropped turns, and mergeProjection deliberately
// preserves anything newer than a replay's last message (KAS's log is not
// fsynced, so an absent tail is normally a durability gap rather than an
// intended deletion). A revert is the one case where the tail is gone on
// purpose, and only vibekit knows that at this moment.
//
// No session/load follows: the live session's context is already reverted
// in place, and the tombstone covers every future load.
//
// Mid-turn is refused, not queued — KAS throws on a live turn and refuses
// a concurrent revert per session, so vibekit forwards the reason instead
// of reimplementing the guard.
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

	bridge := bridges.Bridge(cmd.ChatID)
	if bridge == nil {
		// No live session to revert. The files and the transcript move together
		// or not at all, so truncating the record alone is not an option.
		return nil, StatusError(http.StatusConflict, errNoBridge)
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

// revertResult is KAS's reply to a revert. affectedFiles are the paths it put
// back (or removed, for a file the discarded turns created).
type revertResult struct {
	Error         string   `json:"error"`
	AffectedFiles []string `json:"affectedFiles"`
	TotalFiles    int      `json:"totalFiles"`
	Success       bool     `json:"success"`
}

// revertToMessage performs the KAS round trip and normalises its two
// failure channels into one error plus the HTTP status to report: a
// transport/JSON-RPC failure comes back as an error, while a refusal KAS
// can explain comes back `success:false` with a reason, forwarded verbatim
// since it is more specific than anything vibekit could infer.
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

// userMessageIndex locates the revert target: the user message with this
// id. User-only because KAS requires it, and only user messages share an
// id space with KAS — an assistant turn carries KAS's own id. Returns -1
// when absent.
func userMessageIndex(messages []vibekit.Message, id string) int {
	for i := range messages {
		if messages[i].ID == id && messages[i].Role == vibekit.RoleUser {
			return i
		}
	}
	return -1
}

// CmdSetEffort sets the chat's reasoning-effort level. On v3 (KAS) effort is
// a session config option, so a running session is switched in place via
// session/set_config_option; the level is then persisted on the chat and
// applied to any later session through StartOpts.Effort.
//
// Effort is per-chat: two chats can disagree, and switching models no
// longer discards the previous model's choice.
//
// A chat with no bridge is not a 409: the persisted level is enough, and
// auto-create mirrors CmdSetMode — a fresh chat is client-side only until
// its first prompt.
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
