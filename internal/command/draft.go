package command

// Composer drafts: the unsent text of one chat, kept so switching chat tabs
// stops bleeding a half-written message into the next conversation.
//
// Server-side rather than localStorage because the draft then follows the user
// across devices and joins the state that is already per-chat and canonical
// (model, mode, supervised, effort). The client autosaves on a 600ms debounce
// and flushes on blur, on a chat switch and on unload, so this handler is hit
// often and does as little as possible: no bridge call, one write that does not
// move the chat's retention clock (see ChatStore.SetDraft), and one broadcast
// only when something actually changed.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// CmdSetDraft records the chat's unsent composer text.
//
// An empty Text is a legitimate value, not a missing field: it is how a sent or
// abandoned message is cleared. The reply carries the byte length rather than
// the text, because echoing a draft back would put the user's unsent words in
// the response body of a request that already carried them.
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
	// No UTF-8 check here, deliberately: encoding/json replaces every invalid
	// byte sequence in a string literal with U+FFFD while decoding, so a draft
	// arriving through this envelope is already valid and the check could not
	// fail. The store keeps its own (Store.SetDraft), which is the reachable one:
	// it guards the Go-level API, the same way validateChatUTF8 guards Name and
	// message content.

	state, err := chats.SetDraft(ctx, cmd.ChatID, p.Text)
	if err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	broadcastComposer(ctx, bus, cmd.ChatID, state)

	// Debug, not Info: this fires every 600ms of typing, and the one thing an
	// operator would want from it (that saves are landing) is answered by the
	// chat file. Never log the text.
	slog.Debug("draft set", "chat", cmd.ChatID, "bytes", len(p.Text))
	return responseWith(map[string]any{"bytes": len(p.Text)}), nil
}

// broadcastComposer publishes draft_changed for a write that landed, and does
// nothing for one that did not.
//
// A nil state is the store saying "no record, or the same value already stored",
// and both are silent for the same reason CmdSetMode broadcasts only when its
// `changed` flag is set: a frame carrying a value nobody changed is one every
// connected client parses and re-applies for nothing, and this path fires on a
// typing debounce, so the no-change case is the common one (a blur flush right
// after a save, an unload flush behind it).
func broadcastComposer(ctx context.Context, bus Broadcaster, chatID vibekit.ChatID, state *vibekit.ComposerState) {
	if state == nil {
		return
	}
	bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventDraftChanged, chatID, vibekit.DraftChangedPayload{
		Text:        state.Text,
		Attachments: state.Attachments,
	}))
}
