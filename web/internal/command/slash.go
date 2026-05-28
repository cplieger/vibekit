package command

// Slash-command REST endpoints: /api/slash/execute and /api/slash/options.
// These are request/response pairs the client awaits synchronously.

import (
	"encoding/json"
	"net/http"
	"strings"

	"vibekit/internal/api"
)

// ACP method names for slash-command extensions.
const (
	methodCommandsExecute = api.MethodCommandsExecute
	methodCommandsOptions = api.MethodCommandsOptions
	keyCommand            = "command"
)

// RegisterSlashRoutes wires the slash-command execute endpoint into mux.
// The /api/slash/options endpoint (typeahead completion) was removed
// when the slash-command popover UI was stripped.
func RegisterSlashRoutes(mux *http.ServeMux, deps Dependencies) {
	sh := &slashHandler{deps: deps}
	mux.HandleFunc("/api/slash/execute", sh.handleExecute)
}

// slashHandler holds dependencies for the slash REST endpoints.
type slashHandler struct {
	deps Dependencies
}

// POST /api/slash/execute {chat_id, command}
func (sh *slashHandler) handleExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.MethodNotAllowed(w)
		return
	}
	api.LimitBody(w, r, 64*1024)
	var req struct {
		ChatID  string `json:"chat_id"`
		Command string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.BadRequest(w, "invalid request body")
		return
	}
	if req.ChatID == "" || req.Command == "" {
		api.BadRequest(w, "chat_id and command are required")
		return
	}
	if !validChatID(api.ChatID(req.ChatID)) {
		api.BadRequest(w, api.ErrMsgInvalidChatID)
		return
	}

	b := sh.deps.GetBridge(api.ChatID(req.ChatID))
	if b == nil {
		api.Conflict(w, "no active bridge")
		return
	}

	cmd := strings.TrimPrefix(req.Command, "/")

	resp, err := b.Call(r.Context(), methodCommandsExecute, SessionParams(b, map[string]any{
		keyCommand: cmd,
	}))
	if err != nil {
		api.InternalError(w, err)
		return
	}

	if resp.Result != nil {
		api.WriteRawJSON(w, resp.Result)
		return
	}
	api.Ok(w)
}
