package hub

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp"
)

// registerCommandHandlers populates the dispatcher with the concrete
// dispatch table. Called once from NewHub.
func (h *Hub) registerCommandHandlers() {
	// Register all standard handlers from the command package. Every role is
	// the Hub under a narrower interface; which handler receives which is
	// command.RegisterDefaults' table.
	command.RegisterDefaults(h.dispatcher, &command.Roles{
		Bridges:   h,
		Chats:     h,
		Perms:     h,
		Terminals: h,
		// A value, not h: the two paths are process constants, so nothing is
		// substituted and the hub is not in the middle of reading a string.
		Workspace:   command.Workspace{Dir: h.lifecycle.workDir, ConfigDir: h.lifecycle.configDir},
		Lifecycle:   h,
		MCP:         h,
		TurnOutcome: h,
	})

	// Register handlers that remain on Hub (complex internal coupling).
	h.dispatcher.Register(vibekit.CmdSwitchModel, h.cmdSwitchModel)
}

// respond writes a JSON body and caches it for request_id idempotency.
func (h *Hub) respond(w http.ResponseWriter, reqID string, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		slog.Error("respond marshal", "error", err)
		httpreply.InternalError(w, err)
		return
	}
	h.recordDedup(reqID, data)
	httpreply.WriteRawJSON(w, data)
}

func (h *Hub) respondErr(w http.ResponseWriter, code int, err error) {
	webhttp.WriteJSONStatus(w, code, httpreply.ErrorJSON(err.Error()))
}

// requireChatID validates that cmd.ChatID is non-empty and writes a
// 400 response if not.
func (h *Hub) requireChatID(w http.ResponseWriter, cmd *vibekit.ClientCommand) bool {
	if cmd.ChatID == "" {
		h.respondErr(w, http.StatusBadRequest, command.ErrMissingChatID)
		return false
	}
	return true
}
