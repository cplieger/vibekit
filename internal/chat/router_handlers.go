package chat

import (
	"context"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/v2"
)

// RegisterRoutes wires GET /api/chats (list) and GET /api/chats/{id}
// (one chat with paginated messages).
func (s *Store) RegisterRoutes(mux *http.ServeMux) {
	rt := NewRouter(s)
	rt.Register(mux)
}

// handleList returns all chat headers.
func (rt *Router) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	headers := rt.store.List(r.Context())
	webhttp.WriteJSON(w, map[string]any{"chats": headers})
}

// handleOne serves GET /api/chats/{id}?before_id=<id>&limit=<n> and routes
// /api/chats/{id}/<sub-resource> requests to their handlers.
func (rt *Router) handleOne(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/chats/")
	if rest == "" || strings.HasPrefix(rest, "/") {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}
	if id, sub, ok := strings.Cut(rest, "/"); ok {
		rt.routeChatSubResource(w, r, vibekit.ChatID(id), sub)
		return
	}
	rt.serveChatMessages(w, r, rest)
}

// routeChatSubResource dispatches /api/chats/{id}/<sub> to its handler.
func (rt *Router) routeChatSubResource(w http.ResponseWriter, r *http.Request, cid vibekit.ChatID, sub string) {
	switch sub {
	case "export":
		rt.handleExport(w, r, cid)
	case "turns":
		rt.handleTurns(w, r, cid)
	case "search":
		rt.handleSearch(w, r, cid)
	default:
		httpreply.NotFound(w, "unknown chat sub-resource")
	}
}

// serveChatMessages serves the paginated single-chat GET for /api/chats/{id}.
func (rt *Router) serveChatMessages(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if !chatIDPattern(vibekit.ChatID(id)) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}
	c, ok := rt.store.Get(r.Context(), vibekit.ChatID(id))
	if !ok {
		httpreply.NotFound(w, errMsgChatNotFound)
		return
	}

	limit := parseLimitParam(r)
	beforeID := r.URL.Query().Get("before_id")

	msgs := c.Messages
	end := len(msgs)
	if beforeID != "" {
		end = indexOfMessage(msgs, beforeID)
	}
	start := max(end-limit, 0)
	// NOT slices.Clone: a sub-slice of a nil array clones to nil and marshals as
	// `null`, which the wire decoder rejects for an array. make+copy yields `[]`.
	window := make([]vibekit.Message, end-start)
	copy(window, msgs[start:end])

	// `turn_open` ships with the transcript because the in-flight reply has no
	// carrier in `messages` until turn end, so a client deriving an outcome from
	// that silence would answer `unknown` mid-turn.
	webhttp.WriteJSON(w, map[string]any{
		"chat":      c.Header(),
		"messages":  window,
		"has_more":  start > 0,
		"draft":     c.Draft,
		"turn_open": rt.store.TurnOpen(vibekit.ChatID(id)),
	})
}

// handleTurns serves GET /api/chats/{id}/turns: the chat's session-wide turn
// index (number, outcome, start time, first line) with no message bodies.
// Session-wide because the client's transcript store is a paginated window, so
// a rail assembled from resident turns would grow markers on scroll-up.
func (rt *Router) handleTurns(w http.ResponseWriter, r *http.Request, chatID vibekit.ChatID) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if !chatIDPattern(chatID) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}
	c, ok := rt.store.Get(r.Context(), chatID)
	if !ok {
		httpreply.NotFound(w, errMsgChatNotFound)
		return
	}
	// Liveness is injected: the persisted record cannot see a bridge mid-turn.
	webhttp.WriteJSON(w, map[string]any{
		"turns": projectTurnSummaries(c.Messages, rt.store.TurnOpen(chatID)),
	})
}

// handleSearch serves GET /api/chats/{id}/search?q=: a lexical scan of the
// chat's messages, session-wide. Server-side because the client's store is a
// paginated window; see search.go's header for why there is no index.
func (rt *Router) handleSearch(w http.ResponseWriter, r *http.Request, chatID vibekit.ChatID) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if !chatIDPattern(chatID) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}
	c, ok := rt.store.Get(r.Context(), chatID)
	if !ok {
		httpreply.NotFound(w, errMsgChatNotFound)
		return
	}
	// Both halves of the in-chat search must agree on the match-case toggle.
	caseSensitive := r.URL.Query().Get("case") == "1"
	webhttp.WriteJSON(w, map[string]any{
		"hits": Search(c.Messages, r.URL.Query().Get("q"), caseSensitive),
	})
}

// parseLimitParam returns the ?limit= page size, honouring 1..500 inclusive;
// anything else (absent, non-numeric, out of range) falls back to 50.
func parseLimitParam(r *http.Request) int {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	return limit
}

// indexOfMessage returns the position of the message with the given id, the
// exclusive upper bound of the page before it. Returns len(msgs) for an unknown
// id, so an unknown cursor pages the newest window rather than an empty one.
func indexOfMessage(msgs []vibekit.Message, id string) int {
	for i := range slices.Backward(msgs) {
		if msgs[i].ID == id {
			return i
		}
	}
	return len(msgs)
}

// exportFormat is the requested export serialization.
type exportFormat int

const (
	exportFormatMarkdown exportFormat = iota
	exportFormatJSON
)

// handleExport serves GET /api/chats/{id}/export?format=md|json as a
// downloadable Markdown transcript (the default) or the raw chat JSON.
func (rt *Router) handleExport(w http.ResponseWriter, r *http.Request, chatID vibekit.ChatID) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if !chatIDPattern(chatID) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}
	format, ok := parseExportFormat(r.URL.Query().Get("format"))
	if !ok {
		httpreply.BadRequest(w, "unsupported export format (use md or json)")
		return
	}
	c, found := rt.loadForExport(r.Context(), chatID)
	if !found {
		httpreply.NotFound(w, errMsgChatNotFound)
		return
	}
	if format == exportFormatJSON {
		w.Header().Set("Content-Disposition",
			dispositionAttachment(exportFilename(c.Name, string(chatID), ".json")))
		webhttp.WriteJSON(w, c)
		return
	}
	w.Header().Set("Content-Disposition",
		dispositionAttachment(exportFilename(c.Name, string(chatID), ".md")))
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if _, err := io.WriteString(w, renderChatMarkdown(c)); err != nil {
		slog.Debug("chat export: markdown write failed", "chat_id", chatID, "error", err)
	}
}

// parseExportFormat maps ?format= to an exportFormat: absent/md/markdown to
// Markdown, json to raw JSON, anything else rejected so a typo fails loudly.
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

// loadForExport returns the chat for chatID.
func (rt *Router) loadForExport(ctx context.Context, chatID vibekit.ChatID) (*vibekit.Chat, bool) {
	return rt.store.Get(ctx, chatID)
}

// dispositionAttachment builds an attachment Content-Disposition value via
// mime.FormatMediaType, which escapes anything the sanitiser left in.
func dispositionAttachment(filename string) string {
	return mime.FormatMediaType("attachment", map[string]string{"filename": filename})
}

// exportFilename builds a filesystem-safe "<name>-<id><ext>", falling back to
// "<id><ext>" when the name is empty and "chat<ext>" when both are. The stem is
// rune-capped, so a very long chat title cannot produce an unwieldy filename.
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

// sanitizeFilenamePart replaces control and filename-unsafe characters with
// '_', then trims surrounding whitespace.
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
