// GitHub ForgeOps implementation backed by the gh CLI.
//
// All operations shell out to gh with --json or --output json
// flags where available. JSON schemas are gh-specific; we map them
// into the unified types from provider.go.

package forges

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// githubProvider is a ForgeOps backed by the gh CLI.
type githubProvider struct {
	host string
}

// newGitHub builds a github provider for the given host. host="" maps
// to github.com.
func newGitHub(host string) *githubProvider {
	if host == "" {
		host = KindGitHub.DefaultHost()
	}
	return &githubProvider{host: host}
}

func (p *githubProvider) Kind() Kind   { return KindGitHub }
func (p *githubProvider) Host() string { return p.host }

// withHost prepends the --hostname flag to args so commands target
// the right gh-configured host. Required for GitHub Enterprise.
func (p *githubProvider) withHost(args ...string) []string {
	return append([]string{"--hostname", p.host}, args...)
}

// Whoami queries gh's auth status to confirm login + return the user.
//
// gh's `auth status --hostname X` output isn't great for parsing, but
// `gh api user --hostname X` returns the same /user endpoint as the
// API would. We use the latter.
func (p *githubProvider) Whoami(ctx context.Context) (*User, error) {
	var raw struct {
		Login   string `json:"login"`
		Name    string `json:"name"`
		Email   string `json:"email"`
		HTMLURL string `json:"html_url"`
	}
	args := []string{"api", "user", "--hostname", p.host}
	if err := runJSON(ctx, CmdTimeout, &raw, "gh", args...); err != nil {
		return nil, err
	}
	return &User{
		Login: raw.Login,
		Name:  raw.Name,
		Email: raw.Email,
		URL:   raw.HTMLURL,
	}, nil
}

// ListRepos lists user-owned and collaborated-on repos.
func (p *githubProvider) ListRepos(ctx context.Context) ([]Repo, error) {
	// gh repo list uses repository "graph" output with structured fields.
	fields := "name,owner,nameWithOwner,defaultBranchRef,description,isPrivate,isArchived,isFork,sshUrl,url,updatedAt"
	args := p.withHost("repo", "list", "--json", fields, "--limit", "300")
	var raw []struct {
		Name             string                 `json:"name"`
		Owner            struct{ Login string } `json:"owner"`
		NameWithOwner    string                 `json:"nameWithOwner"`
		DefaultBranchRef struct{ Name string }  `json:"defaultBranchRef"`
		Description      string                 `json:"description"`
		URL              string                 `json:"url"`
		UpdatedAt        string                 `json:"updatedAt"`
		IsPrivate        bool                   `json:"isPrivate"`
		IsArchived       bool                   `json:"isArchived"`
		IsFork           bool                   `json:"isFork"`
	}
	if err := runJSON(ctx, ListTimeout, &raw, "gh", args...); err != nil {
		return nil, err
	}
	repos := make([]Repo, 0, len(raw))
	for _, r := range raw {
		updated := parseRFC3339Millis(r.UpdatedAt)
		repos = append(repos, Repo{
			Owner:         r.Owner.Login,
			Name:          r.Name,
			FullName:      r.NameWithOwner,
			DefaultBranch: r.DefaultBranchRef.Name,
			URL:           r.URL,
			CloneURL:      fmt.Sprintf("https://%s/%s.git", p.host, r.NameWithOwner),
			Description:   r.Description,
			Private:       r.IsPrivate,
			Archived:      r.IsArchived,
			Fork:          r.IsFork,
			UpdatedAt:     updated,
		})
	}
	return repos, nil
}

