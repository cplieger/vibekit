// HTTP handlers for the forges package.
//
// Routes:
//   GET    /api/forges                          — list configured forges
//   POST   /api/forges/refresh                  — re-read CLI configs
//   POST   /api/forges/oauth/github/start       — start GH device flow
//   POST   /api/forges/oauth/github/poll        — poll GH device flow
//   POST   /api/forges/{id}/login/pat           — login via PAT (any kind: github/gitlab/gitea/codeberg)
//   POST   /api/forges/{id}/probe               — verify auth still works
//   DELETE /api/forges/{id}                     — disconnect (remove from CLI)
//
//   GET    /api/forges/{id}/repos               — list accessible repos
//   GET    /api/forges/{id}/repos/{owner}/{name}/prs    — list PRs
//   POST   /api/forges/{id}/repos/{owner}/{name}/prs    — create PR
//   POST   /api/forges/{id}/repos/{owner}/{name}/prs/{n}/merge — merge
//            ?method=merge|squash|rebase  merge strategy
//            ?head_sha=<sha>              refuse the merge if the head moved
//            ?auto=1                      arm the forge's auto-merge instead
//   POST   /api/forges/{id}/repos/{owner}/{name}/prs/{n}/close — close
//   POST   /api/forges/{id}/repos/{owner}/{name}/prs/{n}/reopen — reopen
//   POST   /api/forges/{id}/repos/{owner}/{name}/prs/{n}/rerun — re-run failed CI
//   GET    /api/forges/{id}/repos/{owner}/{name}/issues       — list issues
//   POST   /api/forges/{id}/repos/{owner}/{name}/issues       — create issue
//   POST   /api/forges/{id}/repos/{owner}/{name}/issues/{n}/close — close
//   GET    /api/forges/{id}/repos/{owner}/{name}/checks?ref=… — CI checks
//   GET    /api/forges/{id}/repos/{owner}/{name}/releases     — list releases
//   POST   /api/forges/{id}/repos/{owner}/{name}/releases     — create release
//   GET    /api/forges/{id}/repos/{owner}/{name}/labels       — list labels

package forges

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/webhttp"
)

// ForgeErrCode is a typed error code for machine-readable forge HTTP error responses.
type ForgeErrCode string

// ForgeErrCLINotInstalled and the following constants define the ForgeErrCode values for machine-readable HTTP error responses.
const (
	ForgeErrCLINotInstalled ForgeErrCode = "cli_not_installed"
	ForgeErrNotLoggedIn     ForgeErrCode = "not_logged_in"
	ForgeErrNotSupported    ForgeErrCode = "not_supported"
)

// broadcaster is the SSE fan-out a forge connection change is announced on, so
// every client's forge UI refetches /api/forges. *hub.Hub satisfies it.
//
// Declared HERE, at the consumer, rather than in a shared contract package.
// 1 method against a *hub.Hub exporting well over a hundred; this package fires
// exactly one event kind and needs nothing else from the hub at all.
type broadcaster interface {
	Broadcast(ctx context.Context, evt api.ServerEvent)
}

// HTTPHandler exposes the forges package over HTTP.
type HTTPHandler struct {
	manager      *Manager
	broadcaster  broadcaster
	onChange     func()
	probeTimeout time.Duration
}

// NewHTTPHandler builds an HTTPHandler with the given Manager.
// broadcaster may be nil; when nil, forge change events are silently dropped.
func NewHTTPHandler(m *Manager, b broadcaster) *HTTPHandler {
	return &HTTPHandler{
		manager:      m,
		broadcaster:  b,
		probeTimeout: 20 * time.Second,
	}
}

// SetOnChange wires a callback fired whenever a forge connection
// changes (PAT login, OAuth completion, disconnect, refresh). Called
// once at composition; used to refresh the steering forge-snapshot
// cache and regenerate environment.md. The callback must not block —
// it runs on the HTTP request path — and must not capture the request
// context (composition kicks its work onto the app-lifetime context).
func (h *HTTPHandler) SetOnChange(fn func()) { h.onChange = fn }

// notifyChanged broadcasts a forges_changed event so the UI's repo
// picker (and any other listeners) refresh, then fires the onChange
// callback. No-op parts are skipped when unwired.
func (h *HTTPHandler) notifyChanged(ctx context.Context) {
	if h.broadcaster != nil {
		h.broadcaster.Broadcast(ctx, api.NewEvent(api.EventForgesChanged, "", api.ForgesChangedPayload{}))
	}
	if h.onChange != nil {
		h.onChange()
	}
}

// Compile-time interface assertion.

// RegisterRoutes installs the /api/forges/* mux entries.
func (h *HTTPHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/forges", h.handleForgesList)
	mux.HandleFunc("/api/forges/", h.handleForgeItem)
	mux.HandleFunc("/api/forges/refresh", h.handleRefresh)
	mux.HandleFunc("/api/forges/oauth/github/start", h.handleGitHubDeviceStart)
	mux.HandleFunc("/api/forges/oauth/github/poll", h.handleGitHubDevicePoll)
}

// handleForgesList returns all configured forges.
func (h *HTTPHandler) handleForgesList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	forges := h.manager.List(r.Context())
	webhttp.WriteJSON(w, map[string]any{
		"forges": forges,
		"kinds":  AllKinds(),
		"oauth":  map[string]bool{string(KindGitHub): true}, // device flow is built-in
	})
}

