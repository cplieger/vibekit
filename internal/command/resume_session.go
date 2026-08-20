package command

// Resuming a KAS session vibekit has no chat record for.
//
// The previous-session picker (GET /api/sessions) lists what KAS stored,
// including sessions vibekit never had a chat for — a session started from the
// TUI, or one whose chat the user deleted while retention kept the session. To
// open one, vibekit needs a chat record to hang it on, because a chat is what
// its UI, retention and per-chat bridge are keyed by.
//
// So resume is: create a chat already BOUND to that session id. Nothing else is
// needed. The next OpenBridge sees a stored ACPSessionID and takes the
// session/load path, whose replay the projection turns into the transcript
// (agent/load_projection.go). That is the whole import — vibekit copies no
// messages, because it no longer owns them.

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
// stored conversation can be opened.
func CmdResumeSession(ctx context.Context, chats ChatStore, cmd *vibekit.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p vibekit.ResumeSessionCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// The session id reaches a filesystem path inside KAS and vibekit's own
	// reaper keep-list, so it is validated on the same pattern as a chat id
	// rather than trusted from the client.
	if !ids.ValidSessionID(p.SessionID) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	name := cmp.Or(p.Name, vibekit.DefaultChatName)
	if len(name) > vibekit.MaxChatNameBytes {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}

	err := chats.Mutate(ctx, cmd.ChatID, func(c *vibekit.Chat, exists bool) bool {
		// Refuse to rebind an existing chat. Pointing a live chat at another
		// session would strand its own session (its transcript still on disk,
		// no longer referenced, so the reaper sweeps it) and hand the user a
		// chat whose history silently changed.
		if exists {
			return false
		}
		c.Name = name
		// RecordSession rather than assignment: it is the only sanctioned way
		// to set this field, and it keeps the chain invariant the reaper's
		// keep-list depends on (see Chat.RecordSession).
		c.RecordSession(p.SessionID)
		return true
	})
	if err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	slog.Info("session resumed into a new chat",
		"chat_id", cmd.ChatID, "acp_session", p.SessionID)
	return responseOK, nil
}