// ListPRs lists pull requests for repo.
func (p *githubProvider) ListPRs(ctx context.Context, repo, state string) ([]PR, error) {
	if state == "" {
		state = "open"
	}
	fields := "number,title,body,state,author,headRefName,baseRefName,url,createdAt,updatedAt,mergeable,isDraft"
	args := p.withHost("pr", "list", "--repo", repo, "--state", state, "--json", fields, "--limit", "100")
	var raw []struct {
		Title       string                 `json:"title"`
		Body        string                 `json:"body"`
		State       string                 `json:"state"`
		Author      struct{ Login string } `json:"author"`
		HeadRefName string                 `json:"headRefName"`
		BaseRefName string                 `json:"baseRefName"`
		URL         string                 `json:"url"`
		CreatedAt   string                 `json:"createdAt"`
		UpdatedAt   string                 `json:"updatedAt"`
		Mergeable   string                 `json:"mergeable"`
		Number      int                    `json:"number"`
		IsDraft     bool                   `json:"isDraft"`
	}
	if err := runJSON(ctx, ListTimeout, &raw, "gh", args...); err != nil {
		return nil, err
	}
	prs := make([]PR, 0, len(raw))
	for i := range raw {
		r := &raw[i]
		prs = append(prs, PR{
			Number:       r.Number,
			Title:        r.Title,
			Body:         r.Body,
			State:        normalizePRState(r.State),
			Author:       r.Author.Login,
			SourceBranch: r.HeadRefName,
			TargetBranch: r.BaseRefName,
			URL:          r.URL,
			CreatedAt:    parseRFC3339Millis(r.CreatedAt),
			UpdatedAt:    parseRFC3339Millis(r.UpdatedAt),
			Mergeable:    r.Mergeable == "MERGEABLE",
			Draft:        r.IsDraft,
		})
	}
	return prs, nil
}

// CreatePR opens a new pull request via gh pr create.
func (p *githubProvider) CreatePR(ctx context.Context, repo string, params *CreatePRParams) (*PR, error) {
	args := p.withHost("pr", "create",
		"--repo", repo,
		"--title", params.Title,
		"--body", params.Body,
		"--head", params.SourceBranch,
		"--base", params.TargetBranch,
	)
	if params.Draft {
		args = append(args, "--draft")
	}
	for _, lbl := range params.Labels {
		args = append(args, "--label", lbl)
	}
	out, err := runCmd(ctx, CmdTimeout, nil, "gh", args...)
	if err != nil {
		return nil, err
	}
	// gh prints the PR URL on success — fetch the full PR to return.
	url := string(out)
	prNumber := extractPRNumberFromURL(url)
	if prNumber == 0 {
		return nil, fmt.Errorf("gh pr create: could not parse PR number from output: %s", url)
	}
	return p.viewPR(ctx, repo, prNumber)
}

// viewPR fetches a single PR by number for use after mutations.
func (p *githubProvider) viewPR(ctx context.Context, repo string, number int) (*PR, error) {
	fields := "number,title,body,state,author,headRefName,baseRefName,url,createdAt,updatedAt,mergeable,isDraft"
	args := p.withHost("pr", "view", strconv.Itoa(number), "--repo", repo, "--json", fields)
	var r struct {
		Title       string                 `json:"title"`
		Body        string                 `json:"body"`
		State       string                 `json:"state"`
		Author      struct{ Login string } `json:"author"`
		HeadRefName string                 `json:"headRefName"`
		BaseRefName string                 `json:"baseRefName"`
		URL         string                 `json:"url"`
		CreatedAt   string                 `json:"createdAt"`
		UpdatedAt   string                 `json:"updatedAt"`
		Mergeable   string                 `json:"mergeable"`
		Number      int                    `json:"number"`
		IsDraft     bool                   `json:"isDraft"`
	}
	if err := runJSON(ctx, CmdTimeout, &r, "gh", args...); err != nil {
		return nil, err
	}
	return &PR{
		Number:       r.Number,
		Title:        r.Title,
		Body:         r.Body,
		State:        normalizePRState(r.State),
		Author:       r.Author.Login,
		SourceBranch: r.HeadRefName,
		TargetBranch: r.BaseRefName,
		URL:          r.URL,
		CreatedAt:    parseRFC3339Millis(r.CreatedAt),
		UpdatedAt:    parseRFC3339Millis(r.UpdatedAt),
		Mergeable:    r.Mergeable == "MERGEABLE",
		Draft:        r.IsDraft,
	}, nil
}

