package command

// Staged attachments: the files parked beside one chat's draft and not yet sent.
//
// The DRAFT'S TWIN, and that is why this file mirrors draft.go line for line
// rather than doing anything of its own. The client holds both in maps keyed by
// chat that parallel each other exactly, saves both on the same 600ms debounce,
// and until this command existed only one of the two survived a reload: the
// half-written sentence came back and the three files it described did not.
//
// So the same four properties hold here: no bridge call, a write that does not
// move the retention clock (ChatStore.SetAttachments), a NO-OP on a chat that is
// not a server record yet, and one draft_changed broadcast when something landed.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// CmdSetAttachments records the paths staged beside the chat's draft.
//
// An empty Paths is a legitimate value, not a missing field: it is how a sent or
// emptied pill row clears. The whole list arrives every time, so there is nothing
// to reconcile — the client's row is authoritative and this stores what it shows.
//
// The reply carries the count rather than the paths, for the reason set_draft
// replies with a byte length: the request already carried them, and the row that
// sent them is the one thing on the network that already knows.
func CmdSetAttachments(ctx context.Context, chats ChatStore, bus Broadcaster, cmd *vibekit.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p vibekit.SetAttachmentsCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if len(p.Paths) > vibekit.MaxAttachments {
		return nil, StatusError(http.StatusRequestEntityTooLarge, errTooManyAttachments)
	}
	for _, path := range p.Paths {
		// A 400 rather than a silent drop: an empty or oversized entry means the
		// client sent a row it could not have rendered, and dropping one member of
		// a list the sender believes it replaced wholesale leaves the two sides
		// disagreeing about what is staged with nothing saying so.
		//
		// No UTF-8 check, for set_draft's reason: encoding/json has already
		// replaced every invalid byte sequence in a decoded string literal with
		// U+FFFD, so the check could not fail here. The store keeps its own, which
		// guards the Go-level API.
		if path == "" || len(path) > vibekit.MaxAttachmentPathBytes {
			return nil, StatusError(http.StatusBadRequest, errBadAttachmentPath)
		}
	}

	state, err := chats.SetAttachments(ctx, cmd.ChatID, p.Paths)
	if err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	broadcastComposer(ctx, bus, cmd.ChatID, state)

	// Debug and by COUNT, matching the draft's line: this rides the same debounce,
	// and a workspace path can name a file the user would not want in a log.
	slog.Debug("attachments set", "chat", cmd.ChatID, "count", len(p.Paths))
	return responseWith(map[string]any{"count": len(p.Paths)}), nil
}
