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

	"github.com/cplieger/vibekit/internal/api"
)

// RegisterRoutes wires GET /api/chats (list) and GET /api/chats/{id}
// (one chat with paginated messages). Delegates to Router for structural
// separation of HTTP concerns.
func (s *Store) RegisterRoutes(mux *http.ServeMux) {
	rt := NewRouter(s)
	rt.Register(mux)
}

// handleList returns all chat headers.
func (rt *Router) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w, http.MethodGet)
		return
	}
	headers := rt.store.List(r.Context())
	api.WriteJSON(w, map[string]any{"chats": headers})
}

// handleOne serves GET /api/chats/{id}?before_id=<id>&limit=<n> and routes
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
	case "export":
		rt.handleExport(w, r, cid)
	case "turns":
		rt.handleTurns(w, r, cid)
	case "search":
		rt.handleSearch(w, r, cid)
	default:
		api.NotFound(w, "unknown chat sub-resource")
	}
}

// serveChatMessages serves the paginated single-chat GET for a bare
// /api/chats/{id} request.
func (rt *Router) serveChatMessages(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w, http.MethodGet)
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
	beforeID := r.URL.Query().Get("before_id")

	msgs := c.Messages
	end := len(msgs)
	if beforeID != "" {
		end = indexOfMessage(msgs, beforeID)
	}
	start := max(end-limit, 0)
	window := make([]api.Message, end-start)
	copy(window, msgs[start:end])

	// `draft` rides here as its own field rather than on the header, which is
	// what keeps the composer autosave off the SSE fan-out and off the list
	// response: this is the one request a client makes when it opens a chat it
	// has no local draft for, which is exactly when it needs the server's copy.
	api.WriteJSON(w, map[string]any{
		"chat":     rt.store.header(r.Context(), c),
		"messages": window,
		"has_more": start > 0,
		"draft":    c.Draft,
	})
}

// handleTurns serves GET /api/chats/{id}/turns: the chat's session-wide turn
// index (number, outcome, start time, first line) with no message bodies.
//
// The timeline rail spans the whole session while the client's transcript store
// holds a paginated window, so a rail assembled from resident turns would grow
// markers as the reader scrolled up — exactly the progress read-out it claims
// not to be. Without this route the client's only option is walking `?before_id=`
// to the beginning of history and pulling every message body over the wire to
// count turns.
//
// It is cheap on purpose: `Get` already materialises the whole chat (the
// paginated read does too, then discards all but a window), so the added cost
// here is serialising a few fields per turn rather than any extra IO.
func (rt *Router) handleTurns(w http.ResponseWriter, r *http.Request, chatID api.ChatID) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if !chatIDPattern(chatID) {
		api.BadRequest(w, api.ErrMsgInvalidChatID)
		return
	}
	c, ok := rt.store.Get(r.Context(), chatID)
	if !ok {
		api.NotFound(w, errMsgChatNotFound)
		return
	}
	// thinking=false: the store is the persisted record and knows nothing about
	// a bridge being mid-turn. The client owns the live turn's outcome, which is
	// the one turn it always has resident anyway.
	api.WriteJSON(w, map[string]any{"turns": api.ProjectTurnSummaries(c.Messages, false)})
}

// handleSearch serves GET /api/chats/{id}/search?q=: a lexical scan of the
// chat's messages, session-wide.
//
// Server-side because the CLIENT CANNOT DO IT HONESTLY. Its store is a paginated
// window, so a store-only search would present itself as searching the
// conversation while covering only the resident tail. It is also what makes
// progressive collapse acceptable: a collapse that hides content from search is
// a data-loss bug. See search.go's header for why there is no index.
func (rt *Router) handleSearch(w http.ResponseWriter, r *http.Request, chatID api.ChatID) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if !chatIDPattern(chatID) {
		api.BadRequest(w, api.ErrMsgInvalidChatID)
		return
	}
	c, ok := rt.store.Get(r.Context(), chatID)
	if !ok {
		api.NotFound(w, errMsgChatNotFound)
		return
	}
	// `case=1` is the client's match-case toggle. Both halves of the in-chat
	// search have to agree on it (the client highlights in the DOM, this
	// enumerates session-wide), so it rides the request rather than being a
	// default either side could get wrong. Absent or anything else = insensitive,
	// which is the behaviour every existing client gets.
	caseSensitive := r.URL.Query().Get("case") == "1"
	api.WriteJSON(w, map[string]any{
		"hits": SearchChat(c.Messages, r.URL.Query().Get("q"), caseSensitive),
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

// indexOfMessage returns the position of the message with the given id, which is
// the exclusive upper bound of the page before it. It returns len(msgs) for an id
// the chat does not hold, so an unknown cursor pages the newest window rather
// than an empty one.
//
// This replaced a `?before=<ts>` cursor resolved with sort.Search over
// Message.Ts, and the replacement is a correctness fix rather than a style
// preference. Two things were wrong with the timestamp:
//
//   - Binary search needs the field to be non-decreasing across the slice, and
//     nothing makes that true. Message order is ARRAY POSITION (nothing sorts a
//     transcript for rendering), and translate.newEventMessage stamps Ts when it
//     CONSTRUCTS a message, outside the per-chat lock AppendMessage takes — so two
//     writers can stamp 101 and 102 and then append 102 first. On a slice that is
//     not ordered, sort.Search returns an arbitrary index and the page is silently
//     dropped or repeated. The client catches a repeat by id; nothing catches a drop.
//   - Even on an ordered slice, Search returns the FIRST index at the boundary
//     value, so a group of messages sharing a millisecond was excluded wholesale.
//     Ties are reachable from the two writers above and one is created on purpose:
//     projection.applySummary gives a compaction event its predecessor's Ts so the
//     event sorts where it belongs.
//
// An id is exact, so neither failure exists, and it needs no ordering invariant at
// all. Cost is a backwards scan of an already-materialised slice; the store loads
// the whole chat for this request either way.
func indexOfMessage(msgs []api.Message, id string) int {
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

// handleExport serves GET /api/chats/{id}/export?format=md|json, rendering
// the persisted chat to a downloadable Markdown transcript (the default) or
// the raw chat JSON. The chat store is the source of truth, so no live ACP
// bridge is involved.
func (rt *Router) handleExport(w http.ResponseWriter, r *http.Request, chatID api.ChatID) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w, http.MethodGet)
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

// loadForExport returns the chat for chatID. One lookup: chats never move, so
// there is no second location to fall back to.
func (rt *Router) loadForExport(ctx context.Context, chatID api.ChatID) (*api.Chat, bool) {
	return rt.store.Get(ctx, chatID)
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
