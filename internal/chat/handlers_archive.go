package chat

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"vibekit/internal/api"
)

// handleArchivedChats handles GET (list) and POST (restore) for archived chats.
func (rt *Router) handleArchivedChats(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		headers := rt.store.ListArchived(r.Context())
		if headers == nil {
			headers = []api.ChatHeader{}
		}
		api.WriteJSON(w, map[string]any{"chats": headers})
	case http.MethodPost:
		api.LimitBody(w, r, api.MaxJSONBody)
		var body struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
			api.BadRequest(w, "id is required")
			return
		}
		if !chatIDPattern(api.ChatID(body.ID)) {
			api.BadRequest(w, api.ErrMsgInvalidChatID)
			return
		}
		if err := rt.store.RestoreArchived(r.Context(), api.ChatID(body.ID)); err != nil {
			writeChatErr(w, err)
			return
		}
		api.Ok(w)
	default:
		api.MethodNotAllowed(w)
	}
}

// handleArchivedChatAction handles DELETE /api/chats/archived/{id} to
// permanently remove a single archived chat.
func (rt *Router) handleArchivedChatAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		api.MethodNotAllowed(w)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/chats/archived/")
	if id == "" {
		api.BadRequest(w, "missing chat id")
		return
	}
	if !chatIDPattern(api.ChatID(id)) {
		api.BadRequest(w, api.ErrMsgInvalidChatID)
		return
	}
	if err := rt.store.DeleteArchived(r.Context(), api.ChatID(id)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			api.NotFound(w, "archived chat not found")
		} else {
			api.InternalError(w, err)
		}
		return
	}
	api.Ok(w)
}
