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
	"net/url"
	"strconv"
	"strings"
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

// withHost is a pass-through that's kept as a single seam for any
// future per-host flag plumbing. The actual host targeting happens
// via envHost (GH_HOST environment variable) — gh's --hostname flag
// is per-subcommand and not supported by `gh repo list`,
// `gh issue list`, and others, so we drove the consistency at the
// env-var layer instead.
func (p *githubProvider) withHost(args ...string) []string {
	return args
}

// envHost returns the env-var pair that targets gh at p.host. Apply
// to every gh subprocess via runJSONEnv / runCmdEnv.
func (p *githubProvider) envHost() []string {
	return []string{"GH_HOST=" + p.host}
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
	args := []string{"api", fieldUser}
	if err := runJSONEnv(ctx, CmdTimeout, p.envHost(), &raw, "gh", args...); err != nil {
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
	args := p.withHost(fieldRepo, "list", "--json", fields, "--limit", "300")
	var raw []struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		NameWithOwner    string `json:"nameWithOwner"`
		DefaultBranchRef struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
		Description string `json:"description"`
		URL         string `json:"url"`
		UpdatedAt   string `json:"updatedAt"`
		IsPrivate   bool   `json:"isPrivate"`
		IsArchived  bool   `json:"isArchived"`
		IsFork      bool   `json:"isFork"`
	}
	if err := runJSONEnv(ctx, ListTimeout, p.envHost(), &raw, "gh", args...); err != nil {
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

// ghPRFields is the --json field set for a PR read. Shared by ListPRs
// and viewPR so the two cannot drift; viewPR's result is what CreatePR
// hands back, and a field missing there is a field the caller loses.
//
// statusCheckRollup, mergeStateStatus, headRefOid and autoMergeRequest
// all ride this ONE call: gh resolves them in a single GraphQL query,
// so the row's check chip, its merge-block cause and its merge pin cost
// no extra round trip. mergeStateStatus is a slow field on large
// repositories — if list latency ever regresses, it is the one to drop
// (mergeable plus the rollup still carry a coarser cause).
const ghPRFields = "number,title,body,state,author,headRefName,baseRefName,url," +
	"createdAt,updatedAt,mergeable,isDraft,headRefOid,mergeStateStatus," +
	"statusCheckRollup,autoMergeRequest"

// ghRollupEntry is one element of gh's statusCheckRollup array. GitHub
// returns TWO shapes in that one array and __typename is the
// discriminator: a CheckRun carries status + conclusion, a
// StatusContext carries state. Both key sets are decoded because both
// arrive (verified against gh 2.94.0 on a live repository).
type ghRollupEntry struct {
	TypeName   string `json:"__typename"`
	Name       string `json:"name"`
	Context    string `json:"context"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
	// DetailsURL is the CheckRun's link to the thing that produced it. For a
	// GitHub Actions check that is the workflow RUN, which makes this the
	// commit-scoped run linkage RerunFailedChecks resolves against — see
	// ghActionsRunID. A StatusContext carries targetUrl instead and is
	// deliberately not decoded: gh run cannot re-run a third-party status.
	DetailsURL string `json:"detailsUrl"`
}

// ghPRRaw is the decode target for a gh PR read.
type ghPRRaw struct {
	// AutoMergeRequest is null unless auto-merge is already armed; only
	// its presence is read, so the shape stays empty. Pointer first for
	// govet fieldalignment.
	AutoMergeRequest *struct{} `json:"autoMergeRequest"`
	Title            string    `json:"title"`
	Body             string    `json:"body"`
	State            string    `json:"state"`
	Author           struct {
		Login string `json:"login"`
	} `json:"author"`
	HeadRefName       string          `json:"headRefName"`
	BaseRefName       string          `json:"baseRefName"`
	URL               string          `json:"url"`
	CreatedAt         string          `json:"createdAt"`
	UpdatedAt         string          `json:"updatedAt"`
	Mergeable         string          `json:"mergeable"`
	HeadRefOid        string          `json:"headRefOid"`
	MergeStateStatus  string          `json:"mergeStateStatus"`
	StatusCheckRollup []ghRollupEntry `json:"statusCheckRollup"`
	Number            int             `json:"number"`
	IsDraft           bool            `json:"isDraft"`
}

func (r *ghPRRaw) toPR() PR {
	status, total, failing := summarizeGHRollup(r.StatusCheckRollup)
	return PR{
		Number:         r.Number,
		Title:          r.Title,
		Body:           r.Body,
		State:          normalizePRState(r.State),
		Author:         r.Author.Login,
		SourceBranch:   r.HeadRefName,
		TargetBranch:   r.BaseRefName,
		URL:            r.URL,
		HeadSHA:        r.HeadRefOid,
		CheckStatus:    status,
		MergeBlocked:   mapGHMergeState(r.MergeStateStatus, status),
		ChecksTotal:    total,
		ChecksFailing:  failing,
		CreatedAt:      parseRFC3339Millis(r.CreatedAt),
		UpdatedAt:      parseRFC3339Millis(r.UpdatedAt),
		Mergeable:      r.Mergeable == "MERGEABLE",
		Draft:          r.IsDraft,
		AutoMergeArmed: r.AutoMergeRequest != nil,
	}
}

// summarizeGHRollup folds a rollup array into the canonical check
// verdict plus its counts. A failure outranks a pending check, which
// outranks a pass; an empty rollup is no verdict at all rather than a
// passing one, because "nothing ran" and "everything passed" are
// different facts and only one of them justifies a green chip.
func summarizeGHRollup(entries []ghRollupEntry) (status string, total, failing int) {
	pending := 0
	for i := range entries {
		e := &entries[i]
		switch classifyGHCheck(e.Status, e.Conclusion, e.State) {
		case checkFailing:
			total++
			failing++
		case checkPending:
			total++
			pending++
		case checkPassing:
			total++
		}
	}
	switch {
	case failing > 0:
		return checkFailing, total, failing
	case pending > 0:
		return checkPending, total, failing
	case total > 0:
		return checkPassing, total, failing
	}
	return "", 0, 0
}

// ListPRs lists pull requests for repo.
func (p *githubProvider) ListPRs(ctx context.Context, repo string, state ListState) ([]PR, error) {
	if state == "" {
		state = StateOpen
	}
	args := p.withHost("pr", "list", "--repo", repo, "--state", string(state), "--json", ghPRFields, "--limit", "100")
	var raw []ghPRRaw
	if err := runJSONEnv(ctx, ListTimeout, p.envHost(), &raw, "gh", args...); err != nil {
		return nil, err
	}
	prs := make([]PR, 0, len(raw))
	for i := range raw {
		prs = append(prs, raw[i].toPR())
	}
	return prs, nil
}

// CreatePR opens a new pull request via gh pr create.
func (p *githubProvider) CreatePR(ctx context.Context, repo string, params *CreatePRParams) (*PR, error) {
	// Pass the body via stdin (--body-file -) rather than --body STRING.
	// AI-generated PR descriptions can exceed ARG_MAX (~128 KB on Linux);
	// stdin has no such limit. Title is short enough to stay as a flag arg.
	args := p.withHost("pr", "create",
		"--repo", repo,
		"--title", params.Title,
		"--body-file", "-",
		"--head", params.SourceBranch,
		"--base", params.TargetBranch,
	)
	if params.Draft {
		args = append(args, "--draft")
	}
	for _, lbl := range params.Labels {
		args = append(args, "--label", lbl)
	}
	out, err := runCmdEnv(ctx, CmdTimeout, []byte(params.Body), p.envHost(), "gh", args...)
	if err != nil {
		return nil, err
	}
	// gh prints the PR URL on success — fetch the full PR to return.
	createdURL := string(out)
	prNumber := extractPRNumberFromURL(createdURL)
	if prNumber == 0 {
		return nil, fmt.Errorf("gh pr create: could not parse PR number from output: %s", createdURL)
	}
	return p.viewPR(ctx, repo, prNumber)
}

// viewPR fetches a single PR by number for use after mutations.
func (p *githubProvider) viewPR(ctx context.Context, repo string, number int) (*PR, error) {
	args := p.withHost("pr", "view", strconv.Itoa(number), "--repo", repo, "--json", ghPRFields)
	var r ghPRRaw
	if err := runJSONEnv(ctx, CmdTimeout, p.envHost(), &r, "gh", args...); err != nil {
		return nil, err
	}
	pr := r.toPR()
	return &pr, nil
}

// MergePR merges a PR, pinning the head commit when opts.HeadSHA is set
// and arming gh's own auto-merge when opts.Auto is.
func (p *githubProvider) MergePR(ctx context.Context, repo string, number int, opts MergeOptions) error {
	args := p.withHost("pr", "merge", strconv.Itoa(number), "--repo", repo)
	switch opts.Method {
	case MergeSquash:
		args = append(args, "--squash")
	case MergeRebase:
		args = append(args, "--rebase")
	default:
		args = append(args, "--merge")
	}
	if opts.HeadSHA != "" {
		args = append(args, "--match-head-commit", opts.HeadSHA)
	}
	if opts.Auto {
		args = append(args, "--auto")
	}
	_, err := runCmdEnv(ctx, CmdTimeout, nil, p.envHost(), "gh", args...)
	return err
}

// ClosePR closes an open PR without merging.
func (p *githubProvider) ClosePR(ctx context.Context, repo string, number int) error {
	args := p.withHost("pr", stateClose, strconv.Itoa(number), "--repo", repo)
	_, err := runCmdEnv(ctx, CmdTimeout, nil, p.envHost(), "gh", args...)
	return err
}

// ReopenPR reopens a closed PR — the exact mirror of ClosePR.
func (p *githubProvider) ReopenPR(ctx context.Context, repo string, number int) error {
	args := p.withHost("pr", "reopen", strconv.Itoa(number), "--repo", repo)
	_, err := runCmdEnv(ctx, CmdTimeout, nil, p.envHost(), "gh", args...)
	return err
}

// RerunFailedChecks re-runs the failed jobs of a workflow run belonging to
// the PR's CURRENT head commit.
//
// `gh run rerun` is keyed on a RUN id, not a PR number, so a run has to be
// resolved first — and WHICH run is the whole correctness question. Resolving
// PR → head BRANCH → newest failed run on that branch was wrong twice over: a
// branch is mutable, and "newest failed anywhere in the last 20 runs" ignores
// which commit failed. The concrete case is a PR whose current red status comes
// from a third-party provider while the same branch carries an older failed
// Actions run: the row offers Re-run, the branch search finds the historical
// failure, and CI re-runs an older commit. A re-run can trigger a deployment,
// so that is an observable wrong action rather than a wasted minute.
//
// The fix is to take the run id from the PR's own statusCheckRollup, which is
// BY DEFINITION the check set of the head commit. That is a commit linkage
// rather than a branch one, so it needs no `run list` (one fewer subprocess)
// and no comparison against a run's self-reported headSha — the comparison the
// merge-result and merge-queue workflow triggers would break, since those run
// against a synthesized commit rather than the PR head.
//
// headSHA is the caller's row identity and is checked against the PR's live
// head: a row read before a force-push must not act on what replaced it.
//
// gh run covers GitHub Actions only. A PR failing a third-party status
// provider has no run to rerun, and saying so beats a silent no-op.
func (p *githubProvider) RerunFailedChecks(ctx context.Context, repo string, number int, headSHA string) error {
	head, rollup, err := p.prHeadChecks(ctx, repo, number)
	if err != nil {
		return err
	}
	if headSHA != "" && !strings.EqualFold(headSHA, head) {
		return fmt.Errorf("PR #%d has moved to commit %s since this was read; refresh and look at the new checks before re-running", number, head)
	}
	runID, ok := failedActionsRun(rollup, repo)
	if !ok {
		return fmt.Errorf("no failed workflow run on PR #%d's head commit %s (gh run covers GitHub Actions only)", number, head)
	}
	args := p.withHost("run", "rerun", strconv.FormatInt(runID, 10), "--repo", repo, "--failed")
	_, err = runCmdEnv(ctx, CmdTimeout, nil, p.envHost(), "gh", args...)
	return err
}

// prHeadChecks reads a PR's head commit and the rollup of that commit's
// checks. Two fields off the shared read rather than the whole ghPRFields set:
// this path needs no body, no author and no mergeStateStatus, which is the slow
// field on a large repository.
func (p *githubProvider) prHeadChecks(ctx context.Context, repo string, number int) (head string, rollup []ghRollupEntry, err error) {
	args := p.withHost("pr", "view", strconv.Itoa(number), "--repo", repo,
		"--json", "headRefOid,statusCheckRollup")
	var r struct {
		HeadRefOid        string          `json:"headRefOid"`
		StatusCheckRollup []ghRollupEntry `json:"statusCheckRollup"`
	}
	if err := runJSONEnv(ctx, CmdTimeout, p.envHost(), &r, "gh", args...); err != nil {
		return "", nil, err
	}
	if r.HeadRefOid == "" {
		return "", nil, fmt.Errorf("gh pr view %d: no head commit reported", number)
	}
	return r.HeadRefOid, r.StatusCheckRollup, nil
}

// failedActionsRun picks the Actions run to re-run out of a head commit's
// rollup: the highest run id among the FAILING check runs whose details URL
// names a run in this same repository.
//
// Highest id rather than first-listed because run ids are assigned in creation
// order, so the maximum is the newest attempt — the rollup's own order is not
// documented, and a re-run needs to be deterministic rather than merely
// plausible. Requiring the URL's repository to match is what keeps a check
// reported by another repository's workflow from being re-run here.
//
// A non-Actions failure (a third-party status, or a check run whose details
// point somewhere else) contributes nothing, which is what makes the caller's
// "no failed workflow run" answer honest rather than a fallback.
func failedActionsRun(rollup []ghRollupEntry, repo string) (runID int64, found bool) {
	for i := range rollup {
		e := &rollup[i]
		if classifyGHCheck(e.Status, e.Conclusion, e.State) != checkFailing {
			continue
		}
		id, ok := ghActionsRunID(e.DetailsURL, repo)
		if ok && id > runID {
			runID, found = id, true
		}
	}
	return runID, found
}

// ghActionsRunID extracts the workflow-run id from a check run's details URL,
// requiring the URL to name an Actions run in repo.
//
// The shape is https://<host>/<owner>/<name>/actions/runs/<id>[/job/<id>]
// (verified against gh 2.94.0 on a live repository). Anything else — a
// third-party provider's dashboard, an Actions URL for a different repository,
// a non-numeric id — is not a run this call may re-run.
func ghActionsRunID(detailsURL, repo string) (runID int64, ok bool) {
	u, err := url.Parse(detailsURL)
	if err != nil {
		return 0, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 5 || parts[2] != "actions" || parts[3] != "runs" {
		return 0, false
	}
	// GitHub repository names are case-insensitive, and gh echoes the
	// canonical casing while the caller's repo string came from the row.
	if !strings.EqualFold(parts[0]+"/"+parts[1], repo) {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// classifyGHCheck folds one rollup entry into the canonical check
// vocabulary, or "" for an entry that carries no verdict (a skipped or
// neutral check, or a shape with no recognised state word).
//
// A CheckRun reports status + conclusion; a StatusContext reports state.
// Both are read without branching on __typename, because the two field
// sets are disjoint in practice and an unset field classifies as "".
func classifyGHCheck(status, conclusion, state string) string {
	switch strings.ToUpper(status) {
	case "QUEUED", "IN_PROGRESS", "WAITING", "REQUESTED", "PENDING":
		return checkPending
	case "COMPLETED":
		return ghConclusionVerdict(conclusion)
	}
	// StatusContext: a commit status has one state word and no status.
	switch strings.ToUpper(state) {
	case "SUCCESS":
		return checkPassing
	case "PENDING", "EXPECTED":
		return checkPending
	case "FAILURE", "ERROR":
		return checkFailing
	}
	return ""
}

// ghConclusionVerdict maps a completed check's conclusion. SKIPPED and
// NEUTRAL carry no verdict: counting them as passes would let a PR whose
// every check was skipped render a green chip.
func ghConclusionVerdict(conclusion string) string {
	if isGHFailedConclusion(conclusion) {
		return checkFailing
	}
	if strings.EqualFold(conclusion, "SUCCESS") {
		return checkPassing
	}
	return ""
}

// isGHFailedConclusion reports whether a conclusion word means the run
// did not succeed in a way a re-run could fix.
func isGHFailedConclusion(conclusion string) bool {
	switch strings.ToUpper(conclusion) {
	case "FAILURE", "TIMED_OUT", "CANCELLED", "STARTUP_FAILURE", "ACTION_REQUIRED", "STALE":
		return true
	}
	return false
}

// mapGHMergeState names the merge-block cause from GitHub's
// mergeStateStatus, falling back to the check verdict when the state
// says a check is at fault without saying which way.
//
// UNSTABLE and HAS_HOOKS are NOT blocks: GitHub permits those merges, so
// reporting them as blocked would disable a button the forge would have
// honoured. CLEAN is the mergeable case.
func mapGHMergeState(mergeState, checkStatus string) string {
	switch strings.ToUpper(mergeState) {
	case "CLEAN", "UNSTABLE", "HAS_HOOKS":
		return ""
	case "DIRTY":
		return blockConflicts
	case "DRAFT":
		return blockDraft
	case "BEHIND":
		return blockBehind
	case "BLOCKED":
		// A required check that is failing or still running is a more
		// specific answer than "branch protection", and the rollup we
		// already fetched knows which it is.
		switch checkStatus {
		case checkFailing:
			return blockChecksFailing
		case checkPending:
			return blockChecksRunning
		}
		return blockProtected
	case "UNKNOWN":
		return blockUnknown
	}
	return ""
}

// ListIssues lists issues for repo.
func (p *githubProvider) ListIssues(ctx context.Context, repo string, state ListState) ([]Issue, error) {
	if state == "" {
		state = StateOpen
	}
	fields := "number,title,body,state,author,url,labels,createdAt,updatedAt"
	args := p.withHost("issue", "list", "--repo", repo, "--state", string(state), "--json", fields, "--limit", "100")
	var raw []struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		State  string `json:"state"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		URL       string `json:"url"`
		CreatedAt string `json:"createdAt"`
		UpdatedAt string `json:"updatedAt"`
		Labels    []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Number int `json:"number"`
	}
	if err := runJSONEnv(ctx, ListTimeout, p.envHost(), &raw, "gh", args...); err != nil {
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
	// Body via stdin (--body-file -) for the same reason as CreatePR:
	// avoids ARG_MAX on long bodies.
	args := p.withHost("issue", "create",
		"--repo", repo,
		"--title", params.Title,
		"--body-file", "-",
	)
	for _, lbl := range params.Labels {
		args = append(args, "--label", lbl)
	}
	out, err := runCmdEnv(ctx, CmdTimeout, []byte(params.Body), p.envHost(), "gh", args...)
	if err != nil {
		return nil, err
	}
	createdURL := string(out)
	number := extractIssueNumberFromURL(createdURL)
	if number == 0 {
		return nil, fmt.Errorf("gh issue create: could not parse issue number from output: %s", createdURL)
	}
	return p.viewIssue(ctx, repo, number)
}

func (p *githubProvider) viewIssue(ctx context.Context, repo string, number int) (*Issue, error) {
	fields := "number,title,body,state,author,url,labels,createdAt,updatedAt"
	args := p.withHost("issue", "view", strconv.Itoa(number), "--repo", repo, "--json", fields)
	var r struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		State  string `json:"state"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		URL       string `json:"url"`
		CreatedAt string `json:"createdAt"`
		UpdatedAt string `json:"updatedAt"`
		Labels    []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Number int `json:"number"`
	}
	if err := runJSONEnv(ctx, CmdTimeout, p.envHost(), &r, "gh", args...); err != nil {
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
	args := p.withHost("issue", stateClose, strconv.Itoa(number), "--repo", repo)
	_, err := runCmdEnv(ctx, CmdTimeout, nil, p.envHost(), "gh", args...)
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
	args := []string{"api", endpoint}
	out, err := runCmdEnv(ctx, ListTimeout, nil, p.envHost(), "gh", args...)
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
	if err := runJSONEnv(ctx, ListTimeout, p.envHost(), &raw, "gh", args...); err != nil {
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
	// Notes via stdin (--notes-file -) for the same ARG_MAX reasoning
	// as CreatePR. Empty body still goes through stdin (gh treats it
	// as no notes); harmless and keeps the call site simple.
	if params.Body != "" {
		args = append(args, "--notes-file", "-")
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
	var stdin []byte
	if params.Body != "" {
		stdin = []byte(params.Body)
	}
	out, err := runCmdEnv(ctx, CmdTimeout, stdin, p.envHost(), "gh", args...)
	if err != nil {
		return nil, err
	}
	createdURL := string(out)
	return &Release{
		TagName:     params.TagName,
		Name:        params.Name,
		Body:        params.Body,
		Draft:       params.Draft,
		Prerelease:  params.Prerelease,
		URL:         trimSpace(createdURL),
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
	if err := runJSONEnv(ctx, ListTimeout, p.envHost(), &raw, "gh", args...); err != nil {
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
