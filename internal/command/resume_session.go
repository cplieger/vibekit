package command

// Resuming a KAS session vibekit has no chat record for.
//
// The previous-session picker lists what KAS stored, including sessions
// vibekit never had a chat for — a session started from the TUI, or one
// whose chat the user deleted while retention kept the session. To open
// one, vibekit needs a chat record to hang it on, since a chat is what its
// UI, retention and per-chat bridge are keyed by.
//
// Resume creates a chat already bound to that session id; the next
// OpenBridge sees a stored ACPSessionID and takes the session/load path,
// whose replay turns into the transcript. vibekit copies no messages.

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// CmdResumeSession creates a chat bound to an existing KAS session so the
// stored conversation can be opened, and returns the chat plus its tab.
// The id is minted here when the envelope carries none.
func CmdResumeSession(ctx context.Context, mem *Membership, cmd *vibekit.ClientCommand) (any, error) {
	var p vibekit.ResumeSessionCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// The session id reaches a filesystem path inside KAS and vibekit's own
	// reaper keep-list, so it is validated on the same pattern as a chat id
	// rather than trusted from the client.
	if !ids.ValidSessionID(p.SessionID) || !ValidIdent(p.OpID) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	name := cmp.Or(p.Name, vibekit.DefaultChatName)
	if len(name) > vibekit.MaxChatNameBytes {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}

	// The op ledger matters more here than for a bare create: minting per
	// attempt would leave two chats bound to one KAS session.
	opened, err := mem.CreateChatAndOpen(ctx, ChatCreate{
		OpID:   p.OpID,
		ChatID: cmd.ChatID,
		Init: func(c *vibekit.Chat) {
			// Init runs only when the record does not exist, which is
			// what refuses to rebind an existing chat: pointing a live
			// chat at another session would strand its own session.
			c.Name = name
			// RecordSession, not assignment: the sanctioned writer of
			// this field, keeping the reaper's keep-list chain invariant.
			c.RecordSession(p.SessionID)
		},
	})
	if err != nil {
		return nil, err
	}
	slog.Info("session resumed into a new chat",
		"chat_id", opened.Chat.ID, "acp_session", p.SessionID, "tab", opened.Subject.ID)
	return openedResponse(&opened, nil), nil
}
