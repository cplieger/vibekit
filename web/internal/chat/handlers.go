package chat

import (
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"vibekit/internal/api"
)

// errMsgInvalidChatID is the HTTP error message for invalid chat IDs.
// Centralised so all handler sites use the same wording.
const errMsgInvalidChatID = "invalid chat id"

// RegisterRoutes wires GET /api/chats (list), GET /api/chats/{id}
// (one chat with paginated messages), and the archived-chat routes.
// Delegates to Router for structural separation of HTTP concerns.
func (s *Store) RegisterRoutes(mux *http.ServeMux) {
	rt := NewRouter(s)
	rt.Register(mux)
}

// handleList returns all chat headers.
func (rt *Router) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w)
		return
	}
	headers := rt.store.List(r.Context())
	api.WriteJSON(w, map[string]any{"chats": headers})
}

// handleOne serves GET /api/chats/{id}?before=<ts>&limit=<n>.
func (rt *Router) handleOne(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/chats/")
	if rest == "" || strings.HasPrefix(rest, "/") {
		api.BadRequest(w, errMsgInvalidChatID)
		return
	}
	if id, sub, ok := strings.Cut(rest, "/"); ok {
		cid := api.ChatID(id)
		switch sub {
		case "plan-draft":
			rt.handlePlanDraft(w, r, cid)
		case "export":
			rt.handleExport(w, r, cid)
		case "archive":
			rt.handleArchive(w, r, cid)
		default:
			api.NotFound(w, "unknown chat sub-resource")
		}
		return
	}
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w)
		return
	}
	id := rest
	if !chatIDPattern(api.ChatID(id)) {
		api.BadRequest(w, errMsgInvalidChatID)
		return
	}
	c, ok := rt.store.Get(r.Context(), api.ChatID(id))
	if !ok {
		api.NotFound(w, "chat not found")
		return
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	var before int64
	if v := r.URL.Query().Get("before"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			before = n
		}
	}

	msgs := c.Messages
	end := len(msgs)
	if before > 0 {
		end = sort.Search(len(msgs), func(i int) bool { return msgs[i].Ts >= before })
	}
	start := max(end-limit, 0)
	window := make([]api.Message, end-start)
	copy(window, msgs[start:end])
	hasMore := start > 0

	api.WriteJSON(w, map[string]any{
		"chat":     rt.store.header(r.Context(), c),
		"messages": window,
		"has_more": hasMore,
	})
}

// handleExport returns the full chat JSON as a downloadable file.
func (rt *Router) handleExport(w http.ResponseWriter, r *http.Request, chatID api.ChatID) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w)
		return
	}
	if !chatIDPattern(chatID) {
		api.BadRequest(w, errMsgInvalidChatID)
		return
	}
	c, ok := rt.store.Get(r.Context(), chatID)
	if !ok {
		api.NotFound(w, "chat not found")
		return
	}
	filename := safeExportName(c.Name, string(chatID))
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	api.WriteJSON(w, c)
}

// safeExportName builds a filesystem-safe export filename.
func safeExportName(raw, fallback string) string {
	if raw == "" {
		return fallback + chatFileSuffix
	}
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		switch {
		case r < 0x20 || r == 0x7f:
			b.WriteByte('_')
		case r == '"', r == '\\', r == '/', r == ':', r == '*',
			r == '?', r == '<', r == '>', r == '|':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	name := strings.TrimSpace(b.String())
	if name == "" {
		name = fallback
	}
	if len(name) > 80 {
		name = name[:80]
	}
	return name + chatFileSuffix
}

// handleArchive moves a chat to the archive directory.
func (rt *Router) handleArchive(w http.ResponseWriter, r *http.Request, chatID api.ChatID) {
	if r.Method != http.MethodPost {
		api.MethodNotAllowed(w)
		return
	}
	if !chatIDPattern(chatID) {
		api.BadRequest(w, errMsgInvalidChatID)
		return
	}
	if err := rt.store.Archive(r.Context(), chatID); err != nil {
		slog.Error("chat archive failed", "chat_id", chatID, "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			map[string]string{api.JSONKeyError: "archive failed"})
		return
	}
	api.Ok(w)
}

// handlePlanDraft dispatches GET/PUT/DELETE for the plan-draft sub-resource.
func (rt *Router) handlePlanDraft(w http.ResponseWriter, r *http.Request, chatID api.ChatID) {
	if !chatIDPattern(chatID) {
		api.BadRequest(w, errMsgInvalidChatID)
		return
	}
	switch r.Method {
	case http.MethodGet:
		rt.getPlanDraft(w, r, chatID)
	case http.MethodPut:
		rt.putPlanDraft(w, r, chatID)
	case http.MethodDelete:
		rt.deletePlanDraftHTTP(w, r, chatID)
	default:
		api.MethodNotAllowed(w)
	}
}

// getPlanDraft serves GET for the plan-draft sub-resource.
func (rt *Router) getPlanDraft(w http.ResponseWriter, r *http.Request, chatID api.ChatID) {
	content, err := rt.store.GetPlanDraft(r.Context(), chatID)
	if err != nil {
		slog.Error("chat plan_draft: read failed", "chat_id", chatID, "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			map[string]string{api.JSONKeyError: "read failed"})
		return
	}
	api.WriteJSON(w, map[string]string{"content": content})
}

// putPlanDraft serves PUT for the plan-draft sub-resource.
func (rt *Router) putPlanDraft(w http.ResponseWriter, r *http.Request, chatID api.ChatID) {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		slog.Warn("chat plan_draft: unexpected content-type",
			"chat_id", chatID, "content_type", ct)
		api.BadRequest(w, "expected application/json")
		return
	}
	api.LimitBody(w, r, maxPlanDraftBytes+4096) // + json envelope overhead
	var body struct {
		Content string `json:"content"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("chat plan_draft: body too large",
				"chat_id", chatID, "limit", maxPlanDraftBytes+4096, "error", maxErr)
			api.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				map[string]string{api.JSONKeyError: "request body too large"})
			return
		}
		api.BadRequest(w, "invalid json")
		return
	}
	if dec.More() {
		api.BadRequest(w, "unexpected trailing data")
		return
	}
	err := rt.store.SetPlanDraft(r.Context(), chatID, body.Content)
	if err != nil {
		var ce *StoreError
		if errors.As(err, &ce) {
			writeChatErr(w, err)
		} else {
			slog.Error("chat plan_draft: save failed", "chat_id", chatID, "error", err)
			api.WriteJSONStatus(w, http.StatusInternalServerError,
				map[string]string{api.JSONKeyError: "save failed"})
		}
		return
	}
	slog.Info("chat plan_draft: saved", "chat_id", chatID, "size_bytes", len(body.Content))
	api.Ok(w)
}

// deletePlanDraftHTTP serves DELETE for the plan-draft sub-resource.
func (rt *Router) deletePlanDraftHTTP(w http.ResponseWriter, r *http.Request, chatID api.ChatID) {
	if err := rt.store.DeletePlanDraft(r.Context(), chatID); err != nil {
		slog.Error("chat plan_draft: delete failed", "chat_id", chatID, "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			map[string]string{api.JSONKeyError: "delete failed"})
		return
	}
	slog.Debug("chat plan_draft: delete http", "chat_id", chatID)
	api.Ok(w)
}

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
			api.BadRequest(w, errMsgInvalidChatID)
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
		api.BadRequest(w, errMsgInvalidChatID)
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
