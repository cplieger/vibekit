package command

// The tangent: a second chat that starts from another chat's real context
// and then diverges. A rewind edits the conversation you are in (rewind.go);
// a tangent keeps it and opens another beside it.
//
// The parent bridge is resumed on demand and `session/fork` carries KAS's own
// context into the new session. The parent record must survive until the
// tangent is minted.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/durable"
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
			// Through the coordinator even on the replay: the first attempt
			// can have created the chat and then failed its tab write, and
			// this finishes that. Open is idempotent, so the ordinary
			// replay costs one scan and emits nothing.
			var opened ChatOpened
			opened, err = mem.CreateChatAndOpen(ctx, forkCreate(p, "", c, c.ACPSessionID))
			if err != nil {
				return nil, err
			}
			// The repeat finishes a load the first attempt never STARTED, and
			// skips one whose session that attempt already lost.
			var inherited bool
			opened.Chat, inherited = loadForkedHistory(ctx, bridges, chats, opened.Chat)
			outcome := forkOutcomeOf(opened.Chat.ACPSessionID, inherited)
			slog.Info("tangent: repeat op resolved to the chat it already opened",
				"chat", chatID, "parent", p.ParentChatID, "outcome", outcome)
			return openedResponse(&opened, map[string]any{
				"outcome":    outcome,
				"session_id": opened.Chat.ACPSessionID,
			}), nil
		}
		// The op was recorded but its chat is not there: the first attempt
		// reserved the id and then failed. Fall through and fork for real.
	}

	parent, ok := chats.Get(ctx, p.ParentChatID)
	if !ok {
		return nil, StatusError(http.StatusNotFound, errForkParentUnknown)
	}

	// The parent's model and mode ride along so the tangent's answers come
	// from the same agent that produced the conversation it inherited.
	sessionID := forkSession(ctx, bridges, ws, p)

	opened, err := mem.CreateChatAndOpen(ctx, forkCreate(p, chatID, parent, sessionID))
	if err != nil {
		return nil, err
	}
	if opened.Replay && sessionID != "" && opened.Chat.ACPSessionID != sessionID {
		slog.Warn("tangent: a concurrent attempt of this op already opened the chat, so this attempt's forked session is bound to nothing",
			"chat", opened.Chat.ID, "parent", p.ParentChatID, "orphaned_session", sessionID)
	}

	var inherited bool
	opened.Chat, inherited = loadForkedHistory(ctx, bridges, chats, opened.Chat)
	outcome := forkOutcomeOf(opened.Chat.ACPSessionID, inherited)

	slog.Info("tangent opened",
		"chat", opened.Chat.ID, "parent", p.ParentChatID,
		"outcome", outcome, "acp_session", opened.Chat.ACPSessionID, "tab", opened.Subject.ID)
	return openedResponse(&opened, map[string]any{
		"outcome":    outcome,
		"session_id": opened.Chat.ACPSessionID,
	}), nil
}

// loadForkedHistory gives the tangent the transcript KAS already holds for it and
// answers with the RE-READ record, plus whether that record still names the session
// the fork produced. The retirement chain is what says so: the fork binds its session
// inside the create, so a non-empty chain means that session was retired and the
// tangent inherited nothing.
//
// It refuses nothing, and a failure does not heal itself: the chat stays as it stands.
// Why the load runs here, and what a late swap reaches: `vibekit.md`'s `fork_chat` row.
func loadForkedHistory(ctx context.Context, bridges BridgeAccess, chats ChatStore, c *vibekit.Chat) (*vibekit.Chat, bool) {
	if c == nil || c.ACPSessionID == "" {
		return c, false
	}
	// Deliberately not a comparison against the id THIS attempt forked: a losing
	// concurrent attempt's record names the WINNER's session, and that one inherited.
	if len(c.PriorACPSessionIDs) > 0 {
		return c, false
	}
	want := c.ACPSessionID
	if len(c.Messages) == 0 {
		id := vibekit.ChatID(c.ID)
		resumeForkedSession(ctx, bridges, id, want)
		if refreshed, exists := chats.Get(ctx, id); exists {
			c = refreshed
		}
	}
	return c, c.ACPSessionID == want
}

