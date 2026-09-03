package chat

import (
	"context"
	"encoding/json"
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
// (one chat with paginated messages). Delegates to Router for structural
// separation of HTTP concerns.
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

// routeChatSubResource dispatches /api/chats/{id}/<sub> to the handler for
// the addressed sub-resource.
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

// serveChatMessages serves the paginated single-chat GET for a bare
// /api/chats/{id} request.
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

	msgs := c.Messages
	end := len(msgs)
	if beforeID := r.URL.Query().Get("before_id"); beforeID != "" {
		end = indexOfMessage(msgs, beforeID)
	}
	window, start := messageWindow(msgs[:end], parseLimitParam(r), parseMaxBytesParam(r))

	// `draft` rides here as its own field, keeping the composer autosave
	// off the SSE fan-out and off the list response.
	webhttp.WriteJSON(w, map[string]any{
		"chat":     c.Header(),
		"messages": window,
		"has_more": start > 0,
		"draft":    c.Draft,
	})
}

// messageWindow returns the newest messages of msgs that fit BOTH budgets, plus
// the index the window starts at — so a caller can answer has_more honestly.
//
// A message count alone is not a budget. It defaulted to 50 while the five live
// chats held 2, 4, 10, 10 and 13 messages, so the "paginated window" was every
// real conversation whole, and a SIX-message chat answered 13,010,641 bytes: one
// assistant message can carry 580 blocks and 353 tool calls. Bytes are what the
// client is actually waiting for, so bytes are what the window is measured in.
//
// The messages are marshalled HERE, once, and returned as raw JSON. Measuring
// one encoding and shipping another would be a second implementation of "how
// big is this", and the cut has to be decided on the bytes that go on the wire.
//
// The cut is always at a message boundary and the newest message always goes
// through whole, however big it is. The envelope is the client's reconcile unit,
// so a half-message has no honest `blocks` array — and a budget that could
// return nothing would make the newest message of an over-budget chat
// unreachable.
func messageWindow(msgs []vibekit.Message, limit, maxBytes int) ([]json.RawMessage, int) {
	// Non-nil: a nil slice marshals as `null` and the generated decoder rejects
	// `null` for an array. Same guard as the one the make+copy here replaced.
	window := make([]json.RawMessage, 0, min(limit, len(msgs)))
	spent := 0
	start := len(msgs)
	for i := range slices.Backward(msgs) {
		if len(window) == limit {
			break
		}
		raw, err := json.Marshal(msgs[i])
		if err != nil {
			// A Message holds no type encoding/json can refuse, so this is
			// unreachable; stop rather than serve a window with a hole in it.
			slog.Warn("chat window: message marshal failed",
				"message_id", msgs[i].ID, "error", err)
			break
		}
		if len(window) > 0 && spent+len(raw) > maxBytes {
			break
		}
		spent += len(raw)
		window = append(window, raw)
		start = i
	}
	slices.Reverse(window)
	return window, start
}

// handleTurns serves GET /api/chats/{id}/turns: the chat's session-wide turn
// index (number, outcome, start time, first line) with no message bodies.
//
// The timeline rail spans the whole session while the client's transcript
// store holds a paginated window, so a rail assembled from resident turns
// would grow markers as the reader scrolled up.
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
	// thinking=false: the store is the persisted record and knows nothing
	// about a bridge being mid-turn.
	webhttp.WriteJSON(w, map[string]any{"turns": projectTurnSummaries(c.Messages, false)})
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
	// `case=1` is the client's match-case toggle; both halves of the
	// in-chat search must agree on it, so it rides the request.
	caseSensitive := r.URL.Query().Get("case") == "1"
	webhttp.WriteJSON(w, map[string]any{
		"hits": Search(c.Messages, r.URL.Query().Get("q"), caseSensitive),
	})
}

// parseLimitParam returns the validated ?limit= page size, defaulting to 50
// and honouring values in the inclusive 1..500 range; anything else (absent,
// non-numeric, <=0, >500) falls back to the default.
func parseLimitParam(r *http.Request) int {
	return clampedQueryInt(r, "limit", 50, 1, 500)
}

// Byte budget bounds for the transcript window. The default is what a first
// paint is worth waiting for; the ceiling is what a reader who scrolled up may
// ask for at once. Neither is a limit on the chat — has_more plus before_id is
// how the rest is reached.
const (
	defaultMaxBytes = 1 << 20 // 1 MiB
	maxMaxBytes     = 8 << 20 // 8 MiB
)

// parseMaxBytesParam returns the validated ?max_bytes= budget for the
// transcript window, defaulting to defaultMaxBytes and honouring the inclusive
// 1 KiB..maxMaxBytes range.
//
// The floor is 1 KiB rather than 1 byte because a budget below one message's
// envelope selects exactly one message however small it is set, so anything
// under it is indistinguishable from zero and only hides a client bug.
func parseMaxBytesParam(r *http.Request) int {
	return clampedQueryInt(r, "max_bytes", defaultMaxBytes, 1<<10, maxMaxBytes)
}

// clampedQueryInt returns the named query parameter when it parses as an
// integer inside the inclusive [lo, hi] range, and def for anything else —
// absent, non-numeric, or out of range.
//
// Out of range falls back to the DEFAULT rather than clamping to the nearer
// bound: a caller that asked for something this endpoint will not serve has a
// bug, and silently serving the ceiling instead would let it keep believing the
// number it sent.
func clampedQueryInt(r *http.Request, name string, def, lo, hi int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return def
	}
	return n
}

// indexOfMessage returns the position of the message with the given id, the
// exclusive upper bound of the page before it. Returns len(msgs) for an
// unknown id, so an unknown cursor pages the newest window rather than an
// empty one.
//
// This replaced a `?before=<ts>` cursor resolved with sort.Search over
// Message.Ts. Message order is ARRAY POSITION, not sorted, and
// translate.newEventMessage stamps Ts outside the per-chat lock
// AppendMessage takes — so two writers can stamp 101 and 102 and append 102
// first, making sort.Search return an arbitrary index. An id is exact and
// needs no ordering invariant.
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

// handleExport serves GET /api/chats/{id}/export?format=md|json, rendering
// the persisted chat to a downloadable Markdown transcript (the default) or
// the raw chat JSON. The chat store is the source of truth, so no live ACP
// bridge is involved.
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
func (rt *Router) loadForExport(ctx context.Context, chatID vibekit.ChatID) (*vibekit.Chat, bool) {
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
