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

// RegisterRoutes wires GET /api/chats (list) and GET /api/chats/{id} (one chat
// with paginated messages).
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
	// The one sub-resource that is itself addressed: /tools/{toolCallID}.
	if rest, ok := strings.CutPrefix(sub, "tools/"); ok {
		rt.handleToolCall(w, r, cid, rest)
		return
	}
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
	window, start := messageWindow(msgs[:end], parseWindowBudget(r))

	// `draft` is its own field, keeping the composer autosave off the SSE fan-out.
	webhttp.WriteJSON(w, map[string]any{
		"chat":     c.Header(),
		"messages": window,
		"has_more": start > 0,
		"draft":    c.Draft,
	})
}

// windowBudget is what one transcript page may carry. A struct rather than four
// int parameters, which are interchangeable at a call site.
type windowBudget struct {
	// Messages caps the page's LENGTH, a bound on shape rather than size.
	Messages int
	// Bytes is the hostile-input bound: what the wire may carry.
	Bytes int
	// Blocks and ToolCalls are the client's residency budgets; planResidency stops
	// on whichever runs out first.
	Blocks    int
	ToolCalls int
}

// messageWindow returns the newest messages of msgs that fit EVERY budget, plus
// the index the window starts at, so a caller can answer has_more honestly.
//
// Bytes bound what the WIRE carries, the residency pair what the CLIENT can hold.
// Messages are marshalled HERE and returned as raw JSON, because the cut has to be
// decided on the bytes that go on the wire. It always falls at a message boundary
// and the newest message always goes through whole, or an over-budget chat's
// newest message would be unreachable; previewMessage bounds the message ITSELF.
func messageWindow(msgs []vibekit.Message, budget windowBudget) (window []json.RawMessage, start int) {
	// Non-nil: a nil slice marshals as `null` and the generated decoder rejects
	// `null` for an array.
	window = make([]json.RawMessage, 0, min(budget.Messages, len(msgs)))
	spentBytes := 0
	var spent messageCost
	start = len(msgs)
	for i := range slices.Backward(msgs) {
		if len(window) == budget.Messages {
			break
		}
		raw, err := json.Marshal(previewMessage(&msgs[i]))
		if err != nil {
			// Unreachable — a Message holds no type encoding/json can refuse — but
			// stop rather than serve a window with a hole in it.
			slog.Warn("chat window: message marshal failed",
				"message_id", msgs[i].ID, "error", err)
			break
		}
		cost := costOfMessage(&msgs[i])
		if len(window) > 0 && (spentBytes+len(raw) > budget.Bytes ||
			spent.Blocks+cost.Blocks > budget.Blocks ||
			spent.ToolCalls+cost.ToolCalls > budget.ToolCalls) {
			break
		}
		spentBytes += len(raw)
		spent.Blocks += cost.Blocks
		spent.ToolCalls += cost.ToolCalls
		window = append(window, raw)
		start = i
	}
	slices.Reverse(window)
	return window, start
}

// messageCost is what one message costs the client's two residency budgets.
type messageCost struct {
	Blocks    int
	ToolCalls int
}

// costOfMessage prices one message the way `block-window.ts turnCost` must:
// measuring differently would cut a page the client still stubs.
func costOfMessage(m *vibekit.Message) messageCost {
	return messageCost{Blocks: messageBlockCost(m), ToolCalls: len(m.ToolCalls)}
}

// messageBlockCost is the BLOCK half of costOfMessage. A message with no blocks
// costs ONE, because the reconcile unit is the message row.
//
// The synthesis mirrors `store.ts normalizeMessage` INCLUDING its role gate: only
// an ASSISTANT message persisted before the blocks field synthesizes per tool
// call. Missing either half misprices a legacy many-tool-call turn.
func messageBlockCost(m *vibekit.Message) int {
	if n := len(m.Blocks); n > 0 {
		return n
	}
	if m.Role != vibekit.RoleAssistant {
		return 1
	}
	n := len(m.ToolCalls)
	if m.Reasoning != "" {
		n++
	}
	if m.Content != "" {
		n++
	}
	return max(1, n)
}

// handleTurns serves GET /api/chats/{id}/turns: the session-wide turn index with
// no message bodies. Server-side because the client's transcript store holds a
// paginated window, so a rail built from resident turns would grow markers as the
// reader scrolled up.
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
	// thinking=false: the persisted record knows nothing about a mid-turn bridge.
	webhttp.WriteJSON(w, map[string]any{"turns": projectTurnSummaries(c.Messages, false)})
}

