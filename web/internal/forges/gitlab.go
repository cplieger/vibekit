// GitLab ForgeOps implementation backed by the glab CLI.
//
// glab supports per-host config; we pass --hostname for cloud GitLab
// and self-hosted instances. Most subcommands accept --output json.

package forges

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// gitlabProvider implements ForgeOps via the glab CLI.
type gitlabProvider struct {
	host string
}

// gitlabMR is the wire struct for GitLab merge request JSON responses.
// Shared between ListPRs (array) and viewPR (single object).
type gitlabMR struct {
	Title        string                    `json:"title"`
	Description  string                    `json:"description"`
	State        string                    `json:"state"`
	Author       struct{ Username string } `json:"author"`
	SourceBranch string                    `json:"source_branch"`
	TargetBranch string                    `json:"target_branch"`
	WebURL       string                    `json:"web_url"`
	CreatedAt    string                    `json:"created_at"`
	UpdatedAt    string                    `json:"updated_at"`
	MergeStatus  string                    `json:"merge_status"`
	IID          int                       `json:"iid"`
	Draft        bool                      `json:"draft"`
}

func (r *gitlabMR) toPR() PR {
	return PR{
		Number:       r.IID,
		Title:        r.Title,
		Body:         r.Description,
		State:        normalizePRState(r.State),
		Author:       r.Author.Username,
		SourceBranch: r.SourceBranch,
		TargetBranch: r.TargetBranch,
		URL:          r.WebURL,
		CreatedAt:    parseRFC3339Millis(r.CreatedAt),
		UpdatedAt:    parseRFC3339Millis(r.UpdatedAt),
		Mergeable:    r.MergeStatus == "can_be_merged",
		Draft:        r.Draft,
	}
}

func newGitLab(host string) *gitlabProvider {
	if host == "" {
		host = KindGitLab.DefaultHost()
	}
	return &gitlabProvider{host: host}
}

func (p *gitlabProvider) Kind() Kind   { return KindGitLab }
func (p *gitlabProvider) Host() string { return p.host }

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
func (p *gitlabProvider) ListPRs(ctx context.Context, repo, state string) ([]PR, error) {
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

func (p *gitlabProvider) MergePR(ctx context.Context, repo string, number int, method string) error {
	args := p.withHost("mr", "merge", strconv.Itoa(number), "--repo", repo, "--yes")
	switch method {
	case mergeSquash:
		args = append(args, "--squash")
	case mergeRebase:
		args = append(args, "--rebase")
	}
	_, err := runCmd(ctx, CmdTimeout, nil, "glab", args...)
	return err
}

func (p *gitlabProvider) ClosePR(ctx context.Context, repo string, number int) error {
	args := p.withHost("mr", stateClose, strconv.Itoa(number), "--repo", repo)
	_, err := runCmd(ctx, CmdTimeout, nil, "glab", args...)
	return err
}

func (p *gitlabProvider) ListIssues(ctx context.Context, repo, state string) ([]Issue, error) {
	switch state {
	case "", stateOpen:
		state = stateOpened
	}
	args := p.withHost("issue", "list", "--repo", repo, "--state", state, "--output", "json", "--per-page", "100")
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
	owner, name, err := ParseRepo(repo)
	if err != nil {
		return nil, err
	}
	// glab's "ci status" requires a local checkout; we use the API directly.
	endpoint := fmt.Sprintf("projects/%s%%2F%s/repository/commits/%s/statuses", owner, name, ref)
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
// values. glab uses stateOpened rather than stateOpen and treats merged as
// a separate state outside of stateClosed.
func mrStateForListing(s string) string {
	switch s {
	case stateOpen:
		return stateOpened
	case stateMerged:
		return stateMerged
	case stateClosed:
		return stateClosed
	case "all":
		return "all"
	case "":
		return stateOpened
	}
	return s
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
