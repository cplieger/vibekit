package command

// Composer drafts: the unsent text of one chat, kept so switching chat tabs
// stops bleeding a half-written message into the next conversation.
//
// Server-side rather than localStorage because the draft then follows the user
// across devices and joins the state that is already per-chat and canonical
// (model, mode, supervised, effort). The client autosaves on a 600ms debounce
// and flushes on blur, on a chat switch and on unload, so this handler is hit
// often and does as little as possible: no bridge call, no broadcast, and a
// write that does not move the chat's retention clock (see ChatStore.SetDraft).

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
func CmdSetDraft(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *vibekit.ClientCommand) { //nolint:revive // dispatcher handler signature
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p vibekit.SetDraftCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}
	if len(p.Text) > vibekit.MaxDraftBytes {
		d.RespondErr(w, http.StatusRequestEntityTooLarge, errDraftTooLong)
		return
	}
	// No UTF-8 check here, deliberately: encoding/json replaces every invalid
	// byte sequence in a string literal with U+FFFD while decoding, so a draft
	// arriving through this envelope is already valid and the check could not
	// fail. The store keeps its own (Store.SetDraft), which is the reachable one:
	// it guards the Go-level API, the same way validateChatUTF8 guards Name and
	// message content.

	if err := d.Deps().ChatStore().SetDraft(ctx, cmd.ChatID, p.Text); err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}

	// Debug, not Info: this fires every 600ms of typing, and the one thing an
	// operator would want from it (that saves are landing) is answered by the
	// chat file. Never log the text.
	slog.Debug("draft set", "chat", cmd.ChatID, "bytes", len(p.Text))
	d.Respond(w, cmd.RequestID, responseWith(map[string]any{"bytes": len(p.Text)}))
}
