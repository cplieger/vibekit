package mcp

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/logsafe"
	"github.com/cplieger/webhttp/v2"
)

// RegisterRoutes wires the MCP config endpoints.
//
//	GET    /api/mcp          → list (secrets masked)
//	POST   /api/mcp          → create
//	POST   /api/mcp/import   → create every server of a pasted README block
//	GET    /api/mcp/{id}     → one (secrets masked)
//	PUT    /api/mcp/{id}     → replace (preserves "***" values)
//	PATCH  /api/mcp/{id}     → toggle enabled: body {"enabled": bool}
//	DELETE /api/mcp/{id}     → remove
//
// `import` is its own route rather than a second body shape on POST /api/mcp:
// that endpoint decodes ONE vibekit record and answers with one, while a
// paste decodes a foreign shape and can name several servers.
func (s *Store) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/mcp", s.handleCollection)
	mux.HandleFunc("/api/mcp/import", s.handleImport)
	mux.HandleFunc("/api/mcp/", s.handleOne)
}

// handleImport handles POST /api/mcp/import: translate a pasted publisher block
// (or a single publisher-shaped server object) and create every server in it.
//
// 200 {"results":[{"name","outcome"}],"notes":[...]} on success. `notes` carries
// what the translation had to say about keys vibekit recognises and cannot
// store, and about a name it had to adjust — so an accepted `timeout` does not
// read as a silently-dropped field.
func (s *Store) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var raw json.RawMessage
	if !httpreply.DecodeJSON(w, r, &raw) {
		return
	}
	req, err := parseImportBody(raw)
	if err != nil {
		slog.Debug("mcp: import parse failed", "error", err)
		writeValidationErr(w, err)
		return
	}
	results, err := s.ImportServers(r.Context(), req.servers)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	webhttp.WriteJSON(w, map[string]any{"results": results, "notes": req.notes})
}

func (s *Store) handleCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		webhttp.WriteJSON(w, map[string]any{"servers": s.List(r.Context())})
	case http.MethodPost:
		var in Server
		if !httpreply.DecodeJSON(w, r, &in) {
			return
		}
		created, err := s.Create(r.Context(), &in)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		webhttp.WriteJSON(w, created)
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Store) handleOne(w http.ResponseWriter, r *http.Request) {
	// Mux registers "/api/mcp/" as this handler, so path.Base on the
	// bare route yields "mcp" which we explicitly reject. id == "" is
	// unreachable from path.Base on any real URL (it always returns
	// "." on empty input), so only the "mcp" case needs a guard.
	raw := path.Base(r.URL.Path)
	if raw == "mcp" {
		httpreply.NotFound(w, "server not found")
		return
	}
	id, err := ParseServerID(raw)
	if err != nil {
		httpreply.BadRequest(w, "invalid server id")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.writeOne(w, r, id)
	case http.MethodPut:
		s.putOne(w, r, id)
	case http.MethodPatch:
		s.patchOne(w, r, id)
	case http.MethodDelete:
		s.deleteOne(w, r, id)
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete)
	}
}

// writeOne handles GET /api/mcp/{id}: 200 with the masked record, or 404.
func (s *Store) writeOne(w http.ResponseWriter, r *http.Request, id ServerID) {
	got := s.Get(r.Context(), id)
	if got == nil {
		httpreply.NotFound(w, "server not found")
		return
	}
	webhttp.WriteJSON(w, got)
}

// putOne handles PUT /api/mcp/{id}: replace the record (preserving "***"
// secret values), or map the store error to its status via writeErr.
func (s *Store) putOne(w http.ResponseWriter, r *http.Request, id ServerID) {
	var in Server
	if !httpreply.DecodeJSON(w, r, &in) {
		return
	}
	updated, err := s.Update(r.Context(), id, &in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	webhttp.WriteJSON(w, updated)
}

// patchOne handles PATCH /api/mcp/{id} with body {"enabled": bool}: a
// missing enabled field is a 400; otherwise toggle and return the record.
func (s *Store) patchOne(w http.ResponseWriter, r *http.Request, id ServerID) {
	var patch struct {
		Enabled *bool `json:"enabled"`
	}
	if !httpreply.DecodeJSON(w, r, &patch) {
		return
	}
	if patch.Enabled == nil {
		slog.Debug("mcp: http patch missing enabled field",
			"path", logsafe.Field(r.URL.Path))
		httpreply.BadRequest(w, "enabled required")
		return
	}
	updated, err := s.SetEnabled(r.Context(), id, *patch.Enabled)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	webhttp.WriteJSON(w, updated)
}

// deleteOne handles DELETE /api/mcp/{id}: 200 ok, or map the store error
// to its status via writeErr.
func (s *Store) deleteOne(w http.ResponseWriter, r *http.Request, id ServerID) {
	if err := s.Delete(r.Context(), id); err != nil {
		s.writeErr(w, err)
		return
	}
	webhttp.Ok(w)
}

// writeErr maps package-level sentinel errors to the right HTTP status.
// ErrPersist (filesystem failure) becomes 500 with a generic body so
// the browser never sees raw filesystem paths / errnos; full detail
// lands in slog at Error level for ops.
func (*Store) writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		slog.Debug("mcp: http not found", "error", err)
		httpreply.NotFound(w, err.Error())
	case errors.Is(err, ErrNameConflict):
		slog.Debug("mcp: http name conflict", "error", err)
		httpreply.Conflict(w, err.Error())
	case errors.Is(err, ErrPersist):
		if errors.Is(err, ErrPersistMarshal) {
			slog.Error("mcp: http persist marshal failure (programmer bug)", "error", err)
		} else {
			slog.Warn("mcp: http persist write failure (infra)", "error", err)
		}
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
			httpreply.ErrorJSON("persist failed"))
	default:
		slog.Debug("mcp: http bad request", "error", err)
		writeValidationErr(w, err)
	}
}

// validationErrorBody is the 400 a failed Validate produces.
//
// `error` is the joined message text, so a client reading only that field
// keeps working unchanged. `fields` is the addition: one entry per
// failure, carrying the wire field name so the form can mark inputs
// individually.
type validationErrorBody struct {
	Error  string       `json:"error"`
	Fields []FieldError `json:"fields,omitempty"`
}

// writeValidationErr answers a 400, carrying the per-field breakdown when the
// error has one. An error with no field attribution (a paste parse failure, a
// store precondition) gets the plain envelope, so a field list on the wire always
// means "these inputs are wrong" rather than "here is an empty array".
func writeValidationErr(w http.ResponseWriter, err error) {
	fields := FieldErrors(err)
	if len(fields) == 0 {
		httpreply.BadRequest(w, err.Error())
		return
	}
	webhttp.WriteJSONStatus(w, http.StatusBadRequest,
		validationErrorBody{Error: err.Error(), Fields: fields})
}
