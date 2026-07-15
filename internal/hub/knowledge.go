package hub

// Knowledge-base management: list / add / remove workspace knowledge contexts
// over the v3 (KAS) _kiro/knowledge C→A request. Mirrors the REST-backed-by-
// bridge.Call shape of mcp_control.go.
//
// Bridge-targeting model (knowledge is workspace-GLOBAL, unlike MCP whose pool
// is per-bridge): the store is disk-backed at $KIRO_HOME/.kiro/knowledge_bases/
// default and shared by every kiro-cli process on the same KIRO_HOME. So these
// ops route through the long-lived UTILITY bridge (same as account/getUsage) —
// it's always available even with no chat open, and _kiro/knowledge is issued
// WITHOUT a sessionId so it deterministically targets that global default
// store. (A builtin-agent chat session resolves to the same store, but a
// custom "wire" agent session would get its own in-memory store; omitting
// sessionId sidesteps that entirely.)
//
// Verified live against kiro-cli acp v3 2.12.0:
//   - subcommands: show / add / remove / update / clear / cancel; an unknown
//     subcommand returns {success:false, message:"Unknown subcommand: …"}.
//   - `add` is ASYNC: it returns {success:true, message:"Indexing … in
//     background"} immediately and indexes in a background operation. Progress
//     shows up in `show` as an entry with indexing:true + items_display ("0%",
//     "42% · ETA 3s"); the finished context replaces it with a real item_count.
//   - a user-initiated `add` does NOT emit _kiro/knowledge/indexing* — those
//     fire only for agent-declared knowledge_bases sync at session start — so
//     the client polls `show` for user-add progress (see translate/knowledge.go
//     for the notification → knowledge_indexing SSE that covers the agent case).

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// keySubcommand is the _kiro/knowledge dispatch field naming the operation.
const keySubcommand = "subcommand"

// knowledgeCallTimeout bounds one _kiro/knowledge round-trip. show/add/remove
// are fast (add returns immediately, indexing is backgrounded); the only slow
// path is the first call, which lazily spins up the utility bridge
// (session/new + auth handshake). bridge.Call has no timeout of its own.
const knowledgeCallTimeout = 45 * time.Second

// kasKnowledgeResult is the _kiro/knowledge reply. `show` fills Entries;
// add/remove/update/clear/cancel fill Message. Success is false for a usage
// error (missing args), a not-found remove target, or an unknown subcommand.
type kasKnowledgeResult struct {
	Message string              `json:"message"`
	Entries []kasKnowledgeEntry `json:"entries"`
	Success bool                `json:"success"`
}

// kasKnowledgeEntry is one entry in a `show` reply: either an indexed context
// (item_count + path, no indexing flag) or an in-flight operation (indexing
// true + items_display progress). Shapes verified on the live wire.
type kasKnowledgeEntry struct {
	Name         string `json:"name"`
	ID           string `json:"id"`
	Description  string `json:"description"`
	Path         string `json:"path"`
	ItemsDisplay string `json:"items_display"`
	ItemCount    int    `json:"item_count"`
	Indexing     bool   `json:"indexing"`
}

// knowledgeContext is one entry in the GET /api/knowledge response. Field set
// mirrors kasKnowledgeEntry; the client keys rows by ID and drives the live
// progress ring from Indexing + ItemsDisplay.
type knowledgeContext struct {
	Name         string `json:"name"`
	ID           string `json:"id"`
	Description  string `json:"description,omitempty"`
	Path         string `json:"path,omitempty"`
	ItemsDisplay string `json:"items_display,omitempty"`
	ItemCount    int    `json:"item_count"`
	Indexing     bool   `json:"indexing,omitempty"`
}

// knowledgeListResponse is the GET /api/knowledge body.
type knowledgeListResponse struct {
	Contexts []knowledgeContext `json:"contexts"`
}

// knowledgeMessageResponse is the POST /api/knowledge (add) success body: the
// KAS background-indexing confirmation message, surfaced to the user verbatim.
type knowledgeMessageResponse struct {
	Message string `json:"message"`
}

// knowledgeCall lazily constructs the utility bridge (same pattern as
// AccountUsage/UtilityPrompt) and issues one _kiro/knowledge request with a
// bounded timeout. params carry the subcommand and its arguments; sessionId is
// intentionally never set (global default store).
func (h *Hub) knowledgeCall(ctx context.Context, params map[string]any) (json.RawMessage, error) {
	u := h.ensureUtility()
	cctx, cancel := context.WithTimeout(ctx, knowledgeCallTimeout)
	defer cancel()
	return u.session.knowledgeRaw(cctx, params)
}