// MergePR merges a PR. method: "merge" | "squash" | "rebase".
func (p *githubProvider) MergePR(ctx context.Context, repo string, number int, method string) error {
	args := p.withHost("pr", "merge", strconv.Itoa(number), "--repo", repo)
	switch method {
	case "squash":
		args = append(args, "--squash")
	case "rebase":
		args = append(args, "--rebase")
	default:
		args = append(args, "--merge")
	}
	_, err := runCmd(ctx, CmdTimeout, nil, "gh", args...)
	return err
}

// ClosePR closes an open PR without merging.
func (p *githubProvider) ClosePR(ctx context.Context, repo string, number int) error {
	args := p.withHost("pr", "close", strconv.Itoa(number), "--repo", repo)
	_, err := runCmd(ctx, CmdTimeout, nil, "gh", args...)
	return err
}

// ListIssues lists issues for repo.
func (p *githubProvider) ListIssues(ctx context.Context, repo, state string) ([]Issue, error) {
	if state == "" {
		state = "open"
	}
	fields := "number,title,body,state,author,url,labels,createdAt,updatedAt"
	args := p.withHost("issue", "list", "--repo", repo, "--state", state, "--json", fields, "--limit", "100")
	var raw []struct {
		Title     string                  `json:"title"`
		Body      string                  `json:"body"`
		State     string                  `json:"state"`
		Author    struct{ Login string }  `json:"author"`
		URL       string                  `json:"url"`
		CreatedAt string                  `json:"createdAt"`
		UpdatedAt string                  `json:"updatedAt"`
		Labels    []struct{ Name string } `json:"labels"`
		Number    int                     `json:"number"`
	}
	if err := runJSON(ctx, ListTimeout, &raw, "gh", args...); err != nil {
		return nil, err
	}
	issues := make([]Issue, 0, len(raw))
	for i := range raw {
		r := &raw[i]
		labels := make([]string, 0, len(r.Labels))
		for _, l := range r.Labels {
			labels = append(labels, l.Name)
		}
		issues = append(issues, Issue{
			Number:    r.Number,
			Title:     r.Title,
			Body:      r.Body,
			State:     normalizeIssueState(r.State),
			Author:    r.Author.Login,
			URL:       r.URL,
			Labels:    labels,
			CreatedAt: parseRFC3339Millis(r.CreatedAt),
			UpdatedAt: parseRFC3339Millis(r.UpdatedAt),
		})
	}
	return issues, nil
}

// CreateIssue files a new issue.
func (p *githubProvider) CreateIssue(ctx context.Context, repo string, params CreateIssueParams) (*Issue, error) {
	args := p.withHost("issue", "create",
		"--repo", repo,
		"--title", params.Title,
		"--body", params.Body,
	)
	for _, lbl := range params.Labels {
		args = append(args, "--label", lbl)
	}
	out, err := runCmd(ctx, CmdTimeout, nil, "gh", args...)
	if err != nil {
		return nil, err
	}
	url := string(out)
	number := extractIssueNumberFromURL(url)
	if number == 0 {
		return nil, fmt.Errorf("gh issue create: could not parse issue number from output: %s", url)
	}
	return p.viewIssue(ctx, repo, number)
}

func (p *githubProvider) viewIssue(ctx context.Context, repo string, number int) (*Issue, error) {
	fields := "number,title,body,state,author,url,labels,createdAt,updatedAt"
	args := p.withHost("issue", "view", strconv.Itoa(number), "--repo", repo, "--json", fields)
	var r struct {
		Title     string                  `json:"title"`
		Body      string                  `json:"body"`
		State     string                  `json:"state"`
		Author    struct{ Login string }  `json:"author"`
		URL       string                  `json:"url"`
		CreatedAt string                  `json:"createdAt"`
		UpdatedAt string                  `json:"updatedAt"`
		Labels    []struct{ Name string } `json:"labels"`
		Number    int                     `json:"number"`
	}
	if err := runJSON(ctx, CmdTimeout, &r, "gh", args...); err != nil {
		return nil, err
	}
	labels := make([]string, 0, len(r.Labels))
	for _, l := range r.Labels {
		labels = append(labels, l.Name)
	}
	return &Issue{
		Number:    r.Number,
		Title:     r.Title,
		Body:      r.Body,
		State:     normalizeIssueState(r.State),
		Author:    r.Author.Login,
		URL:       r.URL,
		Labels:    labels,
		CreatedAt: parseRFC3339Millis(r.CreatedAt),
		UpdatedAt: parseRFC3339Millis(r.UpdatedAt),
	}, nil
}

