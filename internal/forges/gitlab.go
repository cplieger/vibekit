// GitLab ForgeOps implementation backed by the glab CLI.
//
// glab supports per-host config; we pass --hostname for cloud GitLab
// and self-hosted instances. Most subcommands accept --output json.

package forges

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// gitlabProvider implements ForgeOps via the glab CLI.
type gitlabProvider struct {
	host string
}

// gitlabMR is the wire struct for GitLab merge request JSON responses.
// Shared between ListPRs (array) and viewPR (single object).
//
// DetailedMergeStatus is the field that carries both the CI verdict and
// the merge-block cause. head_pipeline is deliberately NOT read: GitLab
// documents it on the single-MR response only, so a list-shaped decode
// of it is empty on every row, and fetching it per PR is the N-call
// fan-out this work exists to avoid.
type gitlabMR struct {
	Title               string                    `json:"title"`
	Description         string                    `json:"description"`
	State               string                    `json:"state"`
	Author              struct{ Username string } `json:"author"`
	SourceBranch        string                    `json:"source_branch"`
	TargetBranch        string                    `json:"target_branch"`
	WebURL              string                    `json:"web_url"`
	CreatedAt           string                    `json:"created_at"`
	UpdatedAt           string                    `json:"updated_at"`
	MergeStatus         string                    `json:"merge_status"`
	DetailedMergeStatus string                    `json:"detailed_merge_status"`
	SHA                 string                    `json:"sha"`
	IID                 int                       `json:"iid"`
	Draft               bool                      `json:"draft"`
	HasConflicts        bool                      `json:"has_conflicts"`
	AutoMerge           bool                      `json:"merge_when_pipeline_succeeds"`
}

func (r *gitlabMR) toPR() PR {
	return PR{
		Number:         r.IID,
		Title:          r.Title,
		Body:           r.Description,
		State:          normalizePRState(r.State),
		Author:         r.Author.Username,
		SourceBranch:   r.SourceBranch,
		TargetBranch:   r.TargetBranch,
		URL:            r.WebURL,
		HeadSHA:        r.SHA,
		CheckStatus:    mapGLabCheckStatus(r.DetailedMergeStatus),
		MergeBlocked:   mapGLabMergeBlock(r.DetailedMergeStatus, r.MergeStatus, r.Draft, r.HasConflicts),
		CreatedAt:      parseRFC3339Millis(r.CreatedAt),
		UpdatedAt:      parseRFC3339Millis(r.UpdatedAt),
		Mergeable:      r.MergeStatus == "can_be_merged",
		Draft:          r.Draft,
		AutoMergeArmed: r.AutoMerge,
	}
}

// mapGLabCheckStatus derives the check chip from detailed_merge_status.
//
// Only two of its values are genuine CI statements. `mergeable` is NOT
// one of them: a project that does not require pipelines to pass is
// mergeable with a red pipeline, so treating it as passing would paint a
// green chip over a failure. GitLab therefore reports pending and
// failing, and stays silent otherwise.
func mapGLabCheckStatus(detailed string) string {
	switch detailed {
	case "ci_still_running":
		return checkPending
	case "ci_must_pass":
		return checkFailing
	}
	return ""
}

// mapGLabMergeBlock names the merge-block cause for a GitLab MR.
//
// detailed_merge_status is preferred; merge_status is the fallback for an
// instance old enough not to send it (deprecated in 15.6 but still
// present), and has_conflicts is what separates a conflict from every
// other cannot_be_merged reason on that path.
func mapGLabMergeBlock(detailed, mergeStatus string, draft, hasConflicts bool) string {
	switch detailed {
	case "mergeable":
		return ""
	case "draft_status":
		return blockDraft
	case "conflict":
		return blockConflicts
	case "ci_must_pass":
		return blockChecksFailing
	case "ci_still_running":
		return blockChecksRunning
	case "need_rebase":
		return blockBehind
	case "checking", "unchecked", "preparing":
		return blockUnknown
	case "":
		// Fall through to the deprecated field below.
	default:
		// not_approved, blocked_status, discussions_not_resolved,
		// requested_changes, policies_denied, status_checks_must_pass,
		// jira_association_missing, not_open, … — every one of these is
		// a project policy refusing the merge.
		return blockProtected
	}
	if draft {
		return blockDraft
	}
	if hasConflicts {
		return blockConflicts
	}
	switch mergeStatus {
	case "can_be_merged":
		return ""
	case "cannot_be_merged", "cannot_be_merged_recheck":
		return blockUnknown
	}
	return ""
}

