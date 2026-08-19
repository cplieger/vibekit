package forges

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/cplieger/vibekit/internal/httpreply"
)

// repoLister is what handleRepos needs at the collection level: the account's
// own repositories. Declared here rather than in provider.go because this is the
// one function that asks for them.
type repoLister interface {
	// ListRepos returns repositories accessible to the authenticated
	// account (owned + member).
	ListRepos(ctx context.Context) ([]Repo, error)
}

// handleRepos dispatches /api/forges/{id}/repos/* paths.
func (h *HTTPHandler) handleRepos(w http.ResponseWriter, r *http.Request, id, rest string) {
	provider, err := h.manager.Provider(id)
	if err != nil {
		httpreply.NotFound(w, err.Error())
		return
	}
	// rest is "" → list repos. Otherwise it's "owner/name[/sub...]".
	if rest == "" {
		if r.Method != http.MethodGet {
			httpreply.MethodNotAllowed(w, http.MethodGet)
			return
		}
		repos, err := provider.ListRepos(r.Context())
		if err != nil {
			h.writeOpsError(w, err)
			return
		}
		httpreply.WriteJSON(w, map[string]any{"repos": repos})
		return
	}
	owner, after, ok := splitFirst(rest)
	if !ok || owner == "" {
		httpreply.BadRequest(w, "missing repo owner")
		return
	}
	name, after2, _ := splitFirst(after)
	if name == "" {
		httpreply.BadRequest(w, "missing repo name")
		return
	}
	repo := owner + "/" + name
	if after2 == "" {
		httpreply.BadRequest(w, "missing sub-resource (prs/issues/checks/releases/labels)")
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
		httpreply.NotFound(w, "unknown repo sub-resource")
	}
}

// prOps is the pull-request surface: everything under
// /api/forges/{id}/repos/{owner}/{name}/prs and nothing else. Six methods, which
// is what handlePRs and its four action helpers actually reach for.
type prOps interface {
	// ListPRs lists pull/merge requests for repo.
	ListPRs(ctx context.Context, repo string, state ListState) ([]PR, error)

	// CreatePR opens a new pull/merge request.
	CreatePR(ctx context.Context, repo string, p *CreatePRParams) (*PR, error)

	// MergePR merges an open PR, or arms the forge's own auto-merge
	// when opts.Auto is set.
	MergePR(ctx context.Context, repo string, number int, opts MergeOptions) error

	// ClosePR closes (without merging) an open PR.
	ClosePR(ctx context.Context, repo string, number int) error

	// ReopenPR reopens a closed PR.
	ReopenPR(ctx context.Context, repo string, number int) error

	// RerunFailedChecks re-runs the failed CI of a PR's CURRENT head.
	// Returns an ErrNotSupported-wrapped error on a forge with no re-run
	// verb.
	//
	// headSHA is the commit the caller's row was rendered from, and it is
	// a PRECONDITION exactly like MergeOptions.HeadSHA: an implementation
	// must refuse rather than re-run when the PR has moved since, because
	// a re-run can carry deployment side effects and a row displaying one
	// commit's red status must not act on another's. Empty means the
	// caller had no head to pin (the forge reported none), and the re-run
	// proceeds against whatever the PR's head is now.
	RerunFailedChecks(ctx context.Context, repo string, number int, headSHA string) error
}

func (h *HTTPHandler) handlePRs(w http.ResponseWriter, r *http.Request, p prOps, repo, tail string) {
	if tail == "" {
		h.handlePRCollection(w, r, p, repo)
		return
	}
	h.handlePRAction(w, r, p, repo, tail)
}

// handlePRCollection serves the repo-level PR endpoints: list (GET) and
// create (POST).
func (h *HTTPHandler) handlePRCollection(w http.ResponseWriter, r *http.Request, p prOps, repo string) {
	switch r.Method {
	case http.MethodGet:
		state := ListState(r.URL.Query().Get("state"))
		prs, err := p.ListPRs(r.Context(), repo, state)
		if err != nil {
			h.writeOpsError(w, err)
			return
		}
		httpreply.WriteJSON(w, map[string]any{"prs": prs})
	case http.MethodPost:
		var params CreatePRParams
		httpreply.LimitBody(w, r, httpreply.MaxJSONBody)
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			httpreply.BadRequest(w, "invalid json")
			return
		}
		pr, err := p.CreatePR(r.Context(), repo, &params)
		if err != nil {
			h.writeOpsError(w, err)
			return
		}
		httpreply.WriteJSON(w, pr)
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

// handlePRAction serves the per-PR action endpoints: merge, close,
// reopen and rerun. All four are POST-only.
func (h *HTTPHandler) handlePRAction(w http.ResponseWriter, r *http.Request, p prOps, repo, tail string) {
	numStr, op, _ := splitFirst(tail)
	number, err := strconv.Atoi(numStr)
	if err != nil {
		httpreply.BadRequest(w, "invalid PR number")
		return
	}
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
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
		httpreply.NotFound(w, "unknown PR action")
	}
}

