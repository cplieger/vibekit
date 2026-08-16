package forges

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/cplieger/vibekit/internal/api"
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
			api.MethodNotAllowed(w, http.MethodGet)
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
	owner, after, ok := splitFirst(rest)
	if !ok || owner == "" {
		api.BadRequest(w, "missing repo owner")
		return
	}
	name, after2, _ := splitFirst(after)
	if name == "" {
		api.BadRequest(w, "missing repo name")
		return
	}
	repo := owner + "/" + name
	if after2 == "" {
		api.BadRequest(w, "missing sub-resource (prs/issues/checks/releases/labels)")
		return
	}
	op, tail, _ := splitFirst(after2)
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
		h.handlePRCollection(w, r, p, repo)
		return
	}
	h.handlePRAction(w, r, p, repo, tail)
}

// handlePRCollection serves the repo-level PR endpoints: list (GET) and
// create (POST).
func (h *HTTPHandler) handlePRCollection(w http.ResponseWriter, r *http.Request, p ForgeOps, repo string) {
	switch r.Method {
	case http.MethodGet:
		state := ListState(r.URL.Query().Get("state"))
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
		api.MethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

// handlePRAction serves the per-PR action endpoints: merge, close,
// reopen and rerun. All four are POST-only.
func (h *HTTPHandler) handlePRAction(w http.ResponseWriter, r *http.Request, p ForgeOps, repo, tail string) {
	numStr, op, _ := splitFirst(tail)
	number, err := strconv.Atoi(numStr)
	if err != nil {
		api.BadRequest(w, "invalid PR number")
		return
	}
	if r.Method != http.MethodPost {
		api.MethodNotAllowed(w, http.MethodPost)
		return
	}
	switch op {
	case "merge":
		h.handlePRMerge(w, r, p, repo, number)
	case stateClose:
		h.writeOpResult(w, p.ClosePR(r.Context(), repo, number))
	case "reopen":
		h.writeOpResult(w, p.ReopenPR(r.Context(), repo, number))
	case "rerun":
		h.handlePRRerun(w, r, p, repo, number)
	default:
		api.NotFound(w, "unknown PR action")
	}
}

// handlePRMerge merges a PR. The merge strategy, the head-commit pin and
// the auto-merge arm all travel as query parameters, following the
// ?method= convention this route already carried.
func (h *HTTPHandler) handlePRMerge(w http.ResponseWriter, r *http.Request, p ForgeOps, repo string, number int) {
	q := r.URL.Query()
	headSHA, ok := headPinOrBadRequest(w, q.Get(fieldHeadSHA))
	if !ok {
		return
	}
	opts := MergeOptions{
		Method:  MergeMethod(q.Get(fieldMethod)),
		HeadSHA: headSHA,
		Auto:    queryTrue(q.Get(fieldAuto)),
	}
	h.writeOpResult(w, p.MergePR(r.Context(), repo, number, opts))
}

// handlePRRerun re-runs a PR's failed CI, pinned to the head commit the
// caller's row displayed.
//
// The pin travels and is validated exactly as the merge's does, because it
// serves the same purpose on an action with comparable consequences: a re-run
// can trigger a deployment, so acting on a commit other than the one whose red
// status the caller was looking at is a wrong action rather than a slow one.
func (h *HTTPHandler) handlePRRerun(w http.ResponseWriter, r *http.Request, p ForgeOps, repo string, number int) {
	headSHA, ok := headPinOrBadRequest(w, r.URL.Query().Get(fieldHeadSHA))
	if !ok {
		return
	}
	h.writeOpResult(w, p.RerunFailedChecks(r.Context(), repo, number, headSHA))
}

// headPinOrBadRequest validates a head_sha query parameter, answering 400 and
// returning false when it is present but not an object id. Absent stays legal:
// a forge that reported no head SHA leaves the action unpinned.
//
// The pin reaches a subprocess argv and a JSON body, so it is checked at this
// one boundary rather than at each provider.
func headPinOrBadRequest(w http.ResponseWriter, raw string) (headSHA string, ok bool) {
	if raw != "" && !isHexSHA(raw) {
		api.BadRequest(w, "invalid head_sha")
		return "", false
	}
	return raw, true
}

// writeOpResult answers a mutation: 200 {"ok":true} or the mapped error.
func (h *HTTPHandler) writeOpResult(w http.ResponseWriter, err error) {
	if err != nil {
		h.writeOpsError(w, err)
		return
	}
	api.Ok(w)
}

// queryTrue reads a boolean query parameter. Both spellings the client
// could plausibly send count as true; everything else is false.
func queryTrue(v string) bool {
	return v == "1" || v == "true"
}

func (h *HTTPHandler) handleIssues(w http.ResponseWriter, r *http.Request, p ForgeOps, repo, tail string) {
	if tail == "" {
		h.handleIssueCollection(w, r, p, repo)
		return
	}
	h.handleIssueAction(w, r, p, repo, tail)
}

// handleIssueCollection serves the repo-level issue endpoints: list (GET)
// and create (POST).
func (h *HTTPHandler) handleIssueCollection(w http.ResponseWriter, r *http.Request, p ForgeOps, repo string) {
	switch r.Method {
	case http.MethodGet:
		state := ListState(r.URL.Query().Get("state"))
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
		api.MethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

// handleIssueAction serves the per-issue action endpoints: close.
func (h *HTTPHandler) handleIssueAction(w http.ResponseWriter, r *http.Request, p ForgeOps, repo, tail string) {
	numStr, op, _ := splitFirst(tail)
	number, err := strconv.Atoi(numStr)
	if err != nil {
		api.BadRequest(w, "invalid issue number")
		return
	}
	if op == stateClose {
		if r.Method != http.MethodPost {
			api.MethodNotAllowed(w, http.MethodPost)
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
		api.MethodNotAllowed(w, http.MethodGet)
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
		api.MethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (h *HTTPHandler) handleLabels(w http.ResponseWriter, r *http.Request, p ForgeOps, repo string) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w, http.MethodGet)
		return
	}
	labels, err := p.ListLabels(r.Context(), repo)
	if err != nil {
		h.writeOpsError(w, err)
		return
	}
	api.WriteJSON(w, map[string]any{"labels": labels})
}