// resumeForkedSession opens the TANGENT's own bridge and waits for its replay to
// land. Its own bridge rather than the parent's, which is already resumed: the
// projection is keyed by chat, so a load issued on the parent's bridge would ingest
// the fork's history into the PARENT's transcript.
func resumeForkedSession(ctx context.Context, bridges BridgeAccess, chatID vibekit.ChatID, want string) {
	// durable for the OPEN, the request's own for the WAIT: a cancelled spawn takes
	// tryLoadSession's failure branch, which DETACHES the forked session.
	bridge, err := bridges.OpenBridge(durable.Context(ctx), chatID, "")
	if err != nil || bridge == nil {
		slog.Warn("tangent: the forked session could not be resumed, so the tangent opens empty",
			"chat", chatID, "acp_session", want, keyError, err)
		return
	}
	if string(bridge.SessionID()) != want {
		slog.Warn("tangent: the resume fell through to a fresh session, so the tangent lost its inherited context",
			"chat", chatID, "want", want, "got", bridge.SessionID())
		return
	}
	if err := bridges.AwaitReplayAdopted(ctx, chatID); err != nil {
		slog.Warn("tangent: the replay was not adopted in time, so the tangent opens empty until the swap announces the replacement",
			"chat", chatID, keyError, err)
	}
}

// forkOutcomeOf reads a tangent's path off what it ended up with: only a chat still
// bound to the session the fork produced was forked, so a refused fork AND a resume
// that fell through to a fresh session both report fresh.
func forkOutcomeOf(sessionID string, inherited bool) string {
	if sessionID == "" || !inherited {
		return vibekit.ForkOutcomeFresh
	}
	return vibekit.ForkOutcomeForked
}

// forkCreate is the tangent's create request: one builder for both call
// sites (replay and fresh fork) so the record's shape cannot drift between
// them. The tab hangs under the parent's tab, which is what makes a tangent
// read as a tangent; a parent with no open tab promotes it to top level.
func forkCreate(p vibekit.ForkChatCommand, chatID vibekit.ChatID, parent *vibekit.Chat, sessionID string) ChatCreate {
	return ChatCreate{
		OpID:        p.OpID,
		ChatID:      chatID,
		RequireChat: p.ParentChatID,
		ParentChat:  p.ParentChatID,
		Init: func(c *vibekit.Chat) {
			c.Name = vibekit.DefaultChatName
			c.Model = parent.Model
			c.CurrentModeID = parent.CurrentModeID
			c.Effort = parent.Effort
			// The review gate is inherited like the rest, overriding the global
			// default the coordinator seeded: a tangent continues the same
			// conversation, so taking the model, the mode and the effort while
			// dropping the gate would silently downgrade safety — a supervised
			// chat's tangent would start writing files without asking.
			c.SupervisedMode = parent.SupervisedMode
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
// session id, or "" when the tangent has to start fresh. Every refusal is a
// warning and an empty string, since the caller's answer is always to open the
// tangent without a bound session.
func forkSession(ctx context.Context, bridges BridgeAccess, ws Workspace, p vibekit.ForkChatCommand) string {
	bridge := bridges.Bridge(p.ParentChatID)
	if bridge == nil || bridge.SessionID() == "" {
		// Branching a conversation requires its context, so resume its bridge on
		// demand. CmdRewindChat accepts the same trade for a context-dependent
		// operation on a bridgeless chat.
		//
		// Empty model on purpose: the parent keeps the model recorded on its chat.
		var err error
		bridge, err = bridges.OpenBridge(ctx, p.ParentChatID, "")
		if err != nil || bridge == nil || bridge.SessionID() == "" {
			slog.Warn("tangent: parent bridge unavailable, starting fresh",
				"parent", p.ParentChatID, keyError, err)
			return ""
		}
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
		slog.Warn("tangent: session/fork failed, starting fresh",
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
		slog.Warn("tangent: session/fork returned no usable session id, starting fresh",
			"parent", p.ParentChatID)
		return ""
	}
	return out.SessionID
}