func newGitLab(host string) *gitlabProvider {
	if host == "" {
		host = KindGitLab.DefaultHost()
	}
	return &gitlabProvider{host: host}
}

// withHost prepends host-targeting flags. glab uses GITLAB_HOST env
// var or per-config-section host; --hostname is supported on most
// commands.
func (p *gitlabProvider) withHost(args ...string) []string {
	return append([]string{flagHostname, p.host}, args...)
}

// Whoami uses glab api to fetch the authenticated user.
func (p *gitlabProvider) Whoami(ctx context.Context) (*User, error) {
	var raw struct {
		Username string `json:"username"`
		Name     string `json:"name"`
		Email    string `json:"public_email"`
		WebURL   string `json:"web_url"`
	}
	args := p.withHost("api", fieldUser)
	if err := runJSON(ctx, CmdTimeout, &raw, "glab", args...); err != nil {
		return nil, err
	}
	return &User{
		Login: raw.Username,
		Name:  raw.Name,
		Email: raw.Email,
		URL:   raw.WebURL,
	}, nil
}

// ListRepos lists projects accessible to the user.
func (p *gitlabProvider) ListRepos(ctx context.Context) ([]Repo, error) {
	// glab repo list isn't reliable for "all accessible" projects;
	// glab api projects?membership=true&per_page=100 is the canonical way.
	args := p.withHost("api", "projects", "-F", "membership=true", "-F", "per_page=100", "--paginate")
	out, err := runCmd(ctx, ListTimeout, nil, "glab", args...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ForkedFromProject *struct{} `json:"forked_from_project"`
		PathWithNamespace string    `json:"path_with_namespace"`
		Path              string    `json:"path"`
		Namespace         struct {
			FullPath string `json:"full_path"`
		} `json:"namespace"`
		DefaultBranch  string `json:"default_branch"`
		Description    string `json:"description"`
		WebURL         string `json:"web_url"`
		HTTPURLToRepo  string `json:"http_url_to_repo"`
		Visibility     string `json:"visibility"`
		LastActivityAt string `json:"last_activity_at"`
		Archived       bool   `json:"archived"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("glab list repos: decode: %w", err)
	}
	repos := make([]Repo, 0, len(raw))
	for i := range raw {
		r := &raw[i]
		repos = append(repos, Repo{
			Owner:         r.Namespace.FullPath,
			Name:          r.Path,
			FullName:      r.PathWithNamespace,
			DefaultBranch: r.DefaultBranch,
			URL:           r.WebURL,
			CloneURL:      r.HTTPURLToRepo,
			Description:   r.Description,
			Private:       r.Visibility != "public",
			Archived:      r.Archived,
			Fork:          r.ForkedFromProject != nil,
			UpdatedAt:     parseRFC3339Millis(r.LastActivityAt),
		})
	}
	return repos, nil
}

// ListPRs (MRs) — glab calls them merge requests.
func (p *gitlabProvider) ListPRs(ctx context.Context, repo string, state ListState) ([]PR, error) {
	mrState := mrStateForListing(state)
	args := p.withHost("mr", "list", "--repo", repo, "--state", mrState, "--output", "json", "--per-page", "100")
	out, err := runCmd(ctx, ListTimeout, nil, "glab", args...)
	if err != nil {
		return nil, err
	}
	var raw []gitlabMR
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("glab mr list: decode: %w", err)
	}
	prs := make([]PR, 0, len(raw))
	for i := range raw {
		prs = append(prs, raw[i].toPR())
	}
	return prs, nil
}

// CreatePR opens a new MR.
func (p *gitlabProvider) CreatePR(ctx context.Context, repo string, params *CreatePRParams) (*PR, error) {
	args := p.withHost("mr", "create",
		"--repo", repo,
		"--title", params.Title,
		"--description", params.Body,
		"--source-branch", params.SourceBranch,
		"--target-branch", params.TargetBranch,
		"--yes", // skip confirmation prompts
	)
	if params.Draft {
		args = append(args, "--draft")
	}
	for _, lbl := range params.Labels {
		args = append(args, "--label", lbl)
	}
	out, err := runCmd(ctx, CmdTimeout, nil, "glab", args...)
	if err != nil {
		return nil, err
	}
	url := trimSpace(string(out))
	number := extractPRNumberFromURL(url)
	if number == 0 {
		return nil, fmt.Errorf("glab mr create: could not parse MR number from output: %s", url)
	}
	return p.viewPR(ctx, repo, number)
}

// viewPR fetches a single MR by number.
func (p *gitlabProvider) viewPR(ctx context.Context, repo string, number int) (*PR, error) {
	args := p.withHost("mr", "view", strconv.Itoa(number), "--repo", repo, "--output", "json")
	var r gitlabMR
	if err := runJSON(ctx, CmdTimeout, &r, "glab", args...); err != nil {
		return nil, err
	}
	pr := r.toPR()
	return &pr, nil
}

// MergePR merges an MR.
//
// Two flags carry the new preconditions, both from glab's documented
// `mr merge` surface: `--sha` ("Merge only if the HEAD of the source
// branch matches this SHA") and `--auto-merge`.
//
// `--auto-merge` is passed ONLY when arming. glab documents its default
// as true, so a plain merge on a repository with a running pipeline is
// already handed to GitLab to finish — that is glab's behaviour today and
// this change does not alter it. Passing `--auto-merge=false` to force an
// immediate merge would be a behaviour change on a flag no binary here
// can confirm, so it is left alone.
func (p *gitlabProvider) MergePR(ctx context.Context, repo string, number int, opts MergeOptions) error {
	args := p.withHost("mr", "merge", strconv.Itoa(number), "--repo", repo, "--yes")
	switch opts.Method {
	case MergeSquash:
		args = append(args, "--squash")
	case MergeRebase:
		args = append(args, "--rebase")
	}
	if opts.HeadSHA != "" {
		args = append(args, "--sha", opts.HeadSHA)
	}
	if opts.Auto {
		args = append(args, "--auto-merge=true")
	}
	_, err := runCmd(ctx, CmdTimeout, nil, "glab", args...)
	return err
}

func (p *gitlabProvider) ClosePR(ctx context.Context, repo string, number int) error {
	args := p.withHost("mr", stateClose, strconv.Itoa(number), "--repo", repo)
	_, err := runCmd(ctx, CmdTimeout, nil, "glab", args...)
	return err
}

// ReopenPR reopens a closed MR — the mirror of ClosePR.
func (p *gitlabProvider) ReopenPR(ctx context.Context, repo string, number int) error {
	args := p.withHost("mr", "reopen", strconv.Itoa(number), "--repo", repo)
	_, err := runCmd(ctx, CmdTimeout, nil, "glab", args...)
	return err
}

// RerunFailedChecks retries the failed jobs of the MR's head pipeline.
//
// GitLab has no per-job "rerun failed" verb to reach: retrying failed
// jobs IS a pipeline endpoint, so this goes through `glab api` exactly as
// CommitStatus already does, rather than through `glab ci retry`, which
// is job-keyed and wants a local checkout. The pipeline id comes from the
// single-MR read, the one response GitLab documents head_pipeline on.
//
// `head_pipeline` is already a commit linkage — it is the pipeline GitLab
// reports FOR the MR's head — so unlike GitHub's branch-keyed run list this
// path never had the wrong-commit defect. headSHA is still checked, against
// the MR's own `sha`, because a row read before a force-push describes a
// commit that no longer exists and re-running is an action, not a read.
//
// The head pipeline's OWN sha is deliberately not compared to the MR's: with
// merged-results pipelines or a merge train the pipeline runs against a
// synthesized merge commit, so requiring equality would refuse every re-run on
// a project using either feature.
func (p *gitlabProvider) RerunFailedChecks(ctx context.Context, repo string, number int, headSHA string) error {
	project, err := gLabProjectPath(repo)
	if err != nil {
		return err
	}
	head, pipelineID, err := p.headPipelineID(ctx, project, number)
	if err != nil {
		return err
	}
	if headSHA != "" && !strings.EqualFold(headSHA, head) {
		return fmt.Errorf("merge request !%d has moved to commit %s since this was read; refresh and look at the new pipeline before retrying", number, head)
	}
	endpoint := fmt.Sprintf("projects/%s/pipelines/%d/retry", project, pipelineID)
	args := p.withHost("api", "--method", http.MethodPost, endpoint)
	_, err = runCmd(ctx, CmdTimeout, nil, "glab", args...)
	return err
}

// headPipelineID reads an MR's head commit and the id of the pipeline GitLab
// reports for it.
func (p *gitlabProvider) headPipelineID(ctx context.Context, project string, number int) (head string, pipelineID int64, err error) {
	endpoint := fmt.Sprintf("projects/%s/merge_requests/%d", project, number)
	args := p.withHost("api", endpoint)
	var raw struct {
		SHA          string `json:"sha"`
		HeadPipeline struct {
			ID int64 `json:"id"`
		} `json:"head_pipeline"`
	}
	if err := runJSON(ctx, CmdTimeout, &raw, "glab", args...); err != nil {
		return "", 0, err
	}
	if raw.HeadPipeline.ID == 0 {
		return "", 0, fmt.Errorf("no pipeline on merge request !%d", number)
	}
	return raw.SHA, raw.HeadPipeline.ID, nil
}

// gLabProjectPath renders "owner/name" as the URL-encoded project path
// GitLab's API addresses projects by.
func gLabProjectPath(repo string) (string, error) {
	owner, name, err := ParseRepo(repo)
	if err != nil {
		return "", err
	}
	return owner + "%2F" + name, nil
}

func (p *gitlabProvider) ListIssues(ctx context.Context, repo string, state ListState) ([]Issue, error) {
	st := string(state)
	switch state {
	case "", StateOpen:
		st = stateOpened
	}
	args := p.withHost("issue", "list", "--repo", repo, "--state", st, "--output", "json", "--per-page", "100")
	out, err := runCmd(ctx, ListTimeout, nil, "glab", args...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Title       string                    `json:"title"`
		Description string                    `json:"description"`
		State       string                    `json:"state"`
		Author      struct{ Username string } `json:"author"`
		WebURL      string                    `json:"web_url"`
		CreatedAt   string                    `json:"created_at"`
		UpdatedAt   string                    `json:"updated_at"`
		Labels      []string                  `json:"labels"`
		IID         int                       `json:"iid"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("glab issue list: decode: %w", err)
	}
	issues := make([]Issue, 0, len(raw))
	for i := range raw {
		r := &raw[i]
		issues = append(issues, Issue{
			Number:    r.IID,
			Title:     r.Title,
			Body:      r.Description,
			State:     normalizeIssueState(r.State),
			Author:    r.Author.Username,
			URL:       r.WebURL,
			Labels:    r.Labels,
			CreatedAt: parseRFC3339Millis(r.CreatedAt),
			UpdatedAt: parseRFC3339Millis(r.UpdatedAt),
		})
	}
	return issues, nil
}

