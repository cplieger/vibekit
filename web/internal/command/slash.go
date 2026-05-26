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
		api.BadRequest(w, "invalid chat_id")
		return
	}

	b := sh.deps.GetBridge(api.ChatID(req.ChatID))
	if b == nil {
		api.Conflict(w, "no active bridge")
		return
	}

	cmd := strings.TrimPrefix(req.Command, "/")

	resp, err := b.Call(r.Context(), methodCommandsExecute, SessionParams(b, map[string]any{
		"command": cmd,
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

// GET /api/slash/options?chat_id=X&command=Y&partial=Z
func (sh *slashHandler) handleOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w)
		return
	}
	chatID := r.URL.Query().Get("chat_id")
	command := r.URL.Query().Get("command")
	partial := r.URL.Query().Get("partial")
	if chatID == "" || command == "" {
		api.BadRequest(w, "chat_id and command are required")
		return
	}
	if !validChatID(api.ChatID(chatID)) {
		api.BadRequest(w, "invalid chat_id")
		return
	}

	b := sh.deps.GetBridge(api.ChatID(chatID))
	if b == nil {
		api.WriteJSON(w, map[string]any{keyOptions: []any{}})
		return
	}

	cmd := strings.TrimPrefix(command, "/")

	resp, err := b.Call(r.Context(), methodCommandsOptions, SessionParams(b, map[string]any{
		"command": cmd,
		"partial": partial,
	}))
	if err != nil {
		api.WriteJSON(w, map[string]any{keyOptions: []any{}})
		return
	}

	if resp.Result != nil {
		api.WriteRawJSON(w, resp.Result)
		return
	}
	api.WriteJSON(w, map[string]any{keyOptions: []any{}})
}
