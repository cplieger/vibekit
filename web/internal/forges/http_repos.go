package forges

import (
	"encoding/json"
	"net/http"
	"strconv"

	"vibekit/internal/api"
)

// handleRepos dispatches /api/forges/{id}/repos/* paths.
func (h *HTTPHandler) handleRepos(w http.ResponseWriter, r *http.Request, id, rest string) {
	provider, err := h.manager.Provider(id)
	if err != nil {
		api.NotFound(w, err.Error())
		return
	}
	// rest is "" → list repos. Otherwise it's "owner/name[/sub...]".
	if rest == "" {
		if r.Method != http.MethodGet {
			api.MethodNotAllowed(w)
			return
		}
		repos, err := provider.ListRepos(r.Context())
		if err != nil {
			h.writeOpsError(w, err)
			return
		}
		api.WriteJSON(w, map[string]any{"repos": repos})
		return
	}
	owner, after, ok := splitFirst(rest, "/")
	if !ok || owner == "" {
		api.BadRequest(w, "missing repo owner")
		return
	}
	name, after2, _ := splitFirst(after, "/")
	if name == "" {
		api.BadRequest(w, "missing repo name")
		return
	}
	repo := owner + "/" + name
	if after2 == "" {
		api.BadRequest(w, "missing sub-resource (prs/issues/checks/releases/labels)")
		return
	}
	op, tail, _ := splitFirst(after2, "/")
	switch op {
	case "prs":
		h.handlePRs(w, r, provider, repo, tail)
	case "issues":
		h.handleIssues(w, r, provider, repo, tail)
	case "checks":
		h.handleChecks(w, r, provider, repo)
	case "releases":
		h.handleReleases(w, r, provider, repo)
	case "labels":
		h.handleLabels(w, r, provider, repo)
	default:
		api.NotFound(w, "unknown repo sub-resource")
	}
}

func (h *HTTPHandler) handlePRs(w http.ResponseWriter, r *http.Request, p ForgeOps, repo, tail string) {
	if tail == "" {
		switch r.Method {
		case http.MethodGet:
			state := r.URL.Query().Get("state")
			prs, err := p.ListPRs(r.Context(), repo, state)
			if err != nil {
				h.writeOpsError(w, err)
				return
			}
			api.WriteJSON(w, map[string]any{"prs": prs})
		case http.MethodPost:
			var params CreatePRParams
			api.LimitBody(w, r, api.MaxJSONBody)
			if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
				api.BadRequest(w, "invalid json")
				return
			}
			pr, err := p.CreatePR(r.Context(), repo, &params)
			if err != nil {
				h.writeOpsError(w, err)
				return
			}
			api.WriteJSON(w, pr)
		default:
			api.MethodNotAllowed(w)
		}
		return
	}
	numStr, op, _ := splitFirst(tail, "/")
	number, err := strconv.Atoi(numStr)
	if err != nil {
		api.BadRequest(w, "invalid PR number")
		return
	}
	switch op {
	case "merge":
		if r.Method != http.MethodPost {
			api.MethodNotAllowed(w)
			return
		}
		method := r.URL.Query().Get(fieldMethod)
		if err := p.MergePR(r.Context(), repo, number, method); err != nil {
			h.writeOpsError(w, err)
			return
		}
		api.Ok(w)
	case stateClose:
		if r.Method != http.MethodPost {
			api.MethodNotAllowed(w)
			return
		}
		if err := p.ClosePR(r.Context(), repo, number); err != nil {
			h.writeOpsError(w, err)
			return
		}
		api.Ok(w)
	default:
		api.NotFound(w, "unknown PR action")
	}
}

func (h *HTTPHandler) handleIssues(w http.ResponseWriter, r *http.Request, p ForgeOps, repo, tail string) {
	if tail == "" {
		switch r.Method {
		case http.MethodGet:
			state := r.URL.Query().Get("state")
			issues, err := p.ListIssues(r.Context(), repo, state)
			if err != nil {
				h.writeOpsError(w, err)
				return
			}
			api.WriteJSON(w, map[string]any{"issues": issues})
		case http.MethodPost:
			var params CreateIssueParams
			api.LimitBody(w, r, api.MaxJSONBody)
			if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
				api.BadRequest(w, "invalid json")
				return
			}
			issue, err := p.CreateIssue(r.Context(), repo, params)
			if err != nil {
				h.writeOpsError(w, err)
				return
			}
			api.WriteJSON(w, issue)
		default:
			api.MethodNotAllowed(w)
		}
		return
	}
	numStr, op, _ := splitFirst(tail, "/")
	number, err := strconv.Atoi(numStr)
	if err != nil {
		api.BadRequest(w, "invalid issue number")
		return
	}
	if op == stateClose {
		if r.Method != http.MethodPost {
			api.MethodNotAllowed(w)
			return
		}
		if err := p.CloseIssue(r.Context(), repo, number); err != nil {
			h.writeOpsError(w, err)
			return
		}
		api.Ok(w)
		return
	}
	api.NotFound(w, "unknown issue action")
}

func (h *HTTPHandler) handleChecks(w http.ResponseWriter, r *http.Request, p ForgeOps, repo string) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w)
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		api.BadRequest(w, "ref query parameter required")
		return
	}
	checks, err := p.CommitStatus(r.Context(), repo, ref)
	if err != nil {
		h.writeOpsError(w, err)
		return
	}
	api.WriteJSON(w, map[string]any{"checks": checks})
}

func (h *HTTPHandler) handleReleases(w http.ResponseWriter, r *http.Request, p ForgeOps, repo string) {
	switch r.Method {
	case http.MethodGet:
		releases, err := p.ListReleases(r.Context(), repo)
		if err != nil {
			h.writeOpsError(w, err)
			return
		}
		api.WriteJSON(w, map[string]any{"releases": releases})
	case http.MethodPost:
		var params CreateReleaseParams
		api.LimitBody(w, r, api.MaxJSONBody)
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			api.BadRequest(w, "invalid json")
			return
		}
		release, err := p.CreateRelease(r.Context(), repo, params)
		if err != nil {
			h.writeOpsError(w, err)
			return
		}
		api.WriteJSON(w, release)
	default:
		api.MethodNotAllowed(w)
	}
}

func (h *HTTPHandler) handleLabels(w http.ResponseWriter, r *http.Request, p ForgeOps, repo string) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w)
		return
	}
	labels, err := p.ListLabels(r.Context(), repo)
	if err != nil {
		h.writeOpsError(w, err)
		return
	}
	api.WriteJSON(w, map[string]any{"labels": labels})
}
