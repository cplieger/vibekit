package chat

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/webhttp"
)

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

// handleOne serves GET /api/chats/{id}?before=<ts>&limit=<n> and routes
// /api/chats/{id}/<sub-resource> requests to their handlers.
func (rt *Router) handleOne(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/chats/")
	if rest == "" || strings.HasPrefix(rest, "/") {
		api.BadRequest(w, api.ErrMsgInvalidChatID)
		return
	}
	if id, sub, ok := strings.Cut(rest, "/"); ok {
		rt.routeChatSubResource(w, r, api.ChatID(id), sub)
		return
	}
	rt.serveChatMessages(w, r, rest)
}

// routeChatSubResource dispatches /api/chats/{id}/<sub> to the handler for
// the addressed sub-resource.
func (rt *Router) routeChatSubResource(w http.ResponseWriter, r *http.Request, cid api.ChatID, sub string) {
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
}

// serveChatMessages serves the paginated single-chat GET for a bare
// /api/chats/{id} request.
func (rt *Router) serveChatMessages(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w)
		return
	}
	if !chatIDPattern(api.ChatID(id)) {
		api.BadRequest(w, api.ErrMsgInvalidChatID)
		return
	}
	c, ok := rt.store.Get(r.Context(), api.ChatID(id))
	if !ok {
		api.NotFound(w, errMsgChatNotFound)
		return
	}

	limit := parseLimitParam(r)
	before := parseBeforeParam(r)

	msgs := c.Messages
	end := len(msgs)
	if before > 0 {
		end = sort.Search(len(msgs), func(i int) bool { return msgs[i].Ts >= before })
	}
	start := max(end-limit, 0)
	window := make([]api.Message, end-start)
	copy(window, msgs[start:end])

	api.WriteJSON(w, map[string]any{
		"chat":     rt.store.header(r.Context(), c),
		"messages": window,
		"has_more": start > 0,
	})
}

// parseLimitParam returns the validated ?limit= page size, defaulting to 50
// and honouring values in the inclusive 1..500 range; anything else (absent,
// non-numeric, <=0, >500) falls back to the default.
func parseLimitParam(r *http.Request) int {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	return limit
}

// parseBeforeParam returns the validated ?before= cursor (a millisecond
// timestamp), or 0 when absent or unparseable.
func parseBeforeParam(r *http.Request) int64 {
	var before int64
	if v := r.URL.Query().Get("before"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			before = n
		}
	}
	return before
}

// exportFormat is the requested export serialization.
type exportFormat int

const (
	exportFormatMarkdown exportFormat = iota
	exportFormatJSON
)

// handleExport serves GET /api/chats/{id}/export?format=md|json, rendering
// the persisted chat to a downloadable Markdown transcript (the default) or
// the raw chat JSON. The chat store is the source of truth, so no live ACP
// bridge is involved — any chat exports, including archived ones, which are
// served by falling back to the archive directory (the History view).
func (rt *Router) handleExport(w http.ResponseWriter, r *http.Request, chatID api.ChatID) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w)
		return
	}
	if !chatIDPattern(chatID) {
		api.BadRequest(w, api.ErrMsgInvalidChatID)
		return
	}
	format, ok := parseExportFormat(r.URL.Query().Get("format"))
	if !ok {
		api.BadRequest(w, "unsupported export format (use md or json)")
		return
	}
	c, found := rt.loadForExport(r.Context(), chatID)
	if !found {
		api.NotFound(w, errMsgChatNotFound)
		return
	}
	if format == exportFormatJSON {
		w.Header().Set("Content-Disposition",
			dispositionAttachment(exportFilename(c.Name, string(chatID), ".json")))
		api.WriteJSON(w, c)
		return
	}
	w.Header().Set("Content-Disposition",
		dispositionAttachment(exportFilename(c.Name, string(chatID), ".md")))
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if _, err := io.WriteString(w, renderChatMarkdown(c)); err != nil {
		slog.Debug("chat export: markdown write failed", "chat_id", chatID, "error", err)
	}
}

// parseExportFormat maps the ?format= value to an exportFormat. Absent or
// md/markdown selects Markdown (the default); json selects raw JSON;
// anything else is rejected so a typo fails loudly rather than silently
// returning the wrong format.
func parseExportFormat(v string) (exportFormat, bool) {
	switch strings.ToLower(v) {
	case "", "md", "markdown":
		return exportFormatMarkdown, true
	case "json":
		return exportFormatJSON, true
	default:
		return exportFormatMarkdown, false
	}
}

