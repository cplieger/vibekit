package command

// The tangent: a second chat that starts from another chat's real context
// and then diverges. A rewind edits the conversation you are in (rewind.go);
// a tangent keeps it and opens another beside it.
//
// Two paths, and only their fidelity differs — the tangent opens either way.
//
//   - Fork (primary): one `session/fork` on the parent's live session; KAS
//     returns a new session id carrying the parent's actual context, and the
//     new chat is created already bound to it. No re-narration, no token
//     spend.
//   - Prime (fallback): the fork was refused, so the chat is created unbound
//     and marked so its first session gets the parent's transcript as an
//     invisible priming prompt (bounded by the priming budget).
//
// The log line names which path ran, since the two are otherwise
// indistinguishable from the outside.

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

// errForkParentIsSelf guards forking a chat into itself, which would rebind
// its own session id through RecordSession and retire the session it is
// still using.
var errForkParentIsSelf = errors.New("a tangent cannot fork the chat it opens into")

// forkPayload decodes and validates the tangent command's payload.
func forkPayload(cmd *vibekit.ClientCommand) (vibekit.ForkChatCommand, error) {
	var p vibekit.ForkChatCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return p, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !ids.ValidChatID(string(p.ParentChatID)) || len(p.Title) > vibekit.MaxChatNameBytes {
		return p, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !ValidIdent(p.OpID) {
		return p, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	return p, nil
}

// CmdForkChat opens a tangent off another chat and returns the chat it
// created plus the tab it opened for it. The new chat's id is minted here
// when the envelope carries none.
func CmdForkChat(ctx context.Context, bridges BridgeAccess, chats ChatStore, ws Workspace, mem *Membership, cmd *vibekit.ClientCommand) (any, error) {
	p, err := forkPayload(cmd)
	if err != nil {
		return nil, err
	}

	parent, ok := chats.Get(ctx, p.ParentChatID)
	if !ok {
		return nil, StatusError(http.StatusNotFound, errForkParentUnknown)
	}

	// Read the ledger before session/fork: a retry that already produced a
	// chat must not ask KAS to fork again. A READ rather than the
	// coordinator's resolve, because the fork round trip must not happen
	// under the operation lock — a bridge Call has no client-side timeout.
	chatID, replay := mem.ResolvedChat(p.OpID)
	if cmd.ChatID != "" {
		chatID, replay = cmd.ChatID, false
	}
	if chatID != "" && chatID == p.ParentChatID {
		return nil, StatusError(http.StatusBadRequest, errForkParentIsSelf)
	}
	if replay {
		if c, exists := chats.Get(ctx, chatID); exists {
			// Derive the outcome from the record rather than restating this
			// attempt's: a chat bound to a session was forked, one with no
			// session was primed.
			outcome := forkOutcomeOf(c.ACPSessionID)
			slog.Info("tangent: repeat op resolved to the chat it already opened",
				"chat", chatID, "parent", p.ParentChatID, "outcome", outcome)
			// Through the coordinator even on the replay: the first attempt
			// can have created the chat and then failed its tab write, and
			// this finishes that. Open is idempotent, so the ordinary
			// replay costs one scan and emits nothing.
			var opened ChatOpened
			opened, err = mem.CreateChatAndOpen(ctx, forkCreate(p, chatID, parent, c.ACPSessionID))
			if err != nil {
				return nil, err
			}
			return openedResponse(&opened, map[string]any{
				"outcome":    outcome,
				"session_id": c.ACPSessionID,
			}), nil
		}
		// The op was recorded but its chat is not there: the first attempt
		// reserved the id and then failed. Fall through and fork for real.
	}

	// The parent's model and mode ride along so the tangent's answers come
	// from the same agent that produced the conversation it inherited.
	sessionID := forkSession(ctx, bridges, ws, p)

	opened, err := mem.CreateChatAndOpen(ctx, forkCreate(p, chatID, parent, sessionID))
	if err != nil {
		return nil, err
	}
	outcome := forkOutcomeOf(opened.Chat.ACPSessionID)
	if opened.Replay && sessionID != "" && opened.Chat.ACPSessionID != sessionID {
		slog.Warn("tangent: a concurrent attempt of this op already opened the chat, so this attempt's forked session is bound to nothing",
			"chat", opened.Chat.ID, "parent", p.ParentChatID, "orphaned_session", sessionID)
	}

	if outcome == vibekit.ForkOutcomePrimed {
		// Marked AFTER the record exists, so nothing can observe a prime
		// note for a chat that failed to create.
		bridges.PrimeFromChat(vibekit.ChatID(opened.Chat.ID), p.ParentChatID)
	}

	slog.Info("tangent opened",
		"chat", opened.Chat.ID, "parent", p.ParentChatID,
		"outcome", outcome, "acp_session", opened.Chat.ACPSessionID, "tab", opened.Subject.ID)
	return openedResponse(&opened, map[string]any{
		"outcome":    outcome,
		"session_id": opened.Chat.ACPSessionID,
	}), nil
}

// forkOutcomeOf reads a tangent's path off the record it produced: a chat
// bound to a session was forked, one with no session was primed.
func forkOutcomeOf(sessionID string) string {
	if sessionID == "" {
		return vibekit.ForkOutcomePrimed
	}
	return vibekit.ForkOutcomeForked
}

// forkCreate is the tangent's create request: one builder for both call
// sites (replay and fresh fork) so the record's shape cannot drift between
// them. The tab hangs under the parent's tab, which is what makes a tangent
// read as a tangent; a parent with no open tab promotes it to top level.
func forkCreate(p vibekit.ForkChatCommand, chatID vibekit.ChatID, parent *vibekit.Chat, sessionID string) ChatCreate {
	return ChatCreate{
		OpID:       p.OpID,
		ChatID:     chatID,
		ParentChat: p.ParentChatID,
		Init: func(c *vibekit.Chat) {
			c.Name = vibekit.DefaultChatName
			c.Model = parent.Model
			c.CurrentModeID = parent.CurrentModeID
			c.Effort = parent.Effort
			if sessionID != "" {
				// RecordSession, not assignment: it is the sanctioned writer
				// of this field and keeps the reaper's keep-list chain
				// invariant.
				c.RecordSession(sessionID)
			}
		},
	}
}

// forkSession asks KAS to branch the parent's session and returns the new
// session id, or "" when the tangent has to fall back to priming. Every
// refusal is a WARN and an empty string, since the caller's answer to all
// of them is identical: open the tangent primed.
func forkSession(ctx context.Context, bridges BridgeAccess, ws Workspace, p vibekit.ForkChatCommand) string {
	bridge := bridges.Bridge(p.ParentChatID)
	if bridge == nil || bridge.SessionID() == "" {
		// No live session to branch. Deliberately not started here:
		// spawning a bridge as a side effect of a tangent would resume a
		// conversation the user did not ask to resume.
		slog.Info("tangent: parent has no live session, priming instead",
			"parent", p.ParentChatID)
		return ""
	}

	meta := map[string]any{"createdReason": vibekit.CreatedReasonTangent}
	if p.Title != "" {
		meta["title"] = p.Title
	}
	resp, err := bridge.Call(ctx, vibekit.MethodSessionFork, SessionParams(bridge, map[string]any{
		"cwd":   ws.Dir,
		"_meta": map[string]any{"kiro": meta},
	}))
	if err != nil {
		slog.Warn("tangent: session/fork failed, priming instead",
			"parent", p.ParentChatID, keyError, err)
		return ""
	}
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if resp != nil && resp.Result != nil {
		_ = json.Unmarshal(resp.Result, &out)
	}
	if !ids.ValidSessionID(out.SessionID) {
		// A reply with no usable session id is a refusal however it is
		// spelled. Validated because the value reaches a filesystem path
		// inside KAS and vibekit's own reaper keep-list.
		slog.Warn("tangent: session/fork returned no usable session id, priming instead",
			"parent", p.ParentChatID)
		return ""
	}
	return out.SessionID
}
