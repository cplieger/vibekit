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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/runesafe"
)

// giteaProvider implements ForgeOps via the tea CLI. Used for both
// KindGitea and KindCodeberg (Codeberg is Gitea-compatible).
type giteaProvider struct {
	host string
}

func newGitea(kind Kind, host string) *giteaProvider {
	if host == "" {
		host = kind.DefaultHost()
	}
	return &giteaProvider{host: host}
}

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
	out, err := p.apiGet(ctx, endpoint)
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
	return parsePRs(out)
}

// parsePRs decodes Gitea's own PR objects. `tea pulls list -o json`
// passes the API shape through rather than the --fields vocabulary, so
// this one decoder serves both the list and the single-PR view.
//
// CheckStatus is deliberately left empty for every Gitea row: the PR
// object carries no CI state, and the alternatives were both worse than
// an absent chip — one extra statuses request per PR (the N-call fan-out
// this work rejects) or a chip inferred from `mergeable`, which would be
// a guess presented as a fact.
func parsePRs(data []byte) ([]PR, error) {
	var raw []struct {
		Base      struct{ Ref string }      `json:"base"`
		Title     string                    `json:"title"`
		Body      string                    `json:"body"`
		State     string                    `json:"state"`
		User      struct{ Login string }    `json:"user"`
		Head      struct{ Ref, Sha string } `json:"head"`
		HTMLURL   string                    `json:"html_url"`
		CreatedAt string                    `json:"created_at"`
		UpdatedAt string                    `json:"updated_at"`
		Number    int                       `json:"number"`
		Mergeable bool                      `json:"mergeable"`
		Draft     bool                      `json:"draft"`
		Merged    bool                      `json:"merged"`
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
			HeadSHA:      r.Head.Sha,
			MergeBlocked: giteaMergeBlock(r.Mergeable, r.Draft),
			CreatedAt:    parseRFC3339Millis(r.CreatedAt),
			UpdatedAt:    parseRFC3339Millis(r.UpdatedAt),
			Mergeable:    r.Mergeable,
			Draft:        r.Draft,
		})
	}
	return prs, nil
}

// giteaMergeBlock names what a Gitea PR object can actually support.
// `mergeable` is one bit with no cause behind it, so an unmergeable PR
// reports blockUnknown: the row then says the forge refuses the merge
// without inventing conflicts, checks or protection as the reason.
func giteaMergeBlock(mergeable, draft bool) string {
	if draft {
		return blockDraft
	}
	if !mergeable {
		return blockUnknown
	}
	return ""
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
	prs, err := parsePRs(append([]byte("["), append(out, ']')...))
	if err != nil || len(prs) == 0 {
		return nil, fmt.Errorf("tea view pr: parse single: %v", err)
	}
	return &prs[0], nil
}

// giteaMergeBody is the Gitea merge-API request. Both new fields are
// omitted unless asked for, so an instance that predates
// merge_when_checks_succeed still sees the body it has always seen.
type giteaMergeBody struct {
	Do                     string `json:"Do"`
	HeadCommitID           string `json:"head_commit_id,omitempty"`
	MergeWhenChecksSucceed bool   `json:"merge_when_checks_succeed,omitempty"`
}

// giteaMergeRequestBody renders the merge request body for opts.
func giteaMergeRequestBody(opts MergeOptions) ([]byte, error) {
	style := string(MergeCommit)
	switch opts.Method {
	case MergeSquash:
		style = string(MergeSquash)
	case MergeRebase:
		style = string(MergeRebase)
	}
	body, err := json.Marshal(giteaMergeBody{
		Do:                     style,
		HeadCommitID:           opts.HeadSHA,
		MergeWhenChecksSucceed: opts.Auto,
	})
	if err != nil {
		return nil, fmt.Errorf("gitea merge: encode body: %w", err)
	}
	return body, nil
}