func (p *gitlabProvider) CreateIssue(ctx context.Context, repo string, params CreateIssueParams) (*Issue, error) {
	args := p.withHost("issue", "create",
		"--repo", repo,
		"--title", params.Title,
		"--description", params.Body,
		"--yes",
	)
	for _, lbl := range params.Labels {
		args = append(args, "--label", lbl)
	}
	out, err := runCmd(ctx, CmdTimeout, nil, "glab", args...)
	if err != nil {
		return nil, err
	}
	url := trimSpace(string(out))
	number := extractIssueNumberFromURL(url)
	if number == 0 {
		return nil, fmt.Errorf("glab issue create: could not parse issue number: %s", url)
	}
	return p.viewIssue(ctx, repo, number)
}

func (p *gitlabProvider) viewIssue(ctx context.Context, repo string, number int) (*Issue, error) {
	args := p.withHost("issue", "view", strconv.Itoa(number), "--repo", repo, "--output", "json")
	var r struct {
		Title       string                    `json:"title"`
		Description string                    `json:"description"`
		State       string                    `json:"state"`
		Author      struct{ Username string } `json:"author"`
		WebURL      string                    `json:"web_url"`
		CreatedAt   string                    `json:"created_at"`
		UpdatedAt   string                    `json:"updated_at"`
		Labels      []string                  `json:"labels"`
		IID         int                       `json:"iid"`
	}
	if err := runJSON(ctx, CmdTimeout, &r, "glab", args...); err != nil {
		return nil, err
	}
	return &Issue{
		Number:    r.IID,
		Title:     r.Title,
		Body:      r.Description,
		State:     normalizeIssueState(r.State),
		Author:    r.Author.Username,
		URL:       r.WebURL,
		Labels:    r.Labels,
		CreatedAt: parseRFC3339Millis(r.CreatedAt),
		UpdatedAt: parseRFC3339Millis(r.UpdatedAt),
	}, nil
}