// handleSearch serves GET /api/chats/{id}/search?q=: a session-wide lexical scan.
// Server-side because the client's store is a paginated window.
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
	// Both halves of the in-chat search must agree on match-case, so it rides here.
	caseSensitive := r.URL.Query().Get("case") == "1"
	webhttp.WriteJSON(w, map[string]any{
		"hits": Search(c.Messages, r.URL.Query().Get("q"), caseSensitive),
	})
}

// parseLimitParam returns the validated ?limit= page size, defaulting to 50 over
// the inclusive 1..500 range.
func parseLimitParam(r *http.Request) int {
	return clampedQueryInt(r, "limit", 50, 1, 500)
}

// Byte budget bounds for the transcript window. Neither is a limit on the chat:
// has_more plus before_id is how the rest is reached.
const (
	defaultMaxBytes = 1 << 20 // 1 MiB
	maxMaxBytes     = 8 << 20 // 8 MiB
)

// parseMaxBytesParam returns the validated ?max_bytes= budget, defaulting to
// defaultMaxBytes over the inclusive 1 KiB..maxMaxBytes range. The floor is 1 KiB
// because anything under one message's envelope selects exactly one message
// however small it is set, so it only hides a client bug.
func parseMaxBytesParam(r *http.Request) int {
	return clampedQueryInt(r, "max_bytes", defaultMaxBytes, 1<<10, maxMaxBytes)
}

// Residency-count bounds, shared by ?blocks= and ?tool_calls=. The default is
// several of the client's own residency budgets, so a caller naming neither gets
// the byte-bounded answer; the ceiling is 8× it, as the byte budget's is.
const (
	defaultMaxBlocks = 1024
	maxMaxBlocks     = 8 * defaultMaxBlocks
)

// parseBlocksParam returns the validated ?blocks= budget, defaulting to
// defaultMaxBlocks over the inclusive 1..maxMaxBlocks range. The floor is 1
// because that is the smallest a message can cost.
func parseBlocksParam(r *http.Request) int {
	return clampedQueryInt(r, "blocks", defaultMaxBlocks, 1, maxMaxBlocks)
}

// parseToolCallsParam returns the validated ?tool_calls= budget, the second half
// of the client's residency pair. The default is the BLOCK default, so this budget
// cannot cut a page the block budget admitted; the floor is 0, because a page of
// pure prose costs no tool calls.
func parseToolCallsParam(r *http.Request) int {
	return clampedQueryInt(r, "tool_calls", defaultMaxBlocks, 0, maxMaxBlocks)
}

// parseWindowBudget reads the four page budgets off the query.
func parseWindowBudget(r *http.Request) windowBudget {
	return windowBudget{
		Messages:  parseLimitParam(r),
		Bytes:     parseMaxBytesParam(r),
		Blocks:    parseBlocksParam(r),
		ToolCalls: parseToolCallsParam(r),
	}
}

// clampedQueryInt returns the named query parameter when it parses as an integer
// inside the inclusive [lo, hi] range, and def for anything else. Out of range
// falls back to the DEFAULT rather than clamping, so a caller asking for something
// unserveable cannot keep believing the number it sent.
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
// exclusive upper bound of the page before it, or len(msgs) for an unknown id so
// an unknown cursor pages the newest window.
//
// A scan rather than a search on Message.Ts: message order is ARRAY POSITION, and
// Ts is stamped outside the per-chat lock, so two writers can append out of order.
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

// handleExport serves GET /api/chats/{id}/export?format=md|json, rendering the
// persisted chat to a downloadable Markdown transcript (the default) or raw JSON.
// The store is the source of truth, so no live bridge is involved.
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
// md/markdown selects Markdown, json selects raw JSON; anything else is rejected,
// so a typo fails loudly rather than serving the wrong format.
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

// dispositionAttachment builds the Content-Disposition value for filename via
// mime.FormatMediaType, which escapes anything the sanitiser left in.
func dispositionAttachment(filename string) string {
	return mime.FormatMediaType("attachment", map[string]string{"filename": filename})
}

// exportFilename builds a filesystem-safe "<name>-<id><ext>", falling back to
// "<id><ext>" when the name is empty and "chat<ext>" when both are. The stem is
// rune-capped, so a long chat title cannot produce an unwieldy filename.
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

// sanitizeFilenamePart replaces control characters and characters unsafe in a
// filename or a Content-Disposition param with '_', then trims whitespace.
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