// CloseIssue closes an open issue.
func (p *githubProvider) CloseIssue(ctx context.Context, repo string, number int) error {
	args := p.withHost("issue", "close", strconv.Itoa(number), "--repo", repo)
	_, err := runCmd(ctx, CmdTimeout, nil, "gh", args...)
	return err
}

// CommitStatus returns CI checks for a ref. Uses gh api directly
// since `gh run list` only covers Actions, not other status providers.
func (p *githubProvider) CommitStatus(ctx context.Context, repo, ref string) ([]Check, error) {
	owner, name, err := ParseRepo(repo)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("repos/%s/%s/commits/%s/check-runs", owner, name, ref)
	args := []string{"api", endpoint, "--hostname", p.host}
	out, err := runCmd(ctx, ListTimeout, nil, "gh", args...)
	if err != nil {
		return nil, err
	}
	var raw struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
		} `json:"check_runs"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("gh api check-runs: decode: %w", err)
	}
	checks := make([]Check, 0, len(raw.CheckRuns))
	for _, c := range raw.CheckRuns {
		checks = append(checks, Check{
			Name:       c.Name,
			Status:     c.Status,
			Conclusion: c.Conclusion,
			URL:        c.HTMLURL,
		})
	}
	return checks, nil
}

// ListReleases lists recent releases for repo.
func (p *githubProvider) ListReleases(ctx context.Context, repo string) ([]Release, error) {
	args := p.withHost("release", "list", "--repo", repo, "--limit", "30", "--json", "tagName,name,isDraft,isPrerelease,url,publishedAt")
	var raw []struct {
		TagName      string `json:"tagName"`
		Name         string `json:"name"`
		URL          string `json:"url"`
		PublishedAt  string `json:"publishedAt"`
		IsDraft      bool   `json:"isDraft"`
		IsPrerelease bool   `json:"isPrerelease"`
	}
	if err := runJSON(ctx, ListTimeout, &raw, "gh", args...); err != nil {
		return nil, err
	}
	releases := make([]Release, 0, len(raw))
	for _, r := range raw {
		releases = append(releases, Release{
			TagName:     r.TagName,
			Name:        r.Name,
			Draft:       r.IsDraft,
			Prerelease:  r.IsPrerelease,
			URL:         r.URL,
			PublishedAt: parseRFC3339Millis(r.PublishedAt),
		})
	}
	return releases, nil
}

// CreateRelease cuts a new release.
func (p *githubProvider) CreateRelease(ctx context.Context, repo string, params CreateReleaseParams) (*Release, error) {
	args := p.withHost("release", "create", params.TagName, "--repo", repo)
	if params.Name != "" {
		args = append(args, "--title", params.Name)
	}
	if params.Body != "" {
		args = append(args, "--notes", params.Body)
	}
	if params.Target != "" {
		args = append(args, "--target", params.Target)
	}
	if params.Draft {
		args = append(args, "--draft")
	}
	if params.Prerelease {
		args = append(args, "--prerelease")
	}
	out, err := runCmd(ctx, CmdTimeout, nil, "gh", args...)
	if err != nil {
		return nil, err
	}
	url := string(out)
	return &Release{
		TagName:     params.TagName,
		Name:        params.Name,
		Body:        params.Body,
		Draft:       params.Draft,
		Prerelease:  params.Prerelease,
		URL:         trimSpace(url),
		PublishedAt: time.Now().UnixMilli(),
	}, nil
}

// ListLabels returns labels defined on repo.
func (p *githubProvider) ListLabels(ctx context.Context, repo string) ([]Label, error) {
	args := p.withHost("label", "list", "--repo", repo, "--limit", "200", "--json", "name,color,description")
	var raw []struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}
	if err := runJSON(ctx, ListTimeout, &raw, "gh", args...); err != nil {
		return nil, err
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
