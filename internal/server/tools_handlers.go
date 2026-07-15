// Tools API v2 — thin HTTP layer over internal/tools.Engine.
//
// The engine owns the manifest (tools.json v2), the machine state, the
// job queue, and the compiled catalog; handlers translate HTTP. Long
// work never runs on a request: mutating endpoints enqueue a job and
// return 202 with the job view, output streams over the SSE events
// tool_job_output / tool_job_changed.
//
//	GET    /api/tools                  manifest + install state + active job
//	POST   /api/tools                  add a tool (catalog- or self-described) -> 202 job
//	PATCH  /api/tools/{name}           merge fields; version change -> 202 job
//	DELETE /api/tools/{name}           remove (409 + dependents without force) -> 202 job
//	POST   /api/tools/{name}/install   (re)install at the manifest version -> 202 job
//	POST   /api/tools/update           update-all unpinned -> 202 job
//	GET    /api/tools/search?q=        catalog search (empty q = featured set)
//	GET    /api/tools/jobs             active job (with output tail) + recent history
//	POST   /api/tools/jobs/{id}/cancel abort a queued/running job
//	GET    /api/tools/status           bare PATH probes for feature-gating UIs
package server

import (
	"encoding/json"
	"net/http"
	"os/exec"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/tools"
)

const maxToolsBody = 16 << 10

// statusBinaries is the set of binaries /api/tools/status probes. Each
// key is a name kiro-cli or a vibekit feature panel expects on PATH
// (the MCP add modal gates on node/npx/uv, Sources on the forge CLIs).
var statusBinaries = []string{
	"node", "npm", "npx",
	"go", "gofmt",
	"java",
	"cargo", "rustc",
	"uv", "uvx",
	"gh", "glab", "tea",
	"typescript-language-server", "tsc",
	"pyright", "pyrefly",
	"gopls", "rust-analyzer", "clangd",
	"jdtls", "kotlin-language-server",
}

// handleToolsList: GET /api/tools
func (s *Server) handleToolsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w)
		return
	}
	res, err := s.tools.List()
	if err != nil {
		api.InternalError(w, err)
		return
	}
	api.WriteJSON(w, res)
}

// handleToolsCreate: POST /api/tools
func (s *Server) handleToolsCreate(w http.ResponseWriter, r *http.Request) {
	var req tools.CreateRequest
	if !decodeBody(w, r, &req) {
		return
	}
	job, err := s.tools.Create(r.Context(), req)
	if err != nil {
		api.BadRequest(w, err.Error())
		return
	}
	api.WriteJSONStatus(w, http.StatusAccepted, api.ToolJobAccepted{Job: job})
}

// handleToolPatch: PATCH /api/tools/{name}
func (s *Server) handleToolPatch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req tools.PatchRequest
	if !decodeBody(w, r, &req) {
		return
	}
	job, err := s.tools.Patch(name, req)
	switch {
	case tools.IsNotFound(err):
		api.NotFound(w, "unknown tool")
		return
	case err != nil:
		api.BadRequest(w, err.Error())
		return
	case job != nil:
		api.WriteJSONStatus(w, http.StatusAccepted, api.ToolJobAccepted{Job: job})
		return
	}
	api.Ok(w)
}

// handleToolDelete: DELETE /api/tools/{name}
func (s *Server) handleToolDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Force bool `json:"force"`
	}
	if r.ContentLength > 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxToolsBody))
		if err := dec.Decode(&body); err != nil {
			// A malformed body must NOT silently demote force:true to
			// a plain delete (or vice versa).
			api.BadRequest(w, "invalid body")
			return
		}
	}
	job, dependents, err := s.tools.Delete(name, body.Force)
	switch {
	case tools.IsNotFound(err):
		api.NotFound(w, "unknown tool")
		return
	case tools.IsHasDependents(err):
		api.WriteJSONStatus(w, http.StatusConflict, map[string]any{
			"error":      "tool is required by others",
			"code":       "has_dependents",
			"dependents": dependents,
		})
		return
	case err != nil:
		api.InternalError(w, err)
		return
	}
	api.WriteJSONStatus(w, http.StatusAccepted, api.ToolJobAccepted{Job: job})
}

// handleToolInstallOne: POST /api/tools/{name}/install
func (s *Server) handleToolInstallOne(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	job, err := s.tools.InstallOne(name)
	switch {
	case tools.IsNotFound(err):
		api.NotFound(w, "unknown tool")
		return
	case err != nil:
		api.InternalError(w, err)
		return
	}
	api.WriteJSONStatus(w, http.StatusAccepted, api.ToolJobAccepted{Job: job})
}

// handleToolsUpdate: POST /api/tools/update
func (s *Server) handleToolsUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Names []string `json:"names"`
	}
	if r.ContentLength > 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxToolsBody))
		if err := dec.Decode(&body); err != nil {
			// A malformed per-tool update must NOT silently become a
			// full update-all run.
			api.BadRequest(w, "invalid body")
			return
		}
	}
	job, err := s.tools.UpdateAll(body.Names)
	if err != nil {
		api.InternalError(w, err)
		return
	}
	api.WriteJSONStatus(w, http.StatusAccepted, api.ToolJobAccepted{Job: job})
}

// handleToolsSearch: GET /api/tools/search?q=
func (s *Server) handleToolsSearch(w http.ResponseWriter, r *http.Request) {
	results := s.tools.Search(r.URL.Query().Get("q"))
	// Strip the embedded aqua definitions: the client needs the
	// display fields, not the install internals.
	out := make([]api.ToolCatalogHit, 0, len(results))
	for _, e := range results {
		out = append(out, api.ToolCatalogHit{
			Name:        e.Name,
			Description: e.Description,
			Source:      e.Source,
			Featured:    e.Featured,
			Version:     e.Version,
		})
	}
	api.WriteJSON(w, api.ToolsSearchResponse{Results: out})
}

// handleToolsJobs: GET /api/tools/jobs
func (s *Server) handleToolsJobs(w http.ResponseWriter, _ *http.Request) {
	active, recent := s.tools.Jobs()
	api.WriteJSON(w, api.ToolsJobsResponse{Active: active, Recent: recent})
}

// handleToolsJobCancel: POST /api/tools/jobs/{id}/cancel
func (s *Server) handleToolsJobCancel(w http.ResponseWriter, r *http.Request) {
	if !s.tools.CancelJob(r.PathValue("id")) {
		api.NotFound(w, "unknown job")
		return
	}
	api.Ok(w)
}

// handleToolStatus: GET /api/tools/status
//
// Bare PATH presence probes for the well-known binaries feature panels
// gate on (e.g. the MCP modal's "Setting up Node..." spinner).
func (s *Server) handleToolStatus(w http.ResponseWriter, _ *http.Request) {
	out := make(map[string]bool, len(statusBinaries))
	for _, b := range statusBinaries {
		_, err := exec.LookPath(b)
		out[b] = err == nil
	}
	api.WriteJSON(w, out)
}
