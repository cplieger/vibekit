// Gitea/Codeberg ForgeOps implementation backed by the tea CLI.
//
// Codeberg is a Gitea instance — same CLI, different host. The kind
// is preserved so the UI can render the right branding, but the
// provider behavior is identical.
//
// tea is less feature-rich than gh/glab; some operations (commit
// status, releases) don't have direct CLI commands and we fall back
// to the Gitea HTTP API via tea's API integration.

package forges

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// giteaProvider implements ForgeOps via the tea CLI. Used for both
// KindGitea and KindCodeberg (Codeberg is Gitea-compatible).
type giteaProvider struct {
	kind Kind
	host string
}

func newGitea(kind Kind, host string) *giteaProvider {
	if host == "" {
		host = kind.DefaultHost()
	}
	return &giteaProvider{kind: kind, host: host}
}

func (p *giteaProvider) Kind() Kind   { return p.kind }
func (p *giteaProvider) Host() string { return p.host }

// loginName returns the tea login alias that maps to this host.
// We use the host itself as the login name when injecting tokens,
// so this is just a stable accessor.
func (p *giteaProvider) loginName() string { return p.host }

// withLogin prepends the --login flag for the host's tea config.
func (p *giteaProvider) withLogin(args ...string) []string {
	return append([]string{"--login", p.loginName()}, args...)
}

// Whoami queries tea for the authenticated user.
func (p *giteaProvider) Whoami(ctx context.Context) (*User, error) {
	// `tea whoami` prints just the username on stdout.
	args := p.withLogin("whoami")
	out, err := runCmd(ctx, CmdTimeout, nil, "tea", args...)
	if err != nil {
		return nil, err
	}
	login := trimSpace(string(out))
	if login == "" {
		return nil, ErrNotLoggedIn
	}
	// Get full user info via API for email/name.
	full, apiErr := p.userViaAPI(ctx, login)
	if apiErr == nil {
		return full, nil
	}
	// Fall back to bare login if the API call fails (older tea).
	return &User{
		Login: login,
		URL:   fmt.Sprintf("https://%s/%s", p.host, login),
	}, nil
}

func (p *giteaProvider) userViaAPI(ctx context.Context, login string) (*User, error) {
	endpoint := fmt.Sprintf("https://%s/api/v1/users/%s", p.host, login)
	out, err := runCmd(ctx, CmdTimeout, nil, "curl", flagSilent, flagShowError,
		flagMaxTime, "20",
		flagHeader, "Accept: application/json",
		endpoint,
	)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Login    string `json:"login"`
		FullName string `json:"full_name"`
		Email    string `json:"email"`
		HTMLURL  string `json:"html_url"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	return &User{
		Login: raw.Login,
		Name:  raw.FullName,
		Email: raw.Email,
		URL:   raw.HTMLURL,
	}, nil
}

// ListRepos lists repositories accessible to the authenticated user.
func (p *giteaProvider) ListRepos(ctx context.Context) ([]Repo, error) {
	args := p.withLogin("repos", "list", "--output", "json", "--limit", "200")
	out, err := runCmd(ctx, ListTimeout, nil, "tea", args...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		FullName string `json:"full_name"`
		Name     string `json:"name"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
		DefaultBranch string `json:"default_branch"`
		Description   string `json:"description"`
		HTMLURL       string `json:"html_url"`
		CloneURL      string `json:"clone_url"`
		UpdatedAt     string `json:"updated_at"`
		Private       bool   `json:"private"`
		Archived      bool   `json:"archived"`
		Fork          bool   `json:"fork"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("tea repos list: decode: %w", err)
	}
	repos := make([]Repo, 0, len(raw))
	for i := range raw {
		r := &raw[i]
		owner := r.Owner.Login
		if owner == "" && strings.Contains(r.FullName, "/") {
			owner = strings.SplitN(r.FullName, "/", 2)[0]
		}
		repos = append(repos, Repo{
			Owner:         owner,
			Name:          r.Name,
			FullName:      r.FullName,
			DefaultBranch: r.DefaultBranch,
			URL:           r.HTMLURL,
			CloneURL:      r.CloneURL,
			Description:   r.Description,
			Private:       r.Private,
			Archived:      r.Archived,
			Fork:          r.Fork,
			UpdatedAt:     parseRFC3339Millis(r.UpdatedAt),
		})
	}
	return repos, nil
}

// ListPRs lists pulls for repo.
func (p *giteaProvider) ListPRs(ctx context.Context, repo string, state ListState) ([]PR, error) {
	if state == "" {
		state = StateOpen
	}
	args := p.withLogin("pulls", "list", "--repo", repo, "--state", string(state), "--output", "json", "--limit", "100")
	out, err := runCmd(ctx, ListTimeout, nil, "tea", args...)
	if err != nil {
		return nil, err
	}
	return p.parsePRs(out)
}

func (p *giteaProvider) parsePRs(data []byte) ([]PR, error) {
	var raw []struct {
		Base      struct{ Ref string }   `json:"base"`
		Title     string                 `json:"title"`
		Body      string                 `json:"body"`
		State     string                 `json:"state"`
		User      struct{ Login string } `json:"user"`
		Head      struct{ Ref string }   `json:"head"`
		HTMLURL   string                 `json:"html_url"`
		CreatedAt string                 `json:"created_at"`
		UpdatedAt string                 `json:"updated_at"`
		Number    int                    `json:"number"`
		Mergeable bool                   `json:"mergeable"`
		Draft     bool                   `json:"draft"`
		Merged    bool                   `json:"merged"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("tea pulls: decode: %w", err)
	}
	prs := make([]PR, 0, len(raw))
	for i := range raw {
		r := &raw[i]
		state := normalizePRState(r.State)
		if r.Merged {
			state = stateMerged
		}
		prs = append(prs, PR{
			Number:       r.Number,
			Title:        r.Title,
			Body:         r.Body,
			State:        state,
			Author:       r.User.Login,
			SourceBranch: r.Head.Ref,
			TargetBranch: r.Base.Ref,
			URL:          r.HTMLURL,
			CreatedAt:    parseRFC3339Millis(r.CreatedAt),
			UpdatedAt:    parseRFC3339Millis(r.UpdatedAt),
			Mergeable:    r.Mergeable,
			Draft:        r.Draft,
		})
	}
	return prs, nil
}