// MergePR merges a PR through the Gitea API.
//
// tea gained a `pulls merge` verb, but it exposes only --style/--title/
// --message: no head-commit flag and no auto-merge flag. The API carries
// both (head_commit_id, merge_when_checks_succeed), and this call was
// already on the API path, so the pin and the arming are body fields
// rather than a new dependency on the CLI's feature set.
func (p *giteaProvider) MergePR(ctx context.Context, repo string, number int, opts MergeOptions) error {
	owner, name, err := ParseRepo(repo)
	if err != nil {
		return err
	}
	body, err := giteaMergeRequestBody(opts)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("https://%s/api/v1/repos/%s/%s/pulls/%d/merge", p.host, owner, name, number)
	return p.apiPostJSON(ctx, endpoint, body)
}

func (p *giteaProvider) ClosePR(ctx context.Context, repo string, number int) error {
	return p.setPRState(ctx, repo, number, stateClosed)
}

// ReopenPR reopens a closed PR. It goes through the same PATCH ClosePR
// uses rather than `tea pulls reopen`, so the pair shares one token path
// and one failure shape.
func (p *giteaProvider) ReopenPR(ctx context.Context, repo string, number int) error {
	return p.setPRState(ctx, repo, number, stateOpen)
}

// setPRState PATCHes a PR's state field.
func (p *giteaProvider) setPRState(ctx context.Context, repo string, number int, state string) error {
	owner, name, err := ParseRepo(repo)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"state": state})
	if err != nil {
		return fmt.Errorf("gitea pr state: encode body: %w", err)
	}
	endpoint := fmt.Sprintf("https://%s/api/v1/repos/%s/%s/pulls/%d", p.host, owner, name, number)
	return p.apiPatchJSON(ctx, endpoint, body)
}

// RerunFailedChecks is not available on Gitea or Forgejo: tea has no CI
// verb at all, and the Actions re-run endpoints are not part of the
// stable public API the rest of this file talks to. Returning the
// sentinel lets the client hide the control instead of offering one that
// always fails.
func (p *giteaProvider) RerunFailedChecks(_ context.Context, _ string, _ int, _ string) error {
	return fmt.Errorf("%w: re-running CI is not available on gitea/forgejo", ErrNotSupported)
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
	return parseIssues(out)
}

func parseIssues(data []byte) ([]Issue, error) {
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
	issues, err := parseIssues(append([]byte("["), append(out, ']')...))
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

// Gitea/Forgejo REST API access.
//
// These calls go through Go's net/http rather than shelling out to
// curl. That is a deliberate security choice: the auth token is set as
// an Authorization *header*, so it is never part of a process-argument
// list. It therefore cannot leak into a cliexec CmdError string (which
// joins argv), an slog line, or the HTTP error body writeOpsError
// returns to the browser — the PAT-in-error-response leak the curl-arg
// approach caused.

// giteaAPIClient is the shared HTTP client for direct Gitea/Forgejo API
// calls. The timeout mirrors the CLI command timeout so a wedged forge
// can't pin a request.
var giteaAPIClient = &http.Client{Timeout: CmdTimeout}

// apiMaxResponseBytes caps how much of an API response body we buffer,
// mirroring cliexec's output cap.
const apiMaxResponseBytes = 32 << 20 // 32 MiB

// teaTokenCache holds helper-minted tokens per host, so the gap-fill
// API paths don't spawn a `tea login helper get` subprocess per call.
// Invalidated on a 401/403 response (doAPI re-mints once and retries)
// — the moment a token is rotated via a fresh login, the next API call
// self-heals.
var teaTokenCache sync.Map // host → token string

// teaHelperToken mints the API token for host through tea's own
// git-credential-protocol interface (`tea login helper get`), the same
// documented surface git itself authenticates through. vibekit holds
// the token in memory only — it never persists a second copy.
func teaHelperToken(ctx context.Context, host string) (string, error) {
	if v, ok := teaTokenCache.Load(host); ok {
		if tok, tokOK := v.(string); tokOK && tok != "" {
			return tok, nil
		}
	}
	stdin := "protocol=https\nhost=" + host + "\n\n"
	out, err := runCmd(ctx, CmdTimeout, []byte(stdin), cliTea, "login", "helper", "get")
	if err != nil {
		return "", fmt.Errorf("forges: tea token for %q: %w", host, err)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if pw, ok := strings.CutPrefix(line, "password="); ok && pw != "" {
			teaTokenCache.Store(host, pw)
			return pw, nil
		}
	}
	return "", fmt.Errorf("forges: no tea token for host %q", host)
}

// doAPI performs an authenticated Gitea API request. The token is sent
// only as an Authorization header, so it is structurally absent from
// every error this function can return. On a non-2xx status it returns
// the (token-free) response body plus a status-coded error. A 401/403
// invalidates the cached token and retries once with a fresh mint, so
// a token rotated through a re-login heals without a restart.
func (p *giteaProvider) doAPI(ctx context.Context, method, endpoint string, body []byte) ([]byte, error) {
	token, err := teaHelperToken(ctx, p.host)
	if err != nil {
		return nil, err
	}
	data, status, err := doAPIWith(ctx, token, method, endpoint, body)
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		teaTokenCache.Delete(p.host)
		fresh, tokErr := teaHelperToken(ctx, p.host)
		if tokErr != nil {
			return data, err
		}
		data, _, err = doAPIWith(ctx, fresh, method, endpoint, body)
	}
	return data, err
}

