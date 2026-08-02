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
// needed. The next GetOrCreateBridge sees a stored ACPSessionID and takes the
// session/load path, whose replay the projection turns into the transcript
// (hub/load_projection.go). That is the whole import — vibekit copies no
// messages, because it no longer owns them.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/api"
)

// CmdResumeSession creates a chat bound to an existing KAS session so the
// stored conversation can be opened.
//
//nolint:revive // context-as-argument: dispatcher handler signature
func CmdResumeSession(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) {
	if !d.RequireChatID(w, cmd) {
		return
	}
	var p api.ResumeSessionCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}
	// The session id reaches a filesystem path inside KAS and vibekit's own
	// reaper keep-list, so it is validated on the same pattern as a chat id
	// rather than trusted from the client.
	if !api.ValidSessionID(p.SessionID) {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}
	name := p.Name
	if name == "" {
		name = api.DefaultChatName
	}
	if len(name) > api.MaxChatNameBytes {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}

	err := d.Chat().ChatStore().Mutate(ctx, cmd.ChatID, func(c *api.Chat, exists bool) bool {
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
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	slog.Info("session resumed into a new chat",
		"chat_id", cmd.ChatID, "acp_session", p.SessionID)
	d.RespondOK(w, cmd.RequestID)
}
