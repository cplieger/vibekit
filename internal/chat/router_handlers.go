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
	"github.com/cplieger/vibekit/internal/logsafe"
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

	msgs := c.Messages
	end := len(msgs)
	beforeID := r.URL.Query().Get("before_id")
	if beforeID != "" {
		end = indexOfMessage(msgs, beforeID)
	}
	// Rendered BEFORE the window and CHARGED against its byte budget: the live turn rides
	// the same response, so the caller's ?max_bytes= has to bound both or a page can
	// overrun it by the whole snapshot cap.
	liveTurn := rt.liveTurnField(vibekit.ChatID(id), beforeID == "")
	budget := parseWindowBudget(r)
	budget.Bytes = max(1, budget.Bytes-len(liveTurn))
	window, start := messageWindow(msgs[:end], budget)
	// `start` indexes `msgs` directly: `msgs[:end]` is a PREFIX, so an index into it
	// is the same index into the whole array and no re-basing is needed.
	turnOffset, segmentClosed := turnWindowBase(msgs, start)

	// `turn_open` ships with the transcript because the in-flight reply has no
	// carrier in `messages` until turn end, and `live_turn` beside it is that carrier:
	// without it a client deriving an outcome from the silence answers `unknown` mid-turn
	// and renders the prompt over an empty body. `has_more`, `turn_offset` and
	// `turn_segment_closed` all describe the window's LEFT EDGE, which the client's
	// projection cannot know: its own scan starts at the window.
	page := map[string]any{
		"chat":                c.Header(),
		"messages":            window,
		"has_more":            start > 0,
		"draft":               c.Draft,
		"turn_open":           rt.store.TurnOpen(vibekit.ChatID(id)),
		"turn_offset":         turnOffset,
		"turn_segment_closed": segmentClosed,
	}
	// ABSENT rather than null when there is no turn to describe: an older client ignores
	// an unknown field, and a present-but-empty one would name a message id the client
	// would adopt as its unpersisted live turn.
	if liveTurn != nil {
		page["live_turn"] = liveTurn
	}
	webhttp.WriteJSON(w, page)
}

// liveTurnField renders the chat's in-flight turn for the NEWEST page, or nil.
//
// Newest page only, which is the rule `turn_open` and `draft` already follow: a
// before_id fetch is a scroll-up and asserts nothing about the live edge, so re-delivering
// the in-flight turn on every page a reader scrolls back through would be pure cost.
//
// Returns the marshalled bytes rather than the value, so the caller can charge the page
// budget for exactly what goes on the wire instead of estimating it.
func (rt *Router) liveTurnField(chatID vibekit.ChatID, newestPage bool) json.RawMessage {
	if !newestPage {
		return nil
	}
	live, ok := rt.store.LiveTurn(chatID)
	if !ok {
		return nil
	}
	raw, err := json.Marshal(live)
	if err != nil {
		// Unreachable — a LiveTurn holds no type encoding/json can refuse — but serve the
		// window rather than failing the whole page over the field beside it.
		slog.Warn("chat window: live turn marshal failed",
			"chat_id", logsafe.Field(string(chatID)), "error", err)
		return nil
	}
	return raw
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
	// Turns is the FLOOR: a ceiling bounds the window's SIZE, this bounds its SHAPE,
	// so a ceiling may only cut once the window opens on a turn holding this many.
	Turns int
}

// breachedBy reports whether admitting one more message of msgBytes and cost
// would take the window past any of its three ceilings.
func (b windowBudget) breachedBy(spentBytes, msgBytes int, spent, cost messageCost) bool {
	return spentBytes+msgBytes > b.Bytes ||
		spent.Blocks+cost.Blocks > b.Blocks ||
		spent.ToolCalls+cost.ToolCalls > b.ToolCalls
}