func (p *gitlabProvider) CloseIssue(ctx context.Context, repo string, number int) error {
	args := p.withHost("issue", stateClose, strconv.Itoa(number), "--repo", repo)
	_, err := runCmd(ctx, CmdTimeout, nil, "glab", args...)
	return err
}

// CommitStatus uses the GitLab API endpoint for pipeline statuses.
func (p *gitlabProvider) CommitStatus(ctx context.Context, repo, ref string) ([]Check, error) {
	project, err := gLabProjectPath(repo)
	if err != nil {
		return nil, err
	}
	// glab's "ci status" requires a local checkout; we use the API directly.
	endpoint := fmt.Sprintf("projects/%s/repository/commits/%s/statuses", project, ref)
	args := p.withHost("api", endpoint)
	out, err := runCmd(ctx, ListTimeout, nil, "glab", args...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name      string `json:"name"`
		Status    string `json:"status"`
		TargetURL string `json:"target_url"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("glab commit statuses: decode: %w", err)
	}
	checks := make([]Check, 0, len(raw))
	for _, c := range raw {
		conclusion := ""
		status := mapGLabStatus(c.Status)
		if status == stateCompleted {
			conclusion = mapGLabConclusion(c.Status)
		}
		checks = append(checks, Check{
			Name:       c.Name,
			Status:     status,
			Conclusion: conclusion,
			URL:        c.TargetURL,
		})
	}
	return checks, nil
}

func (p *gitlabProvider) ListReleases(ctx context.Context, repo string) ([]Release, error) {
	args := p.withHost("release", "list", "--repo", repo, "--per-page", "30", "--output", "json")
	out, err := runCmd(ctx, ListTimeout, nil, "glab", args...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		TagName     string                `json:"tag_name"`
		Name        string                `json:"name"`
		Description string                `json:"description"`
		Links       struct{ Self string } `json:"_links"`
		ReleasedAt  string                `json:"released_at"`
		UpcomingRel bool                  `json:"upcoming_release"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("glab release list: decode: %w", err)
	}
	releases := make([]Release, 0, len(raw))
	for _, r := range raw {
		releases = append(releases, Release{
			TagName:     r.TagName,
			Name:        r.Name,
			Body:        r.Description,
			URL:         r.Links.Self,
			Prerelease:  r.UpcomingRel,
			PublishedAt: parseRFC3339Millis(r.ReleasedAt),
		})
	}
	return releases, nil
}