func (h *HTTPHandler) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	if err := h.manager.Refresh(r.Context()); err != nil {
		httpreply.ServerError(w, "refresh failed", err)
		return
	}
	webhttp.Ok(w)
}

// handleForgeItem dispatches to per-forge sub-resources.
func (h *HTTPHandler) handleForgeItem(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/api/forges/")
	if tail == "" {
		httpreply.NotFound(w, "missing forge id")
		return
	}
	// First path segment is either "refresh" / "oauth" (handled
	// above by direct registration) or a forge ID. ID is "kind:host"
	// — the colon could be percent-encoded but we keep it literal.
	id, sub, _ := splitFirst(tail)
	if h.manager.Get(id) == nil {
		httpreply.NotFound(w, "unknown forge id")
		return
	}
	if sub == "" {
		switch r.Method {
		case http.MethodGet:
			f := h.manager.Get(id)
			webhttp.WriteJSON(w, f)
		case http.MethodDelete:
			h.handleDisconnect(w, r, id)
		default:
			httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodDelete)
		}
		return
	}
	op, rest, _ := splitFirst(sub)
	switch op {
	case "probe":
		h.handleProbe(w, r, id)
	case "login":
		h.handleLogin(w, r, id, rest)
	case "repos":
		h.handleRepos(w, r, id, rest)
	default:
		httpreply.NotFound(w, "unknown forge sub-resource")
	}
}

func (h *HTTPHandler) handleDisconnect(w http.ResponseWriter, r *http.Request, id string) {
	f := h.manager.Get(id)
	if f == nil {
		httpreply.NotFound(w, "unknown forge")
		return
	}
	if err := Logout(r.Context(), f.Kind, f.Host); err != nil {
		httpreply.ServerError(w, "disconnect failed", err)
		return
	}
	h.manager.Invalidate()
	_ = h.manager.Refresh(r.Context())
	h.notifyChanged(r.Context())
	webhttp.Ok(w)
}

func (h *HTTPHandler) handleProbe(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.probeTimeout)
	defer cancel()
	err := h.manager.Probe(ctx, id)
	f := h.manager.Get(id)
	if err != nil {
		webhttp.WriteJSONStatus(w, http.StatusOK, map[string]any{
			"connected": false,
			statusError: err.Error(),
			"forge":     f,
		})
		return
	}
	webhttp.WriteJSON(w, map[string]any{
		"connected": true,
		"forge":     f,
	})
}

// handleLogin handles POST /api/forges/{id}/login/pat — the user-
// supplied PAT path. id is the forge ID we're logging into; the
// kind+host are derived from id.
func (h *HTTPHandler) handleLogin(w http.ResponseWriter, r *http.Request, id, sub string) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	op, _, _ := splitFirst(sub)
	if op != "pat" {
		httpreply.NotFound(w, "unknown login method")
		return
	}
	kind, host := splitID(id)
	if !kind.Valid() {
		httpreply.BadRequest(w, "invalid forge id")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpreply.BadRequest(w, "invalid json")
		return
	}
	if err := LoginWithPAT(r.Context(), LoginPATParams{
		Kind:  kind,
		Host:  host,
		Token: body.Token,
	}); err != nil {
		// Return the validation failure as a 2xx {error} envelope (vibekit
		// convention): the client's apiPost/action layer collapses any non-2xx
		// to null → a generic "Network error.", hiding the real reason (bad
		// credentials, missing scope, wrong host). On a 2xx the PAT form's
		// inline error branch surfaces err. (Malformed JSON above stays 400.)
		webhttp.WriteJSON(w, httpreply.ErrorJSON(err.Error()))
		return
	}
	h.manager.Invalidate()
	_ = h.manager.Refresh(r.Context())
	h.notifyChanged(r.Context())
	webhttp.WriteJSON(w, map[string]string{"status": stateComplete})
}

// writeOpsError maps ForgeOps errors to HTTP status codes.
func (h *HTTPHandler) writeOpsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotInstalled):
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable,
			httpreply.ErrorJSONWithCode(err.Error(), string(ForgeErrCLINotInstalled)))
	case errors.Is(err, ErrNotLoggedIn):
		webhttp.WriteJSONStatus(w, http.StatusUnauthorized,
			httpreply.ErrorJSONWithCode(err.Error(), string(ForgeErrNotLoggedIn)))
	case errors.Is(err, ErrNotSupported):
		webhttp.WriteJSONStatus(w, http.StatusNotImplemented,
			httpreply.ErrorJSONWithCode(err.Error(), string(ForgeErrNotSupported)))
	default:
		slog.Debug("forges: ops error", "error", err)
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
			httpreply.ErrorJSON(err.Error()))
	}
}

// splitID parses "kind:host" → (kind, host). Returns (KindGitHub, "")
// for malformed input — the caller should validate Kind.Valid().
func splitID(id string) (kind Kind, ref string) {
	parts := strings.SplitN(id, ":", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return Kind(parts[0]), parts[1]
}

// splitFirst splits s at the first '/' separator, returning
// (head, tail, found). If '/' is not present, returns (s, "", false).
func splitFirst(s string) (head, tail string, found bool) {
	return strings.Cut(s, "/")
}
