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

// CmdForkChat opens a tangent off another chat and returns the chat it created
// plus the tab it opened for it.
//
// The new chat's id is MINTED here when the envelope carries none. The response
// carries the chat because nothing else can: the caller opens a sub-tab for the
// tangent, and before this it could only address it by having invented the id.
func CmdForkChat(ctx context.Context, bridges BridgeAccess, chats ChatStore, ws Workspace, mem *Membership, cmd *vibekit.ClientCommand) (any, error) {
	var p vibekit.ForkChatCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !ids.ValidChatID(string(p.ParentChatID)) || len(p.Title) > vibekit.MaxChatNameBytes {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !ValidIdent(p.OpID) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}

	parent, ok := chats.Get(ctx, p.ParentChatID)
	if !ok {
		return nil, StatusError(http.StatusNotFound, errForkParentUnknown)
	}

	// The op is READ before session/fork, not after, and that ordering is the
	// whole reason the ledger is consulted here: a retry that already produced a
	// chat must not ask KAS to fork again. A second fork would create a second
	// session nothing binds, and on the primed fallback it would spend the priming
	// budget twice for one gesture.
	//
	// A READ rather than the coordinator's resolve, because the fork round trip
	// must not happen under the operation lock: a bridge Call has no client-side
	// timeout, so one wedged agent process would block every tab mutation in the
	// workspace. The coordinator's own resolve is still the authority for the
	// mint — see Membership.ResolvedChat.
	chatID, replay := mem.ResolvedChat(p.OpID)
	if cmd.ChatID != "" {
		chatID, replay = cmd.ChatID, false
	}
	if chatID != "" && chatID == p.ParentChatID {
		return nil, StatusError(http.StatusBadRequest, errForkParentIsSelf)
	}
	if replay {
		if c, exists := chats.Get(ctx, chatID); exists {
			// Answer with the chat the first attempt made, and DERIVE its outcome
			// from the record rather than restating this attempt's: a chat bound to
			// a session was forked, one with no session was primed. Reporting
			// `forked` unconditionally here would make the field a guess in exactly
			// the case a report about a vague answer would be reading it.
			outcome := forkOutcomeOf(c.ACPSessionID)
			slog.Info("tangent: repeat op resolved to the chat it already opened",
				"chat", chatID, "parent", p.ParentChatID, "outcome", outcome)
			// Through the coordinator even on the replay, because the first attempt
			// can have created the chat and then failed its tab write: this is where
			// that tab is finished. Open is idempotent, so the ordinary replay costs
			// one scan and emits nothing.
			opened, err := mem.CreateChatAndOpen(ctx, forkCreate(p, chatID, parent, c.ACPSessionID))
			if err != nil {
				return nil, err
			}
			return openedResponse(opened, map[string]any{
				"outcome":    outcome,
				"session_id": c.ACPSessionID,
			}), nil
		}
		// The op was recorded but its chat is not there: the first attempt
		// reserved the id and then failed. Fall through and fork for real.
	}

	// The parent's model and mode ride along so the tangent's answers come from
	// the same agent that produced the conversation it inherited. Read here
	// rather than sent by the client: the record is the truth about both, and a
	// client value could be a tab's stale projection.
	sessionID := forkSession(ctx, bridges, ws, p)

	opened, err := mem.CreateChatAndOpen(ctx, forkCreate(p, chatID, parent, sessionID))
	if err != nil {
		return nil, err
	}
	// The record is what the outcome is read off, including here: a genuinely
	// CONCURRENT second attempt of one op resolves to the chat the first made, so
	// this attempt's own session is not the one bound, and reporting `forked` from
	// it would describe a session nothing uses. The coordinator's resolve is the
	// authority for the mint, so this is where that race becomes visible.
	outcome := forkOutcomeOf(opened.Chat.ACPSessionID)
	if opened.Replay && sessionID != "" && opened.Chat.ACPSessionID != sessionID {
		slog.Warn("tangent: a concurrent attempt of this op already opened the chat, so this attempt's forked session is bound to nothing",
			"chat", opened.Chat.ID, "parent", p.ParentChatID, "orphaned_session", sessionID)
	}

	if outcome == vibekit.ForkOutcomePrimed {
		// Marked AFTER the record exists, so nothing can observe a prime note for
		// a chat that failed to create.
		bridges.PrimeFromChat(vibekit.ChatID(opened.Chat.ID), p.ParentChatID)
	}

	slog.Info("tangent opened",
		"chat", opened.Chat.ID, "parent", p.ParentChatID,
		"outcome", outcome, "acp_session", opened.Chat.ACPSessionID, "tab", opened.Subject.ID)
	return openedResponse(opened, map[string]any{
		"outcome":    outcome,
		"session_id": opened.Chat.ACPSessionID,
	}), nil
}

// forkOutcomeOf reads a tangent's path off the record it produced: a chat bound
// to a session was forked, one with no session was primed.
//
// Derived rather than remembered, because the two callers that need it are the
// replay and the fresh path and only the record can answer for both.
func forkOutcomeOf(sessionID string) string {
	if sessionID == "" {
		return vibekit.ForkOutcomePrimed
	}
	return vibekit.ForkOutcomeForked
}

// forkCreate is the tangent's create request: what a forked chat's record holds,
// and where its tab hangs.
//
// One builder for both call sites (the replay and the real fork) so the record's
// shape cannot drift between them — the replay path exists to FINISH what the
// first attempt started, and a different Init there would mean finishing it
// differently.
//
// The tab hangs under the PARENT's tab, which is what makes a tangent read as a
// tangent rather than as an unrelated chat that appeared. The parent CHAT is
// named rather than its tab so the coordinator resolves it under its own lock;
// a parent with no open tab promotes the tangent to top level.
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
				// RecordSession rather than assignment: it is the only sanctioned
				// writer of this field and it keeps the chain invariant the reaper's
				// keep-list depends on.
				c.RecordSession(sessionID)
			}
		},
	}
}

// forkSession asks KAS to branch the parent's session and returns the new
// session id, or "" when the tangent has to fall back to priming.
//
// Every refusal is a WARN and an empty string rather than an error, because the
// caller's answer to all of them is identical: open the tangent primed. The
// reasons are still distinguished in the log, since "no bridge" (the parent was
// never prompted, or its process is gone) and "KAS refused" want different
// follow-ups.
//
// It logs the PARENT and not the new chat, because at this point the new chat has
// no id: minting moved inside the coordinator so the capacity reservation could
// run before it, and this round trip deliberately happens outside that lock.
func forkSession(ctx context.Context, bridges BridgeAccess, ws Workspace, p vibekit.ForkChatCommand) string {
	bridge := bridges.Bridge(p.ParentChatID)
	if bridge == nil || bridge.SessionID() == "" {
		// No live session to branch. Deliberately NOT started here: spawning a
		// bridge for the parent as a side effect of opening a tangent would
		// resume a conversation the user did not ask to resume, and the primed
		// path reaches the same place from the record alone.
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
		// A reply with no usable session id is a refusal however it is spelled.
		// Validated rather than trusted for CmdResumeSession's reason: the value
		// reaches a filesystem path inside KAS and vibekit's own reaper keep-list.
		slog.Warn("tangent: session/fork returned no usable session id, priming instead",
			"parent", p.ParentChatID)
		return ""
	}
	return out.SessionID
}