func (p *gitlabProvider) CreateRelease(ctx context.Context, repo string, params CreateReleaseParams) (*Release, error) {
	args := p.withHost("release", "create", params.TagName, "--repo", repo)
	if params.Name != "" {
		args = append(args, "--name", params.Name)
	}
	if params.Body != "" {
		args = append(args, "--notes", params.Body)
	}
	if params.Target != "" {
		args = append(args, "--ref", params.Target)
	}
	out, err := runCmd(ctx, CmdTimeout, nil, "glab", args...)
	if err != nil {
		return nil, err
	}
	return &Release{
		TagName:     params.TagName,
		Name:        params.Name,
		Body:        params.Body,
		URL:         trimSpace(string(out)),
		PublishedAt: time.Now().UnixMilli(),
	}, nil
}

func (p *gitlabProvider) ListLabels(ctx context.Context, repo string) ([]Label, error) {
	args := p.withHost("label", "list", "--repo", repo, "--per-page", "100", "--output", "json")
	out, err := runCmd(ctx, ListTimeout, nil, "glab", args...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("glab label list: decode: %w", err)
	}
	labels := make([]Label, 0, len(raw))
	for _, r := range raw {
		labels = append(labels, Label{
			Name:        r.Name,
			Color:       r.Color,
			Description: r.Description,
		})
	}
	return labels, nil
}

// mrStateForListing maps the unified state filter to glab's expected
// values. glab uses stateOpened rather than StateOpen and treats merged as
// a separate state outside of StateClosed.
func mrStateForListing(s ListState) string {
	switch s {
	case StateOpen, "":
		return stateOpened
	case StateMerged:
		return stateMerged
	case StateClosed:
		return stateClosed
	case StateAll:
		return string(StateAll)
	}
	return string(s)
}

// mapGLabStatus converts a GitLab pipeline status to our canonical
// "queued" / "in_progress" / stateCompleted set.
func mapGLabStatus(s string) string {
	switch s {
	case statePending, "created", "scheduled":
		return "queued"
	case "running":
		return "in_progress"
	case statusSuccess, "failed", "canceled", stateSkipped, "manual":
		return stateCompleted
	}
	return s
}

// mapGLabConclusion maps a completed GitLab pipeline status to our
// conclusion field.
func mapGLabConclusion(s string) string {
	switch s {
	case statusSuccess:
		return statusSuccess
	case "failed":
		return statusFailure
	case "canceled":
		return "cancelled"
	case stateSkipped:
		return stateSkipped
	case "manual":
		return stateSkipped
	}
	return ""
}