// parseKnowledgeResult decodes the raw _kiro/knowledge JSON-RPC result.
func parseKnowledgeResult(raw json.RawMessage) (*kasKnowledgeResult, error) {
	if len(raw) == 0 {
		return nil, errors.New("knowledge: empty result")
	}
	var r kasKnowledgeResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// knowledgeShow lists the global store's contexts + any in-flight indexing
// operations.
func (h *Hub) knowledgeShow(ctx context.Context) ([]knowledgeContext, error) {
	raw, err := h.knowledgeCall(ctx, map[string]any{keySubcommand: "show"})
	if err != nil {
		return nil, err
	}
	res, err := parseKnowledgeResult(raw)
	if err != nil {
		return nil, err
	}
	if !res.Success {
		return nil, errors.New(cleanKnowledgeMsg(res.Message))
	}
	// kasKnowledgeEntry and knowledgeContext have identical field layout
	// (only json tags differ), so a direct conversion suffices.
	out := make([]knowledgeContext, 0, len(res.Entries))
	for _, e := range res.Entries {
		out = append(out, knowledgeContext(e))
	}
	return out, nil
}

// resolveKnowledgePath makes a user-supplied knowledge path absolute: a
// relative path resolves against the workspace dir; an absolute path is
// cleaned and used as-is. Sending an absolute path to KAS removes any
// ambiguity about which cwd its file enumerator resolves against.
func (h *Hub) resolveKnowledgePath(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(h.lifecycle.workDir, p)
}

// cleanKnowledgeMsg trims a KAS message for surfacing as an HTTP error,
// falling back to a generic sentinel when empty.
func cleanKnowledgeMsg(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "knowledge operation failed"
	}
	return s
}

// --- HTTP handlers (registered by registerKnowledgeRoutes) ---

// handleKnowledgeList: GET /api/knowledge → the global store's contexts +
// in-flight indexing operations. The client polls this while any entry is
// still indexing.
func (h *Hub) handleKnowledgeList(w http.ResponseWriter, r *http.Request) {
	ctxs, err := h.knowledgeShow(r.Context())
	if err != nil {
		h.writeKnowledgeErr(w, err)
		return
	}
	api.WriteJSON(w, knowledgeListResponse{Contexts: ctxs})
}

type knowledgeAddReq struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// handleKnowledgeAdd: POST /api/knowledge {path, name?} → start a background
// index of a directory. Name defaults to the path's base name. Returns the
// KAS confirmation message on success (200); a usage / bad-path failure is a
// 400 with the KAS message.
func (h *Hub) handleKnowledgeAdd(w http.ResponseWriter, r *http.Request) {
	var body knowledgeAddReq
	if !api.DecodeJSON(w, r, &body) {
		return
	}
	path := strings.TrimSpace(body.Path)
	if path == "" {
		api.BadRequest(w, "path required")
		return
	}
	abs := h.resolveKnowledgePath(path)
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = filepath.Base(abs)
	}
	res, err := h.knowledgeMutate(r.Context(), map[string]any{
		keySubcommand: "add",
		"name":        name,
		"path":        abs,
	})
	if err != nil {
		h.writeKnowledgeErr(w, err)
		return
	}
	if !res.Success {
		api.BadRequest(w, cleanKnowledgeMsg(res.Message))
		return
	}
	api.WriteJSON(w, knowledgeMessageResponse{Message: res.Message})
}

// handleKnowledgeRemove: DELETE /api/knowledge/{name} → drop a context by name
// (KAS matches path first, then name). A missing target is a 404.
func (h *Hub) handleKnowledgeRemove(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		api.BadRequest(w, "name required")
		return
	}
	res, err := h.knowledgeMutate(r.Context(), map[string]any{
		keySubcommand: "remove",
		"target":      name,
	})
	if err != nil {
		h.writeKnowledgeErr(w, err)
		return
	}
	if !res.Success {
		api.NotFound(w, cleanKnowledgeMsg(res.Message))
		return
	}
	api.Ok(w)
}

// knowledgeMutate issues a mutating subcommand (add/remove/…) and parses the
// {success, message} reply.
func (h *Hub) knowledgeMutate(ctx context.Context, params map[string]any) (*kasKnowledgeResult, error) {
	raw, err := h.knowledgeCall(ctx, params)
	if err != nil {
		return nil, err
	}
	return parseKnowledgeResult(raw)
}

// writeKnowledgeErr maps a bridge / kiro-cli failure to 502 with a generic
// message (details logged, not leaked). There is no errNoLiveBridge case: the
// utility bridge is auto-started, so a failure here is a backend fault, not a
// "open a chat first" condition.
func (h *Hub) writeKnowledgeErr(w http.ResponseWriter, err error) {
	slog.Warn("knowledge op failed", "error", err)
	api.WriteJSONStatus(w, http.StatusBadGateway, api.ErrorJSON("knowledge request failed"))
}

// registerKnowledgeRoutes wires the knowledge-base management endpoints.
func (h *Hub) registerKnowledgeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/knowledge", h.handleKnowledgeList)
	mux.HandleFunc("POST /api/knowledge", h.handleKnowledgeAdd)
	mux.HandleFunc("DELETE /api/knowledge/{name}", h.handleKnowledgeRemove)
}