// messageWindow returns the newest messages of msgs that meet the turn floor and
// fit every ceiling, plus the index the window starts at, so has_more is honest.
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
	openers := findTurnOpeners(msgs)
	// The floor is a floor on what is ACHIEVABLE: a transcript offering fewer turns
	// than asked for must still be able to satisfy it, or no ceiling ever fires.
	floor := min(budget.Turns, openers.total)
	spentBytes := 0
	turns := 0
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
		// The floor may not carry a page past the largest one a caller may ask for.
		if len(window) > 0 && spentBytes+len(raw) > maxMaxBytes {
			break
		}
		// A cut is admissible only where the window already holds whole turns, so a
		// ceiling can never end it mid-turn.
		floorMet := len(window) > 0 && turns >= floor && openers.admitCutAt(msgs, start)
		if floorMet && budget.breachedBy(spentBytes, len(raw), spent, cost) {
			break
		}
		spentBytes += len(raw)
		spent.Blocks += cost.Blocks
		spent.ToolCalls += cost.ToolCalls
		if openers.opens[i] {
			turns++
		}
		window = append(window, raw)
		start = i
	}
	slices.Reverse(window)
	return window, start
}

// turnOpeners is which indices of a message slice OPEN a turn, plus the first
// such index and how many there are.
type turnOpeners struct {
	opens []bool
	// first is len(msgs) for a slice that opens no turn at all.
	first int
	total int
}

// findTurnOpeners derives the opener set for msgs with opensTurn, the predicate
// turnWindowBase resolves the turn_offset from, so a window's left edge and the
// ordinal published beside it are a boundary in the same unit.
//
// One forward pass, because opensTurn is stateful: it reads the scan's position
// and the segmentation state as of the message before it.
func findTurnOpeners(msgs []vibekit.Message) turnOpeners {
	o := turnOpeners{opens: make([]bool, len(msgs)), first: len(msgs)}
	closed := false
	for i := range msgs {
		m := &msgs[i]
		if carriesNothing(m) {
			continue
		}
		if opensTurn(m, o.total == 0, closed) {
			o.opens[i] = true
			o.first = min(o.first, i)
			o.total++
			closed = closesTurn(m.TurnOutcome)
			continue
		}
		closed = closed || closesTurn(m.TurnOutcome)
	}
	return o
}

// admitCutAt reports whether a window opening at start opens on a turn boundary.
// The question is asked of the first message that RENDERS, because turnWindowBase
// skips the others when it resolves the base.
//
// A start with no opener at or before it is admissible too: no further walking
// could ever produce a boundary.
func (o turnOpeners) admitCutAt(msgs []vibekit.Message, start int) bool {
	if start < o.first {
		return true
	}
	for start < len(msgs) && carriesNothing(&msgs[start]) {
		start++
	}
	return start < len(msgs) && o.opens[start]
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
	// Liveness is injected: the persisted record cannot see a bridge mid-turn.
	webhttp.WriteJSON(w, map[string]any{
		"turns": projectTurnSummaries(c.Messages, rt.store.TurnOpen(chatID)),
	})
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
	// Both halves of the in-chat search must agree on the match-case toggle.
	caseSensitive := r.URL.Query().Get("case") == "1"
	webhttp.WriteJSON(w, map[string]any{
		"hits": Search(c.Messages, r.URL.Query().Get("q"), caseSensitive),
	})
}

// parseLimitParam returns the ?limit= page size, honouring 1..500 inclusive;
// anything else (absent, non-numeric, out of range) falls back to 50.
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

const defaultWindowTurns = 3

// parseTurnsParam returns the validated ?turns= floor, defaulting to
// defaultWindowTurns over the inclusive 1..50 range. The floor is 1 rather than 0
// because a window ending mid-turn opens on a turn it can only continue.
func parseTurnsParam(r *http.Request) int {
	return clampedQueryInt(r, "turns", defaultWindowTurns, 1, 50)
}

// parseWindowBudget reads the five page budgets off the query.
func parseWindowBudget(r *http.Request) windowBudget {
	return windowBudget{
		Messages:  parseLimitParam(r),
		Bytes:     parseMaxBytesParam(r),
		Blocks:    parseBlocksParam(r),
		ToolCalls: parseToolCallsParam(r),
		Turns:     parseTurnsParam(r),
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
		slog.Debug("chat export: markdown write failed",
			"chat_id", logsafe.Field(string(chatID)), "error", err)
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
