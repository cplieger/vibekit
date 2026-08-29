package command

// Chat CRUD commands: create, delete, cancel, and permission forwarding.

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// cancelGrace is how long a cooperative session/cancel gets to be reflected
// in a turn end before vibekit unblocks the turn itself. Copied from
// KiroCrew's `_CANCEL_GRACE_SECS` (src/kiro_crew/acp/{client,session_handle}.py)
// rather than guessed.
const cancelGrace = 10 * time.Second

// CmdCreateChat creates a new chat, opens its tab, and returns both.
//
// It MINTS the chat id when the envelope carries none, which is the ordinary
// path and the point of this command. A client-minted id is still accepted: the
// prompt path auto-creates under an id it was handed, so refusing one here would
// mean two rules for the same question, and there is nothing to gain from a
// refusal — validChatID already gates the envelope for every command.
//
// The RESPONSE carries the chat AND its tab, which is what makes server minting
// workable at all: the caller has to be able to address the thing it just created
// (open its tab, set its mode, send its first prompt), and before this it could
// only do that by having invented the id itself.
//
// The record and the tab are ONE operation, ordered by the coordinator — chat
// first, tab second, capacity reserved before either mints. This handler owns the
// payload's shape and nothing else.
func CmdCreateChat(ctx context.Context, mem *Membership, cmd *vibekit.ClientCommand) (any, error) {
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
	if !ValidIdent(p.Model) || !ValidIdent(p.OpID) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	opened, err := mem.CreateChatAndOpen(ctx, ChatCreate{
		OpID:   p.OpID,
		ChatID: cmd.ChatID,
		Init: func(c *vibekit.Chat) {
			c.Name = name
			c.Model = p.Model
		},
	})
	if err != nil {
		return nil, err
	}
	return openedResponse(&opened, nil), nil
}

// openedResponse is the shape every create-and-open answers with: the chat, the
// tab that was opened for it, and the version that open produced, plus whatever
// the command adds of its own.
//
// One helper so "a create returns its chat AND its subject" has one spelling
// across the three creating commands rather than three. The chat is its header,
// as every other chat response is — the full record carries the whole message
// history.
//
// The subject is omitted when no tab store is wired (see Membership), because a
// zero-valued subject on the wire would name a tab with an empty id.
//
// No size hint on the map, deliberately: the body holds three to five entries, so
// the hint bought nothing measurable, and spelling it as len(extra)+3 is an
// addition on a length that go/allocation-size-overflow reads as a potential
// overflow. Do not put the arithmetic back to save one growth.
func openedResponse(opened *ChatOpened, extra map[string]any) any {
	body := make(map[string]any)
	maps.Copy(body, extra)
	body["chat"] = opened.Chat.Header()
	body[keyVersion] = opened.Version
	if opened.Subject.ID != "" {
		body["subject"] = opened.Subject
	}
	return responseWith(body)
}

// CmdDeleteChat removes a chat: tear down its side effects, remove the record,
// then close its tabs.
//
// The ORDER is the coordinator's and it is the whole contract: the record leads,
// so an open_tab that slips in after it finds no chat and is refused. Reversed —
// tabs closed first — the window between the two writes is one where an open
// succeeds and its tab outlives the chat until the next restart.
//
// It used to cascade through DeleteFamily, whose whole subject was rewind
// CHILDREN — a chat could own other chats, so deletion needed an ordering
// contract (children first, so no crash window left a child pointing at a
// deleted parent) and a truthful partial-failure response (`failed_children`).
// A rewind reverts the chat it is in now, so no chat owns another and there is
// nothing to order or to half-fail. The transition, its ordering guarantee and
// the `failed_children` response are all gone rather than kept as a
// single-element loop.
func CmdDeleteChat(ctx context.Context, mem *Membership, cmd *vibekit.ClientCommand) (any, error) {
	var p vibekit.CloseTabCommand
	if len(cmd.Payload) > 0 {
		// The payload is optional and carries only an op_id, so a body this cannot
		// read is ignored rather than refused: delete_chat's subject is the
		// envelope's chat id, and failing the delete over a correlation id would
		// leave the chat nobody wants.
		_ = json.Unmarshal(cmd.Payload, &p)
	}
	if !ValidIdent(p.OpID) {
		p.OpID = ""
	}
	if err := mem.DeleteChatAndCloseTabs(ctx, cmd.ChatID, p.OpID); err != nil {
		return nil, err
	}
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

// closeChatTeardown is the tab-close teardown: the user closed the chat's tab,
// and the gesture means "kill all of it" (user decision) — the turn, the runs,
// the process. The chat RECORD is untouched; under retention a closed chat is
// just a chat without a tab, and reopening it session/loads everything back.
//
// It is INTERNAL MACHINERY now, not a command. `close_chat` left the client
// surface when close_tab arrived: the × on a chat tab is one gesture, and two
// commands meaning it would be two things to keep in step — a client that sent
// only close_chat would tear the bridge down and leave the tab, and one that sent
// only close_tab would leave the process. The coordinator calls this for every
// chat tab it closes, so there is one door.
//
// Ordering is the contract. The turn is cancelled FIRST (session/cancel, the
// graceful stop — the model stops rather than being severed mid-write), then
// the chat's runs (durable state; killing the process would only pause them,
// and a paused run later revives and edits files nobody is watching), then the
// process teardown, which also flushes the in-flight buffer and kills the
// chat's agent terminals.
func closeChatTeardown(ctx context.Context, bridges BridgeAccess, perms PendingPermAccess, teardown ChatTeardown, chatID vibekit.ChatID) {
	perms.ClearPendingPermsForChat(chatID)
	if sb := bridges.Bridge(chatID); sb != nil {
		if err := sb.Notify(ctx, vibekit.MethodCancel, SessionParams(sb)); err != nil {
			slog.Warn("close: turn cancel failed", "chat_id", chatID, keyError, err)
		}
	}
	teardown.CloseChatState(ctx, chatID)
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
	if !perms.TakePendingPerm(cmd.ChatID, p.RequestID, vibekit.SettledByUser) {
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
