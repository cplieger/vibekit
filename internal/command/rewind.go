package command

// Rewind: revert THIS chat to a past turn. There is no branch, no promote and
// no discard — the whole family apparatus is gone, because a fork made a SECOND
// chat and rewinding was always meant to edit the one you are in.
//
// The operation is one KAS call. `_kiro/checkpoint/revertMultiple` drops the
// addressed user message and everything after it, rolls the files back from
// KAS's own snapshots, and appends a `checkpoint_revert` tombstone to its
// session log so the truncation is durable. Vibekit's Rewind and its former
// turn-level Restore are therefore the same operation: the transcript and the
// files move together, because KAS moves them together.

import (
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
// The truncation is NOT redundant with the revert, and it is not a
// re-derivation of KAS's answer either. KAS's tombstone makes ITS log correct;
// vibekit's chat file still holds the dropped turns, and `mergeProjection`
// deliberately preserves anything newer than a replay's last message (KAS's log
// is not fsynced, so an absent tail is normally a durability gap rather than an
// intended deletion). Left alone, the next resume would therefore hand the
// reverted turns straight back. A revert is the one case where the tail is gone
// ON PURPOSE, and only vibekit knows that at this moment, so it cuts its own
// record here.
//
// That is also why there is no session/load: the live session's context is
// already reverted in place by the handler, the tombstone covers every future
// load, and a reload would only re-derive an answer both sides already agree on
// at the price of tearing down the bridge.
//
// Mid-turn is refused, not queued. KAS throws on `session.abortController`
// ("Cannot revert while the agent is still running"), and refuses a concurrent
// revert per session — so both races are settled upstream and vibekit forwards
// the reason instead of reimplementing the guard.
func CmdRewindChat(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *vibekit.ClientCommand) { //nolint:revive // dispatcher handler signature
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p vibekit.RewindChatCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || p.MessageID == "" {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}

	chat, ok := deps.ChatStore().Get(ctx, cmd.ChatID)
	if !ok {
		d.RespondErr(w, http.StatusNotFound, ErrChatNotFound)
		return
	}
	idx := userMessageIndex(chat.Messages, p.MessageID)
	if idx < 0 {
		d.RespondErr(w, http.StatusBadRequest, errRewindTargetNotFound)
		return
	}

	bridge := deps.GetBridge(cmd.ChatID)
	if bridge == nil {
		// No live session to revert. The files and the transcript move together
		// or not at all, so truncating the record alone is not an option.
		d.RespondErr(w, http.StatusConflict, errNoBridge)
		return
	}

	result, status, err := revertToMessage(ctx, bridge, p.MessageID)
	if err != nil {
		slog.Warn("rewind: revert failed", "chat", cmd.ChatID, "status", status, keyError, err)
		d.RespondErr(w, status, err)
		return
	}

	// Cut at idx, not idx+1: the addressed message is discarded WITH its
	// successors (KAS slices from the target inclusive), so the prompt at that
	// turn is gone and has to be retyped. That is the operation, not a bug.
	if mErr := deps.ChatStore().Mutate(ctx, cmd.ChatID, func(c *vibekit.Chat, exists bool) bool {
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
		d.RespondErr(w, http.StatusInternalServerError, mErr)
		return
	}

	slog.Info("chat rewound",
		"chat", cmd.ChatID, "message", p.MessageID,
		"dropped_messages", len(chat.Messages)-idx,
		"restored_files", len(result.AffectedFiles), "total_files", result.TotalFiles)
	d.Respond(w, cmd.RequestID, responseWith(map[string]any{
		"restored_files": result.AffectedFiles,
	}))
}

// revertResult is KAS's reply to a revert. affectedFiles are the paths it put
// back (or removed, for a file the discarded turns created).
type revertResult struct {
	Error         string   `json:"error"`
	AffectedFiles []string `json:"affectedFiles"`
	TotalFiles    int      `json:"totalFiles"`
	Success       bool     `json:"success"`
}

// revertToMessage performs the KAS round trip and normalises its TWO failure
// channels into one error plus the HTTP status to report.
//
// Two channels because KAS uses both: a transport/JSON-RPC failure comes back as
// an error, while a refusal it can explain — an unknown id, a non-user target, a
// live turn, a concurrent revert, an unreadable snapshot — comes back
// `success:false` with a reason. The reason is forwarded verbatim: it is more
// specific than anything vibekit could infer, and a revert that did not happen
// must never read like one that did.
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
		reason := result.Error
		if reason == "" {
			reason = "revert failed"
		}
		return result, http.StatusConflict, errors.New(reason)
	}
	return result, http.StatusOK, nil
}

// userMessageIndex locates the revert target: the USER message with this id.
//
// User-only because KAS requires it — a non-user target is refused in-band with
// the type it found — and because only user messages share an id space with KAS
// at all. It echoes back the messageId vibekit sends on session/prompt, while an
// assistant turn carries KAS's own `<uuid>-say`, so an assistant id would be
// unknown to the revert regardless of role checking. Returns -1 when absent.
func userMessageIndex(messages []vibekit.Message, id string) int {
	for i := range messages {
		if messages[i].ID == id && messages[i].Role == vibekit.RoleUser {
			return i
		}
	}
	return -1
}

// CmdSetEffort sets the chat's reasoning-effort level. On v3 (KAS) effort is a
// session config option, so a running session is switched in place via
// session/set_config_option (configId "effortLevel"); the level is then
// persisted ON THE CHAT and applied to any later session at launch through
// StartOpts.Effort.
//
// Effort is PER-CHAT (the fourth composer setting to be, beside model, mode and
// supervised). It used to be one global `model_effort` setting keyed by the LAST
// model, which meant two chats could not disagree and switching models silently
// discarded the previous model's choice.
//
// A chat with no bridge is NOT a 409 any more, which is the other half of the
// same move: the persisted level is enough, exactly as CmdSetMode's is, and the
// client no longer needs an empty-chat branch that writes a global setting
// instead. Auto-create mirrors CmdSetMode for the same reason — a fresh chat is
// client-side only until its first prompt, so without it every pick before the
// first message 404'd and the control rolled back.
func CmdSetEffort(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *vibekit.ClientCommand) { //nolint:revive // dispatcher handler signature
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p vibekit.SetEffortCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || !p.Level.Valid() {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}

	// Switch live first (fail fast) when a bridge is running, so a refusal is
	// reported rather than persisted as a level the session never took.
	if bridge := deps.GetBridge(cmd.ChatID); bridge != nil {
		if _, err := bridge.Call(ctx, vibekit.MethodSetConfigOption, SessionParams(bridge, map[string]any{
			"configId": vibekit.ConfigOptionEffort,
			"value":    string(p.Level),
		})); err != nil {
			slog.Warn("set_effort: bridge call failed", "chat", cmd.ChatID, keyError, err)
			d.RespondErr(w, http.StatusBadGateway, err)
			return
		}
	}

	if err := deps.ChatStore().Mutate(ctx, cmd.ChatID, func(c *vibekit.Chat, exists bool) bool {
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
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}

	slog.Info("effort set", "chat", cmd.ChatID, "level", p.Level)
	d.Respond(w, cmd.RequestID, responseWith(map[string]any{"level": p.Level}))
}