// doAPIWith is doAPI's single-attempt core, bound to one token.
func doAPIWith(ctx context.Context, token, method, endpoint string, body []byte) (data []byte, status int, err error) {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	//nolint:gosec // G704: the URL authority is the user's own configured forge host (constrained to logged-in forges by manager.Get); only the path varies, so this is not attacker-controlled SSRF
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reqBody)
	if err != nil {
		return nil, 0, fmt.Errorf("gitea api: build request: %w", err)
	}
	req.Header.Set("Accept", mimeTypeJSON)
	req.Header.Set("Authorization", "token "+token)
	if body != nil {
		req.Header.Set("Content-Type", mimeTypeJSON)
	}
	//nolint:gosec // G704: the URL authority is the user's own configured forge host (constrained to logged-in forges by manager.Get); only the path varies, so this is not attacker-controlled SSRF
	resp, err := giteaAPIClient.Do(req)
	if err != nil {
		// The url.Error here carries the method, URL, and cause — none of
		// which contain the token (it lived only in a request header).
		return nil, 0, fmt.Errorf("gitea api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err = io.ReadAll(io.LimitReader(resp.Body, apiMaxResponseBytes))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("gitea api: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, resp.StatusCode, fmt.Errorf("gitea api: %s %s: status %d: %s",
			method, endpoint, resp.StatusCode, apiErrorSnippet(data))
	}
	return data, resp.StatusCode, nil
}

// apiErrorSnippet returns a short, single-line summary of a Gitea API
// error body for an error message. It is derived purely from the
// server's response, so it never contains the request's auth token.
// Both paths are sanitized and byte-capped (rune-boundary safe) via
// runesafe, so an upstream-controlled body cannot forge log records,
// carry terminal escapes, or balloon the error string.
func apiErrorSnippet(body []byte) string {
	const maxLen = 256
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		return runesafe.SanitizeSingleLineBounded(trimSpace(e.Message), maxLen)
	}
	return runesafe.SanitizeSingleLineBounded(trimSpace(string(body)), maxLen)
}

// apiGet performs an authenticated GET against the Gitea API.
func (p *giteaProvider) apiGet(ctx context.Context, endpoint string) ([]byte, error) {
	return p.doAPI(ctx, http.MethodGet, endpoint, nil)
}

// apiPostJSON sends a POST with a JSON body to the Gitea API.
func (p *giteaProvider) apiPostJSON(ctx context.Context, endpoint string, body []byte) error {
	_, err := p.doAPI(ctx, http.MethodPost, endpoint, body)
	return err
}

// apiPatchJSON sends a PATCH with a JSON body.
func (p *giteaProvider) apiPatchJSON(ctx context.Context, endpoint string, body []byte) error {
	_, err := p.doAPI(ctx, http.MethodPatch, endpoint, body)
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
