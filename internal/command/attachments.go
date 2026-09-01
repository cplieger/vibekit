package command

// Staged attachments: the files parked beside one chat's draft and not yet
// sent. Mirrors draft.go: no bridge call, a write that does not move the
// retention clock (ChatStore.SetAttachments), a no-op on a chat that is not
// a server record yet, and one draft_changed broadcast when something landed.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// CmdSetAttachments records the paths staged beside the chat's draft. An
// empty Paths is a legitimate value (how a sent or emptied pill row clears);
// the whole list arrives every time, so nothing needs reconciling.
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
		// A 400 rather than a silent drop: dropping one member of a list the
		// client believes it replaced wholesale would leave the two sides
		// disagreeing with nothing saying so. No UTF-8 check: encoding/json
		// already replaced any invalid byte sequence in the decoded string.
		if path == "" || len(path) > vibekit.MaxAttachmentPathBytes {
			return nil, StatusError(http.StatusBadRequest, errBadAttachmentPath)
		}
	}

	state, err := chats.SetAttachments(ctx, cmd.ChatID, p.Paths)
	if err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	broadcastComposer(ctx, bus, cmd.ChatID, state)

	// Debug and by count, matching the draft's line: a workspace path can
	// name a file the user would not want in a log.
	slog.Debug("attachments set", "chat", cmd.ChatID, "count", len(p.Paths))
	return responseWith(map[string]any{"count": len(p.Paths)}), nil
}