// handlePRMerge merges a PR. The merge strategy, the head-commit pin and
// the auto-merge arm all travel as query parameters, following the
// ?method= convention this route already carried.
func (h *HTTPHandler) handlePRMerge(w http.ResponseWriter, r *http.Request, p prOps, repo string, number int) {
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
func (h *HTTPHandler) handlePRRerun(w http.ResponseWriter, r *http.Request, p prOps, repo string, number int) {
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
		httpreply.BadRequest(w, "invalid head_sha")
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
	httpreply.Ok(w)
}

// queryTrue reads a boolean query parameter. Both spellings the client
// could plausibly send count as true; everything else is false.
func queryTrue(v string) bool {
	return v == "1" || v == "true"
}

// issueOps is the issue surface: three methods, and the row the C19 audit
// measured — handleIssues used to take all fifteen to call these.
type issueOps interface {
	// ListIssues lists issues for repo.
	ListIssues(ctx context.Context, repo string, state ListState) ([]Issue, error)

	// CreateIssue files a new issue.
	CreateIssue(ctx context.Context, repo string, p CreateIssueParams) (*Issue, error)

	// CloseIssue closes an open issue.
	CloseIssue(ctx context.Context, repo string, number int) error
}

func (h *HTTPHandler) handleIssues(w http.ResponseWriter, r *http.Request, p issueOps, repo, tail string) {
	if tail == "" {
		h.handleIssueCollection(w, r, p, repo)
		return
	}
	h.handleIssueAction(w, r, p, repo, tail)
}

// handleIssueCollection serves the repo-level issue endpoints: list (GET)
// and create (POST).
func (h *HTTPHandler) handleIssueCollection(w http.ResponseWriter, r *http.Request, p issueOps, repo string) {
	switch r.Method {
	case http.MethodGet:
		state := ListState(r.URL.Query().Get("state"))
		issues, err := p.ListIssues(r.Context(), repo, state)
		if err != nil {
			h.writeOpsError(w, err)
			return
		}
		httpreply.WriteJSON(w, map[string]any{"issues": issues})
	case http.MethodPost:
		var params CreateIssueParams
		httpreply.LimitBody(w, r, httpreply.MaxJSONBody)
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			httpreply.BadRequest(w, "invalid json")
			return
		}
		issue, err := p.CreateIssue(r.Context(), repo, params)
		if err != nil {
			h.writeOpsError(w, err)
			return
		}
		httpreply.WriteJSON(w, issue)
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

// handleIssueAction serves the per-issue action endpoints: close.
func (h *HTTPHandler) handleIssueAction(w http.ResponseWriter, r *http.Request, p issueOps, repo, tail string) {
	numStr, op, _ := splitFirst(tail)
	number, err := strconv.Atoi(numStr)
	if err != nil {
		httpreply.BadRequest(w, "invalid issue number")
		return
	}
	if op == stateClose {
		if r.Method != http.MethodPost {
			httpreply.MethodNotAllowed(w, http.MethodPost)
			return
		}
		if err := p.CloseIssue(r.Context(), repo, number); err != nil {
			h.writeOpsError(w, err)
			return
		}
		httpreply.Ok(w)
		return
	}
	httpreply.NotFound(w, "unknown issue action")
}

// checkOps is the CI-status read: one method, one handler.
type checkOps interface {
	// CommitStatus returns CI checks for a commit ref (branch / SHA).
	CommitStatus(ctx context.Context, repo, ref string) ([]Check, error)
}

func (h *HTTPHandler) handleChecks(w http.ResponseWriter, r *http.Request, p checkOps, repo string) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		httpreply.BadRequest(w, "ref query parameter required")
		return
	}
	checks, err := p.CommitStatus(r.Context(), repo, ref)
	if err != nil {
		h.writeOpsError(w, err)
		return
	}
	httpreply.WriteJSON(w, map[string]any{"checks": checks})
}

// releaseOps is the release surface: list and cut.
type releaseOps interface {
	// ListReleases returns recent releases for repo.
	ListReleases(ctx context.Context, repo string) ([]Release, error)

	// CreateRelease cuts a new release.
	CreateRelease(ctx context.Context, repo string, p CreateReleaseParams) (*Release, error)
}

func (h *HTTPHandler) handleReleases(w http.ResponseWriter, r *http.Request, p releaseOps, repo string) {
	switch r.Method {
	case http.MethodGet:
		releases, err := p.ListReleases(r.Context(), repo)
		if err != nil {
			h.writeOpsError(w, err)
			return
		}
		httpreply.WriteJSON(w, map[string]any{"releases": releases})
	case http.MethodPost:
		var params CreateReleaseParams
		httpreply.LimitBody(w, r, httpreply.MaxJSONBody)
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			httpreply.BadRequest(w, "invalid json")
			return
		}
		release, err := p.CreateRelease(r.Context(), repo, params)
		if err != nil {
			h.writeOpsError(w, err)
			return
		}
		httpreply.WriteJSON(w, release)
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

// labelOps is the label read: one method, one handler.
type labelOps interface {
	// ListLabels returns labels defined on repo.
	ListLabels(ctx context.Context, repo string) ([]Label, error)
}

func (h *HTTPHandler) handleLabels(w http.ResponseWriter, r *http.Request, p labelOps, repo string) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	labels, err := p.ListLabels(r.Context(), repo)
	if err != nil {
		h.writeOpsError(w, err)
		return
	}
	httpreply.WriteJSON(w, map[string]any{"labels": labels})
}