// CreatePR opens a new pull request via tea pr create.
func (p *giteaProvider) CreatePR(ctx context.Context, repo string, params *CreatePRParams) (*PR, error) {
	args := p.withLogin("pulls", "create",
		"--repo", repo,
		"--title", params.Title,
		"--description", params.Body,
		"--head", params.SourceBranch,
		"--base", params.TargetBranch,
	)
	for _, lbl := range params.Labels {
		args = append(args, "--labels", lbl)
	}
	out, err := runCmd(ctx, CmdTimeout, nil, "tea", args...)
	if err != nil {
		return nil, err
	}
	// tea prints the PR URL on success; parse the number from it.
	number := extractPRNumberFromURL(string(out))
	if number == 0 {
		// Fallback: list and find newest by source branch.
		prs, listErr := p.ListPRs(ctx, repo, StateOpen)
		if listErr == nil {
			for i := range prs {
				if prs[i].SourceBranch == params.SourceBranch {
					return &prs[i], nil
				}
			}
		}
		return nil, errors.New("tea pulls create: could not locate created PR")
	}
	return p.viewPR(ctx, repo, number)
}

func (p *giteaProvider) viewPR(ctx context.Context, repo string, number int) (*PR, error) {
	owner, name, err := ParseRepo(repo)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("https://%s/api/v1/repos/%s/%s/pulls/%d", p.host, owner, name, number)
	out, err := p.apiGet(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	prs, err := p.parsePRs(append([]byte("["), append(out, ']')...))
	if err != nil || len(prs) == 0 {
		return nil, fmt.Errorf("tea view pr: parse single: %v", err)
	}
	return &prs[0], nil
}

func (p *giteaProvider) MergePR(ctx context.Context, repo string, number int, method MergeMethod) error {
	style := "merge"
	switch method {
	case MergeSquash:
		style = string(MergeSquash)
	case MergeRebase:
		style = string(MergeRebase)
	}
	owner, name, err := ParseRepo(repo)
	if err != nil {
		return err
	}
	// tea doesn't have a direct merge command yet; use the API.
	endpoint := fmt.Sprintf("https://%s/api/v1/repos/%s/%s/pulls/%d/merge", p.host, owner, name, number)
	body := fmt.Sprintf(`{"Do":%q}`, style)
	return p.apiPostJSON(ctx, endpoint, []byte(body))
}

func (p *giteaProvider) ClosePR(ctx context.Context, repo string, number int) error {
	owner, name, err := ParseRepo(repo)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("https://%s/api/v1/repos/%s/%s/pulls/%d", p.host, owner, name, number)
	body := []byte(`{"state":"closed"}`)
	return p.apiPatchJSON(ctx, endpoint, body)
}

func (p *giteaProvider) ListIssues(ctx context.Context, repo string, state ListState) ([]Issue, error) {
	if state == "" {
		state = StateOpen
	}
	args := p.withLogin("issues", "list", "--repo", repo, "--state", string(state), "--output", "json", "--limit", "100")
	out, err := runCmd(ctx, ListTimeout, nil, "tea", args...)
	if err != nil {
		return nil, err
	}
	return p.parseIssues(out)
}

func (p *giteaProvider) parseIssues(data []byte) ([]Issue, error) {
	var raw []struct {
		Title     string                  `json:"title"`
		Body      string                  `json:"body"`
		State     string                  `json:"state"`
		User      struct{ Login string }  `json:"user"`
		HTMLURL   string                  `json:"html_url"`
		CreatedAt string                  `json:"created_at"`
		UpdatedAt string                  `json:"updated_at"`
		Labels    []struct{ Name string } `json:"labels"`
		Number    int                     `json:"number"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("tea issues: decode: %w", err)
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
			Author:    r.User.Login,
			URL:       r.HTMLURL,
			Labels:    labels,
			CreatedAt: parseRFC3339Millis(r.CreatedAt),
			UpdatedAt: parseRFC3339Millis(r.UpdatedAt),
		})
	}
	return issues, nil
}

func (p *giteaProvider) CreateIssue(ctx context.Context, repo string, params CreateIssueParams) (*Issue, error) {
	args := p.withLogin("issues", "create",
		"--repo", repo,
		"--title", params.Title,
		"--description", params.Body,
	)
	for _, lbl := range params.Labels {
		args = append(args, "--labels", lbl)
	}
	out, err := runCmd(ctx, CmdTimeout, nil, "tea", args...)
	if err != nil {
		return nil, err
	}
	number := extractIssueNumberFromURL(string(out))
	if number == 0 {
		issues, listErr := p.ListIssues(ctx, repo, StateOpen)
		if listErr == nil && len(issues) > 0 {
			// Return the newest issue by creation time.
			newest := issues[0]
			for idx := range issues {
				i := &issues[idx]
				if i.CreatedAt > newest.CreatedAt {
					newest = *i
				}
			}
			return &newest, nil
		}
		return nil, errors.New("tea issues create: could not locate created issue")
	}
	return p.viewIssue(ctx, repo, number)
}

func (p *giteaProvider) viewIssue(ctx context.Context, repo string, number int) (*Issue, error) {
	owner, name, err := ParseRepo(repo)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("https://%s/api/v1/repos/%s/%s/issues/%d", p.host, owner, name, number)
	out, err := p.apiGet(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	issues, err := p.parseIssues(append([]byte("["), append(out, ']')...))
	if err != nil || len(issues) == 0 {
		return nil, fmt.Errorf("tea view issue: parse single: %v", err)
	}
	return &issues[0], nil
}

func (p *giteaProvider) CloseIssue(ctx context.Context, repo string, number int) error {
	owner, name, err := ParseRepo(repo)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("https://%s/api/v1/repos/%s/%s/issues/%d", p.host, owner, name, number)
	body := []byte(`{"state":"closed"}`)
	return p.apiPatchJSON(ctx, endpoint, body)
}

// CommitStatus uses the Gitea API directly (no tea command for this).
func (p *giteaProvider) CommitStatus(ctx context.Context, repo, ref string) ([]Check, error) {
	owner, name, err := ParseRepo(repo)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("https://%s/api/v1/repos/%s/%s/commits/%s/statuses", p.host, owner, name, url.PathEscape(ref))
	out, err := p.apiGet(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Context   string `json:"context"`
		Status    string `json:"status"`
		TargetURL string `json:"target_url"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("gitea commit statuses: decode: %w", err)
	}
	checks := make([]Check, 0, len(raw))
	for _, c := range raw {
		status := mapGiteaStatus(c.Status)
		conclusion := ""
		if status == stateCompleted {
			conclusion = mapGiteaConclusion(c.Status)
		}
		checks = append(checks, Check{
			Name:       c.Context,
			Status:     status,
			Conclusion: conclusion,
			URL:        c.TargetURL,
		})
	}
	return checks, nil
}

func (p *giteaProvider) ListReleases(ctx context.Context, repo string) ([]Release, error) {
	args := p.withLogin("releases", "list", "--repo", repo, "--output", "json", "--limit", "30")
	out, err := runCmd(ctx, ListTimeout, nil, "tea", args...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		TagName     string `json:"tag_name"`
		Name        string `json:"name"`
		Body        string `json:"body"`
		HTMLURL     string `json:"html_url"`
		PublishedAt string `json:"published_at"`
		Draft       bool   `json:"draft"`
		Prerelease  bool   `json:"prerelease"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("tea releases list: decode: %w", err)
	}
	releases := make([]Release, 0, len(raw))
	for _, r := range raw {
		releases = append(releases, Release{
			TagName:     r.TagName,
			Name:        r.Name,
			Body:        r.Body,
			Draft:       r.Draft,
			Prerelease:  r.Prerelease,
			URL:         r.HTMLURL,
			PublishedAt: parseRFC3339Millis(r.PublishedAt),
		})
	}
	return releases, nil
}

func (p *giteaProvider) CreateRelease(ctx context.Context, repo string, params CreateReleaseParams) (*Release, error) {
	args := p.withLogin("releases", "create", "--repo", repo, "--tag", params.TagName)
	if params.Name != "" {
		args = append(args, "--title", params.Name)
	}
	if params.Body != "" {
		args = append(args, "--note", params.Body)
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
	if _, err := runCmd(ctx, CmdTimeout, nil, "tea", args...); err != nil {
		return nil, err
	}
	return &Release{
		TagName:     params.TagName,
		Name:        params.Name,
		Body:        params.Body,
		Draft:       params.Draft,
		Prerelease:  params.Prerelease,
		URL:         fmt.Sprintf("https://%s/%s/releases/tag/%s", p.host, repo, params.TagName),
		PublishedAt: time.Now().UnixMilli(),
	}, nil
}

func (p *giteaProvider) ListLabels(ctx context.Context, repo string) ([]Label, error) {
	args := p.withLogin("labels", "list", "--repo", repo, "--output", "json", "--limit", "200")
	out, err := runCmd(ctx, ListTimeout, nil, "tea", args...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("tea labels list: decode: %w", err)
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

// apiGet calls the Gitea API using tea's stored token.
// Falls back to running curl with the user's token from tea's config.
// Since tea doesn't have a native "raw API" passthrough, we read its
// config file (see inject.go for the format) and use curl.
func (p *giteaProvider) apiGet(ctx context.Context, endpoint string) ([]byte, error) {
	token, err := readTeaToken(p.host)
	if err != nil {
		return nil, err
	}
	args := []string{
		flagSilent, flagShowError, flagMaxTime, "30",
		flagHeader, "Accept: application/json",
		flagHeader, "Authorization: token " + token,
		endpoint,
	}
	return runCmd(ctx, CmdTimeout, nil, "curl", args...)
}

// apiPostJSON sends a POST with a JSON body to the Gitea API.
func (p *giteaProvider) apiPostJSON(ctx context.Context, endpoint string, body []byte) error {
	token, err := readTeaToken(p.host)
	if err != nil {
		return err
	}
	args := []string{
		flagSilent, flagShowError, flagMaxTime, "30",
		"--fail",
		flagHeader, "Content-Type: application/json",
		flagHeader, "Authorization: token " + token,
		"--data-binary", "@-",
		endpoint,
	}
	_, err = runCmd(ctx, CmdTimeout, body, "curl", args...)
	return err
}

// apiPatchJSON sends a PATCH with a JSON body.
func (p *giteaProvider) apiPatchJSON(ctx context.Context, endpoint string, body []byte) error {
	token, err := readTeaToken(p.host)
	if err != nil {
		return err
	}
	args := []string{
		flagSilent, flagShowError, flagMaxTime, "30",
		"--fail",
		"--request", "PATCH",
		flagHeader, "Content-Type: application/json",
		flagHeader, "Authorization: token " + token,
		"--data-binary", "@-",
		endpoint,
	}
	_, err = runCmd(ctx, CmdTimeout, body, "curl", args...)
	return err
}

func mapGiteaStatus(s string) string {
	switch strings.ToLower(s) {
	case statePending:
		return "queued"
	case "running":
		return "in_progress"
	case statusSuccess, statusFailure, statusError, "warning":
		return stateCompleted
	}
	return s
}

func mapGiteaConclusion(s string) string {
	switch strings.ToLower(s) {
	case statusSuccess:
		return statusSuccess
	case statusFailure, statusError:
		return statusFailure
	case "warning":
		return stateSkipped
	}
	return ""
}

// strconv import is unused otherwise — it was used by the removed
// strconvItoa helper. Keep the import only if other call sites exist.
