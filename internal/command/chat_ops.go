package command

// Chat CRUD commands: create, delete, cancel, and permission forwarding.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// cancelGrace is how long a cooperative session/cancel gets to be reflected
// in a turn end before vibekit unblocks the turn itself. Copied from
// KiroCrew's `_CANCEL_GRACE_SECS` (src/kiro_crew/acp/{client,session_handle}.py)
// rather than guessed.
const cancelGrace = 10 * time.Second

// CmdCreateChat creates a new chat with the given metadata.
func CmdCreateChat(ctx context.Context, chats ChatStore, cmd *vibekit.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p vibekit.CreateChatCommand
	if len(cmd.Payload) > 0 {
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
		}
	}
	name := p.Name
	if name == "" {
		name = vibekit.DefaultChatName
	}
	if len(name) > vibekit.MaxChatNameBytes {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !ValidIdent(p.Model) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	err := chats.Mutate(ctx, cmd.ChatID, func(c *vibekit.Chat, exists bool) bool {
		if exists {
			return false
		}
		c.Name = name
		c.Model = p.Model
		return true
	})
	if err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	return responseOK, nil
}

// CmdDeleteChat removes a chat: tear down its side effects, then delete the
// record.
//
// It used to cascade through DeleteFamily, whose whole subject was rewind
// CHILDREN — a chat could own other chats, so deletion needed an ordering
// contract (children first, so no crash window left a child pointing at a
// deleted parent) and a truthful partial-failure response (`failed_children`).
// A rewind reverts the chat it is in now, so no chat owns another and there is
// nothing to order or to half-fail. The transition, its ordering guarantee and
// the `failed_children` response are all gone rather than kept as a
// single-element loop.
func CmdDeleteChat(ctx context.Context, chats ChatStore, teardown ChatTeardown, cmd *vibekit.ClientCommand) (any, error) {
	// Delete implies close semantics, and DeleteChatState owns that: it cancels
	// the chat's runs before dropping the bridge, because a run is durable state
	// a dead bridge only PAUSES — without it, deleting a chat mid-run left the run
	// to revive and edit files attributed to a chat that no longer exists.
	teardown.DeleteChatState(ctx, cmd.ChatID)
	if err := chats.Delete(ctx, cmd.ChatID); err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	slog.Info("chat deleted", "chat_id", cmd.ChatID)
	return responseOK, nil
}

// CmdCancel cancels the active turn, if any.
func CmdCancel(ctx context.Context, bridges BridgeAccess, perms PendingPermAccess, terms TerminalAccess, cmd *vibekit.ClientCommand) (any, error) {
	// Only the pending PERMISSIONS are cleared. There is no staging queue to
	// flush and no per-turn trust to drop: KAS owns the write gate, and cancelling
	// a turn reverts its own approval (measured — session/cancel is the documented
	// escape from an unanswered approval and it reverts correctly).
	perms.ClearPendingPermsForChat(cmd.ChatID)

	// The interrupt's process half (§5.6 R3): stopping the MODEL does not stop
	// the processes its turn already spawned — cancelling mid-`npm test` left
	// the command running, owned by nobody, streaming into a turn that no
	// longer existed. Scoped to the turn's own terminals; a background command
	// an earlier turn started on purpose is not this gesture's to kill.
	terms.KillForTurn(cmd.ChatID)

	sb := bridges.Bridge(cmd.ChatID)
	if sb == nil {
		return responseOK, nil
	}
	if err := sb.Notify(ctx, vibekit.MethodCancel, SessionParams(sb)); err != nil {
		slog.Error("cancel failed", "chat_id", cmd.ChatID, keyError, err)
	}
	// Unresponsive-cancel budget. session/cancel is a notification, so nothing
	// acks it directly: the turn ends only when KAS answers the pending
	// session/prompt with a cancelled stop reason. If it never does, the prompt
	// Call blocks forever and the chat stays busy, refusing every later prompt
	// with 409. After the grace expires the prompt's context is cancelled, which
	// makes Call return and runs the ordinary prompt-failure finalization.
	//
	// The 10s value is KiroCrew's `_CANCEL_GRACE_SECS`, adopted rather than
	// invented. Its own comment records why it does NOT kill the process there
	// ("impossible on a multiplexed runtime") — vibekit is one process per chat,
	// so it does not need to: cancelling the context is enough and leaves the
	// session intact, so the chat stays resumable instead of being restarted.
	if !sb.ArmCancelGrace(sb.PromptGeneration(), cancelGrace) {
		slog.Debug("cancel: no in-flight prompt to arm a grace budget against", "chat_id", cmd.ChatID)
	}
	return responseOK, nil
}

// CmdCloseChat is the tab-close teardown: the user closed the chat's tab, and
// the gesture means "kill all of it" (user decision) — the turn, the runs, the
// process. The chat RECORD is untouched; under retention a closed chat is just
// a chat without a tab, and reopening it session/loads everything back.
//
// Ordering is the contract. The turn is cancelled FIRST (session/cancel, the
// graceful stop — the model stops rather than being severed mid-write), then
// the chat's runs (durable state; killing the process would only pause them,
// and a paused run later revives and edits files nobody is watching), then the
// process teardown, which also flushes the in-flight buffer and kills the
// chat's agent terminals.
func CmdCloseChat(ctx context.Context, bridges BridgeAccess, perms PendingPermAccess, teardown ChatTeardown, cmd *vibekit.ClientCommand) (any, error) {
	perms.ClearPendingPermsForChat(cmd.ChatID)
	if sb := bridges.Bridge(cmd.ChatID); sb != nil {
		if err := sb.Notify(ctx, vibekit.MethodCancel, SessionParams(sb)); err != nil {
			slog.Warn("close: turn cancel failed", "chat_id", cmd.ChatID, keyError, err)
		}
	}
	teardown.CloseChatState(ctx, cmd.ChatID)
	return responseOK, nil
}

// CmdPermission forwards the user's permission dialog choice to kiro-cli.
func CmdPermission(ctx context.Context, bridges BridgeAccess, perms PendingPermAccess, cmd *vibekit.ClientCommand) (any, error) {
	sb := bridges.Bridge(cmd.ChatID)
	if sb == nil {
		return nil, StatusError(http.StatusBadRequest, errNoBridge)
	}
	var p vibekit.PermissionResponseCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// Claim the request BEFORE answering it. Two tabs on one chat both see the
	// card, and kiro-cli discards the second answer for a request id it has
	// already resolved — silently, so the choice that won was decided there
	// rather than here. Losing the take means somebody else answered, which is
	// not this request's failure to report as one: 409 with a code the client
	// can explain.
	if !perms.TakePendingPerm(p.RequestID, vibekit.SettledByUser) {
		return nil, StatusError(http.StatusConflict, errAlreadyAnswered)
	}
	// A turn approval answers on the SAME reply, with per-file decisions in
	// _meta. Built through one helper so the omitted-id-means-reject rule lives in
	// exactly one place.
	outcome := vibekit.PermissionOutcomeWithFileDecisions(p.OptionID, p.FileDecisions)
	if err := sb.Respond(ctx, p.RequestID, outcome, nil); err != nil {
		slog.Error("permission response failed", "chat_id", cmd.ChatID, keyError, err)
	}
	return responseOK, nil
}
