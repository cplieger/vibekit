package command

// The tangent: a second chat that starts from another chat's real context and
// then diverges.
//
// A tangent is exactly the operation a SECOND session is the right answer to,
// which is what distinguishes it from a rewind. A rewind edits the conversation
// you are in (see rewind.go); a tangent keeps it and opens another one beside it,
// so both histories survive and neither writes to the other afterwards.
//
// TWO PATHS, and only their fidelity differs — the tangent opens either way.
//
//   - FORK (primary). One `session/fork` on the parent's live session; KAS
//     returns a new session id carrying the parent's actual context, and the new
//     chat is created already BOUND to it, exactly as CmdResumeSession binds an
//     adopted one. So the transcript arrives from the session/load replay
//     projection and vibekit copies no messages. No re-narration, no budget
//     ceiling, no preamble telling the model about its own past, no token spend.
//
//   - PRIME (fallback). The fork was refused, so there is no session id to bind.
//     The chat is created unbound and marked so its FIRST session gets the
//     parent's transcript as an invisible priming prompt (BuildHistory, bounded
//     by the priming budget with an omission notice when it does not fit).
//     Strictly worse, and it is the whole reason the fallback exists rather than
//     the reason to prefer it.
//
// The fallback is not defensive padding. Every fork precondition vibekit can
// read is checked here, but a refusal can still come from inside KAS — and a
// tangent that simply fails is a feature the user cannot rely on, while a primed
// tangent is one that works less well. The log line names which path ran, because
// from the outside the two are indistinguishable and the difference is exactly
// what a report about a vague answer would need.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// errForkParentUnknown is returned when the chat being forked has no record.
var errForkParentUnknown = errors.New("the chat this tangent came from no longer exists")

// errForkParentIsSelf guards the one shape that would corrupt rather than fail:
// forking a chat into itself would rebind its own session id through
// RecordSession and retire the session it is still using.
var errForkParentIsSelf = errors.New("a tangent cannot fork the chat it opens into")

// CmdForkChat opens a tangent off another chat.
//
//nolint:revive // context-as-argument: dispatcher handler signature
func CmdForkChat(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *vibekit.ClientCommand) {
	deps := d.Deps()
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p vibekit.ForkChatCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}
	if !ids.ValidChatID(string(p.ParentChatID)) || len(p.Title) > vibekit.MaxChatNameBytes {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}
	if p.ParentChatID == cmd.ChatID {
		d.RespondErr(w, http.StatusBadRequest, errForkParentIsSelf)
		return
	}

	parent, ok := deps.ChatStore().Get(ctx, p.ParentChatID)
	if !ok {
		d.RespondErr(w, http.StatusNotFound, errForkParentUnknown)
		return
	}

	// The parent's model and mode ride along so the tangent's answers come from
	// the same agent that produced the conversation it inherited. Read here
	// rather than sent by the client: the record is the truth about both, and a
	// client value could be a tab's stale projection.
	sessionID := forkSession(ctx, deps, p, cmd.ChatID)
	outcome := vibekit.ForkOutcomeForked
	if sessionID == "" {
		outcome = vibekit.ForkOutcomePrimed
	}

	if err := deps.ChatStore().Mutate(ctx, cmd.ChatID, func(c *vibekit.Chat, exists bool) bool {
		// Refuse to reshape an existing chat, for CmdResumeSession's reason:
		// binding a live chat to another session strands its own (the transcript
		// stays on disk unreferenced, so the reaper sweeps it) and silently
		// changes the history under a conversation someone is reading.
		if exists {
			return false
		}
		c.Name = vibekit.DefaultChatName
		c.Model = parent.Model
		c.CurrentModeID = parent.CurrentModeID
		c.Effort = parent.Effort
		if sessionID != "" {
			// RecordSession rather than assignment: it is the only sanctioned
			// writer of this field and it keeps the chain invariant the reaper's
			// keep-list depends on.
			c.RecordSession(sessionID)
		}
		return true
	}); err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}

	if outcome == vibekit.ForkOutcomePrimed {
		// Marked AFTER the record exists, so nothing can observe a prime note for
		// a chat that failed to create.
		deps.PrimeFromChat(cmd.ChatID, p.ParentChatID)
	}

	slog.Info("tangent opened",
		"chat", cmd.ChatID, "parent", p.ParentChatID,
		"outcome", outcome, "acp_session", sessionID)
	d.Respond(w, cmd.RequestID, responseWith(map[string]any{
		"outcome":    outcome,
		"session_id": sessionID,
	}))
}

// forkSession asks KAS to branch the parent's session and returns the new
// session id, or "" when the tangent has to fall back to priming.
//
// Every refusal is a WARN and an empty string rather than an error, because the
// caller's answer to all of them is identical: open the tangent primed. The
// reasons are still distinguished in the log, since "no bridge" (the parent was
// never prompted, or its process is gone) and "KAS refused" want different
// follow-ups.
func forkSession(ctx context.Context, deps Dependencies, p vibekit.ForkChatCommand, newChat vibekit.ChatID) string {
	bridge := deps.GetBridge(p.ParentChatID)
	if bridge == nil || bridge.SessionID() == "" {
		// No live session to branch. Deliberately NOT started here: spawning a
		// bridge for the parent as a side effect of opening a tangent would
		// resume a conversation the user did not ask to resume, and the primed
		// path reaches the same place from the record alone.
		slog.Info("tangent: parent has no live session, priming instead",
			"chat", newChat, "parent", p.ParentChatID)
		return ""
	}

	meta := map[string]any{"createdReason": vibekit.CreatedReasonTangent}
	if p.Title != "" {
		meta["title"] = p.Title
	}
	resp, err := bridge.Call(ctx, vibekit.MethodSessionFork, SessionParams(bridge, map[string]any{
		"cwd":   deps.WorkDir(),
		"_meta": map[string]any{"kiro": meta},
	}))
	if err != nil {
		slog.Warn("tangent: session/fork failed, priming instead",
			"chat", newChat, "parent", p.ParentChatID, keyError, err)
		return ""
	}
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if resp != nil && resp.Result != nil {
		_ = json.Unmarshal(resp.Result, &out)
	}
	if !ids.ValidSessionID(out.SessionID) {
		// A reply with no usable session id is a refusal however it is spelled.
		// Validated rather than trusted for CmdResumeSession's reason: the value
		// reaches a filesystem path inside KAS and vibekit's own reaper keep-list.
		slog.Warn("tangent: session/fork returned no usable session id, priming instead",
			"chat", newChat, "parent", p.ParentChatID)
		return ""
	}
	return out.SessionID
}
