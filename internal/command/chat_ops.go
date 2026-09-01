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
// in a turn end before vibekit unblocks the turn itself. Adopted from
// KiroCrew's `_CANCEL_GRACE_SECS`.
const cancelGrace = 10 * time.Second

// CmdCreateChat creates a new chat, opens its tab, and returns both.
//
// Mints the chat id when the envelope carries none (the ordinary path); a
// client-minted id is still accepted since validChatID already gates it.
// The response carries both the chat and its tab, so the caller can address
// what it just created without having invented the id itself.
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

// openedResponse is the shape every create-and-open answers with: the chat
// header, the tab opened for it, and the version that open produced, plus
// whatever the command adds. Subject is omitted when no tab store is wired
// (a zero-valued subject would name a tab with an empty id).
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

// CmdDeleteChat removes a chat: tear down its side effects, remove the
// record, then close its tabs. The order is the coordinator's — the record
// leads, so an open_tab that slips in after finds no chat and is refused.
func CmdDeleteChat(ctx context.Context, mem *Membership, cmd *vibekit.ClientCommand) (any, error) {
	var p vibekit.CloseTabCommand
	if len(cmd.Payload) > 0 {
		// Optional and carries only an op_id; ignored rather than refused if
		// unreadable, since the subject is the envelope's chat id.
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
	// Only pending permissions are cleared; KAS owns the write gate and
	// cancelling a turn already reverts its own approval.
	perms.ClearPendingPermsForChat(cmd.ChatID)

	// Stopping the model does not stop the processes its turn already
	// spawned. Scoped to the turn's own terminals only.
	terms.KillForTurn(cmd.ChatID)

	sb := bridges.Bridge(cmd.ChatID)
	if sb == nil {
		return responseOK, nil
	}
	if err := sb.Notify(ctx, vibekit.MethodCancel, SessionParams(sb)); err != nil {
		slog.Error("cancel failed", "chat_id", cmd.ChatID, keyError, err)
	}
	// session/cancel is a notification, so nothing acks it directly: the turn
	// ends only when KAS answers the pending session/prompt. If it never
	// does, the grace budget cancels the prompt's context so Call returns and
	// the ordinary prompt-failure path finalizes the turn. 10s is KiroCrew's
	// `_CANCEL_GRACE_SECS`.
	if !sb.ArmCancelGrace(sb.PromptGeneration(), cancelGrace) {
		slog.Debug("cancel: no in-flight prompt to arm a grace budget against", "chat_id", cmd.ChatID)
	}
	return responseOK, nil
}

// closeChatTeardown is the tab-close teardown: closing a chat's tab means
// kill all of it — the turn, the runs, the process. The chat RECORD is
// untouched; under retention a closed chat is just a chat without a tab.
//
// Internal machinery now, not a command: close_chat left the client surface
// when close_tab arrived, since the × on a chat tab is one gesture and two
// commands meaning it would be two things to keep in step.
//
// Ordering is the contract: the turn is cancelled first (graceful stop), then
// the chat's runs (durable state; killing the process would only pause them),
// then the process teardown, which flushes the in-flight buffer and kills the
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

// deleteChatTeardown is closeChatTeardown's delete grade, for a chat the
// retention-off close escalation has already erased: same graceful cancel
// first, then the delete-grade teardown driven from the session chain
// captured before the record went (a record-reading teardown would no-op on
// a deleted chat). Mirrors closeChatTeardown so the two grades cannot drift.
func deleteChatTeardown(ctx context.Context, bridges BridgeAccess, perms PendingPermAccess, teardown ChatTeardown, chatID vibekit.ChatID, sessionChain []string) {
	perms.ClearPendingPermsForChat(chatID)
	if sb := bridges.Bridge(chatID); sb != nil {
		if err := sb.Notify(ctx, vibekit.MethodCancel, SessionParams(sb)); err != nil {
			slog.Warn("close: turn cancel failed", "chat_id", chatID, keyError, err)
		}
	}
	teardown.DeleteChatStateByChain(ctx, chatID, sessionChain)
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
	// Claim the request before answering it: two tabs on one chat can both
	// see the card, and kiro-cli silently discards the second answer for a
	// request id already resolved.
	if !perms.TakePendingPerm(cmd.ChatID, p.RequestID, vibekit.SettledByUser) {
		return nil, StatusError(http.StatusConflict, errAlreadyAnswered)
	}
	// A turn approval answers on the same reply, with per-file decisions in
	// _meta; built through one helper so the omitted-id-means-reject rule
	// lives in one place.
	outcome := vibekit.PermissionOutcomeWithFileDecisions(p.OptionID, p.FileDecisions)
	if err := sb.Respond(ctx, p.RequestID, outcome, nil); err != nil {
		slog.Error("permission response failed", "chat_id", cmd.ChatID, keyError, err)
	}
	return responseOK, nil
}