// loadForExport returns the chat for chatID, checking the active store first
// and falling back to the archive directory so archived chats (surfaced in
// the History view) export too. No live bridge is required either way.
func (rt *Router) loadForExport(ctx context.Context, chatID api.ChatID) (*api.Chat, bool) {
	if c, ok := rt.store.Get(ctx, chatID); ok {
		return c, true
	}
	if c, err := rt.store.LoadArchived(ctx, chatID); err == nil {
		return c, true
	}
	return nil, false
}

// dispositionAttachment builds a safe Content-Disposition header value
// (attachment) for filename via mime.FormatMediaType, which quotes/escapes
// any characters the sanitiser left in.
func dispositionAttachment(filename string) string {
	return mime.FormatMediaType("attachment", map[string]string{"filename": filename})
}

// exportFilename builds a filesystem-safe download name of the form
// "<name>-<id><ext>", falling back to "<id><ext>" when the name is empty and
// "chat<ext>" when both are empty. The name stem is rune-capped so a very
// long chat title can't produce an unwieldy filename.
func exportFilename(name, id, ext string) string {
	const maxStem = 80
	stem := sanitizeFilenamePart(name)
	if r := []rune(stem); len(r) > maxStem {
		stem = strings.TrimSpace(string(r[:maxStem]))
	}
	safeID := sanitizeFilenamePart(id)
	switch {
	case stem == "" && safeID == "":
		return "chat" + ext
	case stem == "":
		return safeID + ext
	case safeID == "":
		return stem + ext
	default:
		return stem + "-" + safeID + ext
	}
}

// sanitizeFilenamePart replaces control characters and characters unsafe in
// a filename (and in a Content-Disposition filename param) with '_', then
// trims surrounding whitespace.
func sanitizeFilenamePart(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
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
	return strings.TrimSpace(b.String())
}

// handleArchive moves a chat to the archive directory.
func (rt *Router) handleArchive(w http.ResponseWriter, r *http.Request, chatID api.ChatID) {
	if r.Method != http.MethodPost {
		api.MethodNotAllowed(w)
		return
	}
	if !chatIDPattern(chatID) {
		api.BadRequest(w, api.ErrMsgInvalidChatID)
		return
	}
	if err := rt.store.Archive(r.Context(), chatID); err != nil {
		slog.Error("chat archive failed", "chat_id", chatID, "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			api.ErrorJSON("archive failed"))
		return
	}
	api.Ok(w)
}

// handlePlanDraft dispatches GET/PUT/DELETE for the plan-draft sub-resource.
func (rt *Router) handlePlanDraft(w http.ResponseWriter, r *http.Request, chatID api.ChatID) {
	if !chatIDPattern(chatID) {
		api.BadRequest(w, api.ErrMsgInvalidChatID)
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
			api.ErrorJSON("read failed"))
		return
	}
	api.WriteJSON(w, map[string]string{"content": content})
}

// putPlanDraft serves PUT for the plan-draft sub-resource.
func (rt *Router) putPlanDraft(w http.ResponseWriter, r *http.Request, chatID api.ChatID) {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, api.MIMETypeJSON) {
		slog.Warn("chat plan_draft: unexpected content-type",
			"chat_id", chatID, "content_type", ct)
		api.BadRequest(w, "expected "+api.MIMETypeJSON)
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	// DecodeJSONInto owns the cap (+ json envelope overhead) and the
	// trailing-data rejection; the 413-vs-400 mapping stays here.
	if err := webhttp.DecodeJSONInto(w, r, &body, maxPlanDraftBytes+4096); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("chat plan_draft: body too large",
				"chat_id", chatID, "limit", maxPlanDraftBytes+4096, "error", maxErr)
			api.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				api.ErrorJSON("request body too large"))
			return
		}
		if errors.Is(err, webhttp.ErrTrailingData) {
			api.BadRequest(w, "unexpected trailing data")
			return
		}
		api.BadRequest(w, "invalid json")
		return
	}
	err := rt.store.SetPlanDraft(r.Context(), chatID, body.Content)
	if err != nil {
		if _, ok := errors.AsType[*StoreError](err); ok {
			writeChatErr(w, err)
		} else {
			slog.Error("chat plan_draft: save failed", "chat_id", chatID, "error", err)
			api.WriteJSONStatus(w, http.StatusInternalServerError,
				api.ErrorJSON("save failed"))
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
			api.ErrorJSON("delete failed"))
		return
	}
	slog.Debug("chat plan_draft: delete http", "chat_id", chatID)
	api.Ok(w)
}
