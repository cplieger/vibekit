package hub

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"vibekit/internal/api"
	"vibekit/internal/command"
)

// Static command errors used by handlers that remain in the hub package.
var (
	errMissingChatID  = errors.New("missing chat_id")
	errInvalidPayload = errors.New("invalid payload")
	errChatNotFound   = errors.New("chat not found")
)

// registerCommandHandlers populates the dispatcher with the concrete
// dispatch table. Called once from NewHub.
func (h *Hub) registerCommandHandlers() {
	// Register all standard handlers from the command package.
	command.RegisterDefaults(h.dispatcher)

	// Register handlers that remain on Hub (complex internal coupling).
	h.dispatcher.Register(api.CmdSwitchModel, h.cmdSwitchModel)
}

// respond writes a JSON body and caches it for request_id idempotency.
func (h *Hub) respond(w http.ResponseWriter, reqID string, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		slog.Error("respond marshal", "error", err)
		api.InternalError(w, err)
		return
	}
	h.recordDedup(reqID, data)
	api.WriteRawJSON(w, data)
}

func (h *Hub) respondErr(w http.ResponseWriter, code int, err error) {
	api.WriteJSONStatus(w, code, map[string]string{"error": err.Error()})
}

// requireChatID validates that cmd.ChatID is non-empty and writes a
// 400 response if not.
func (h *Hub) requireChatID(w http.ResponseWriter, cmd *api.ClientCommand) bool {
	if cmd.ChatID == "" {
		h.respondErr(w, http.StatusBadRequest, errMissingChatID)
		return false
	}
	return true
}
