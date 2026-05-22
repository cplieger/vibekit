package mcp

import (
	"errors"
	"log/slog"
	"net/http"
	"path"

	"vibekit/internal/api"
)

// RegisterRoutes wires the MCP config endpoints.
//
//	GET    /api/mcp          → list (secrets masked)
//	POST   /api/mcp          → create
//	GET    /api/mcp/{id}     → one (secrets masked)
//	PUT    /api/mcp/{id}     → replace (preserves "***" values)
//	PATCH  /api/mcp/{id}     → toggle enabled: body {"enabled": bool}
//	DELETE /api/mcp/{id}     → remove
func (s *Store) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/mcp", s.handleCollection)
	mux.HandleFunc("/api/mcp/", s.handleOne)
}

func (s *Store) handleCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.WriteJSON(w, map[string]any{"servers": s.List(r.Context())})
	case http.MethodPost:
		var in Server
		if !decodeJSONBody(w, r, &in) {
			return
		}
		created, err := s.Create(r.Context(), &in)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		api.WriteJSON(w, created)
	default:
		api.MethodNotAllowed(w)
	}
}

func (s *Store) handleOne(w http.ResponseWriter, r *http.Request) {
	// Mux registers "/api/mcp/" as this handler, so path.Base on the
	// bare route yields "mcp" which we explicitly reject. id == "" is
	// unreachable from path.Base on any real URL (it always returns
	// "." on empty input), so only the "mcp" case needs a guard.
	raw := path.Base(r.URL.Path)
	if raw == "mcp" {
		api.NotFound(w, "server not found")
		return
	}
	id, err := ParseServerID(raw)
	if err != nil {
		api.BadRequest(w, "invalid server id")
		return
	}
	switch r.Method {
	case http.MethodGet:
		got := s.Get(r.Context(), id)
		if got == nil {
			api.NotFound(w, "server not found")
			return
		}
		api.WriteJSON(w, got)
	case http.MethodPut:
		var in Server
		if !decodeJSONBody(w, r, &in) {
			return
		}
		updated, err := s.Update(r.Context(), id, &in)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		api.WriteJSON(w, updated)
	case http.MethodPatch:
		var patch struct {
			Enabled *bool `json:"enabled"`
		}
		if !decodeJSONBody(w, r, &patch) {
			return
		}
		if patch.Enabled == nil {
			slog.Debug("mcp: http patch missing enabled field",
				"path", r.URL.Path)
			api.BadRequest(w, "enabled required")
			return
		}
		updated, err := s.SetEnabled(r.Context(), id, *patch.Enabled)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		api.WriteJSON(w, updated)
	case http.MethodDelete:
		if err := s.Delete(r.Context(), id); err != nil {
			s.writeErr(w, err)
			return
		}
		api.Ok(w)
	default:
		api.MethodNotAllowed(w)
	}
}

// decodeJSONBody delegates to api.DecodeJSON for backward compatibility
// within this package. Existing call sites use the local name.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	return api.DecodeJSON(w, r, v)
}

// writeErr maps package-level sentinel errors to the right HTTP status.
// ErrPersist (filesystem failure) becomes 500 with a generic body so
// the browser never sees raw filesystem paths / errnos; full detail
// lands in slog at Error level for ops.
func (*Store) writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		slog.Debug("mcp: http not found", "error", err)
		api.NotFound(w, err.Error())
	case errors.Is(err, ErrNameConflict):
		slog.Debug("mcp: http name conflict", "error", err)
		api.Conflict(w, err.Error())
	case errors.Is(err, ErrPersist):
		if errors.Is(err, ErrPersistMarshal) {
			slog.Error("mcp: http persist marshal failure (programmer bug)", "error", err)
		} else {
			slog.Warn("mcp: http persist write failure (infra)", "error", err)
		}
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			map[string]string{"error": "persist failed"})
	default:
		slog.Debug("mcp: http bad request", "error", err)
		api.BadRequest(w, err.Error())
	}
}
