package command

// Composer drafts: the unsent text of one chat, kept so switching chat tabs
// stops bleeding a half-written message into the next conversation.
//
// Server-side rather than localStorage so the draft follows the user across
// devices. The client autosaves on a 600ms debounce, so this handler does as
// little as possible: no bridge call, one write that does not move the
// chat's retention clock (see ChatStore.SetDraft), one broadcast only when
// something changed.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// CmdSetDraft records the chat's unsent composer text. An empty Text is a
// legitimate value (how a sent or abandoned message clears). The reply
// carries the byte length rather than the text.
func CmdSetDraft(ctx context.Context, chats ChatStore, bus Broadcaster, cmd *vibekit.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p vibekit.SetDraftCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if len(p.Text) > vibekit.MaxDraftBytes {
		return nil, StatusError(http.StatusRequestEntityTooLarge, errDraftTooLong)
	}
	// No UTF-8 check here: encoding/json already replaced any invalid byte
	// sequence in the decoded string. The store keeps its own, which guards
	// the Go-level API.

	state, err := chats.SetDraft(ctx, cmd.ChatID, p.Text)
	if err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	broadcastComposer(ctx, bus, cmd.ChatID, state)

	// Debug, not Info: fires every 600ms of typing. Never log the text.
	slog.Debug("draft set", "chat", cmd.ChatID, "bytes", len(p.Text))
	return responseWith(map[string]any{"bytes": len(p.Text)}), nil
}

// broadcastComposer publishes draft_changed for a write that landed, and
// does nothing for one that did not. A nil state means no record, or the
// same value already stored.
func broadcastComposer(ctx context.Context, bus Broadcaster, chatID vibekit.ChatID, state *vibekit.ComposerState) {
	if state == nil {
		return
	}
	bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventDraftChanged, chatID, vibekit.DraftChangedPayload{
		Text:        state.Text,
		Attachments: state.Attachments,
	}))
}
