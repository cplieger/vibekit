// Package forges integrates remote git forges (GitHub, GitLab, Gitea/Codeberg)
// by orchestrating their first-party CLI tools (gh, glab, tea).
//
// Design: the CLI tools own all the hard parts — auth (including OAuth
// device flow, token refresh), credential helpers for git, API
// pagination, error mapping. vibekit owns the UX and the unified API
// surface that the agent and the web UI both consume.
//
// There is no encrypted credential store. The CLIs persist tokens
// in their own config files (~/.config/gh/hosts.yml, etc.). The
// container is single-user; file permissions are sufficient.
package forges

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Kind identifies a forge backend.
type Kind string

const (
	KindGitHub   Kind = "github"
	KindGitLab   Kind = "gitlab"
	KindGitea    Kind = "gitea"    // also covers Codeberg (codeberg.org is a Gitea instance)
	KindCodeberg Kind = "codeberg" // synonym for KindGitea, host=codeberg.org
)

// Valid reports whether k is a known forge kind.
func (k Kind) Valid() bool {
	switch k {
	case KindGitHub, KindGitLab, KindGitea, KindCodeberg:
		return true
	}
	return false
}

// CLI returns the CLI tool name backing this kind.
func (k Kind) CLI() string {
	switch k {
	case KindGitHub:
		return "gh"
	case KindGitLab:
		return "glab"
	case KindGitea, KindCodeberg:
		return "tea"
	}
	return ""
}

// DefaultHost returns the canonical hostname for the kind, or "" if
// no default exists (self-hosted Gitea/Forgejo).
func (k Kind) DefaultHost() string {
	switch k {
	case KindGitHub:
		return "github.com"
	case KindGitLab:
		return "gitlab.com"
	case KindCodeberg:
		return "codeberg.org"
	}
	return ""
}

// Title returns the human-readable display name for the kind.
func (k Kind) Title() string {
	switch k {
	case KindGitHub:
		return "GitHub"
	case KindGitLab:
		return "GitLab"
	case KindCodeberg:
		return "Codeberg"
	case KindGitea:
		return "Gitea"
	}
	return string(k)
}

// AllKinds returns every supported forge kind. Stable ordering for
// UI rendering.
func AllKinds() []Kind {
	return []Kind{KindGitHub, KindGitLab, KindCodeberg, KindGitea}
}

// User represents the authenticated forge account.
type User struct {
	Login string `json:"login"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	URL   string `json:"url,omitempty"`
}

// Repo is a remote repository accessible via the authenticated forge.
type Repo struct {
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch,omitempty"`
	URL           string `json:"url,omitempty"`
	CloneURL      string `json:"clone_url,omitempty"`
	Description   string `json:"description,omitempty"`
	Private       bool   `json:"private,omitempty"`
	Archived      bool   `json:"archived,omitempty"`
	Fork          bool   `json:"fork,omitempty"`
	UpdatedAt     int64  `json:"updated_at,omitempty"` // unix millis
}

// PR represents a pull/merge request.
type PR struct {
	Title        string `json:"title"`
	Body         string `json:"body,omitempty"`
	State        string `json:"state"`
	Author       string `json:"author,omitempty"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	URL          string `json:"url,omitempty"`
	Number       int    `json:"number"`
	CreatedAt    int64  `json:"created_at,omitempty"`
	UpdatedAt    int64  `json:"updated_at,omitempty"`
	Mergeable    bool   `json:"mergeable,omitempty"`
	Draft        bool   `json:"draft,omitempty"`
}

// CreatePRParams describes a PR to create.
type CreatePRParams struct {
	Title        string   `json:"title"`
	Body         string   `json:"body,omitempty"`
	SourceBranch string   `json:"source_branch"`
	TargetBranch string   `json:"target_branch"`
	Labels       []string `json:"labels,omitempty"`
	Draft        bool     `json:"draft,omitempty"`
}

// Issue represents a forge issue.
type Issue struct {
	Title     string   `json:"title"`
	Body      string   `json:"body,omitempty"`
	State     string   `json:"state"`
	Author    string   `json:"author,omitempty"`
	URL       string   `json:"url,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	Number    int      `json:"number"`
	CreatedAt int64    `json:"created_at,omitempty"`
	UpdatedAt int64    `json:"updated_at,omitempty"`
}

// CreateIssueParams describes an issue to create.
type CreateIssueParams struct {
	Title  string   `json:"title"`
	Body   string   `json:"body,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

// Check is a single CI status check for a commit.
type Check struct {
	Name       string `json:"name"`
	Status     string `json:"status"`     // "queued" | "in_progress" | stateCompleted
	Conclusion string `json:"conclusion"` // statusSuccess | statusFailure | "cancelled" | stateSkipped | ""
	URL        string `json:"url,omitempty"`
}

// Release represents a tagged release.
type Release struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name,omitempty"`
	Body        string `json:"body,omitempty"`
	URL         string `json:"url,omitempty"`
	PublishedAt int64  `json:"published_at,omitempty"`
	Draft       bool   `json:"draft,omitempty"`
	Prerelease  bool   `json:"prerelease,omitempty"`
}

// CreateReleaseParams describes a release to create.
type CreateReleaseParams struct {
	TagName    string `json:"tag_name"`
	Name       string `json:"name,omitempty"`
	Body       string `json:"body,omitempty"`
	Target     string `json:"target,omitempty"` // commit SHA or branch
	Draft      bool   `json:"draft,omitempty"`
	Prerelease bool   `json:"prerelease,omitempty"`
}

// Label is a forge label (used on PRs and issues).
type Label struct {
	Name        string `json:"name"`
	Color       string `json:"color,omitempty"`
	Description string `json:"description,omitempty"`
}

// ForgeOps is the unified abstraction over a specific forge backend.
// Each implementation shells out to the corresponding CLI tool.
//
// Methods take a context for cancellation and a host for routing
// (each Provider instance is bound to one host but methods accept
// it explicitly so multi-host instances are possible later).
type ForgeOps interface {
	// Kind returns the backend kind (github/gitlab/gitea).
	Kind() Kind

	// Host returns the forge hostname this provider is bound to.
	Host() string

	// Whoami returns the authenticated account, or an error if not
	// logged in (or the CLI is not installed).
	Whoami(ctx context.Context) (*User, error)

	// ListRepos returns repositories accessible to the authenticated
	// account (owned + member).
	ListRepos(ctx context.Context) ([]Repo, error)

	// ListPRs lists pull/merge requests for repo. state is one of
	// stateOpen, stateClosed, stateMerged, "all".
	ListPRs(ctx context.Context, repo, state string) ([]PR, error)

	// CreatePR opens a new pull/merge request.
	CreatePR(ctx context.Context, repo string, p *CreatePRParams) (*PR, error)

	// MergePR merges an open PR. method is "merge" | mergeSquash | mergeRebase.
	MergePR(ctx context.Context, repo string, number int, method string) error

	// ClosePR closes (without merging) an open PR.
	ClosePR(ctx context.Context, repo string, number int) error

	// ListIssues lists issues for repo. state is stateOpen, stateClosed, "all".
	ListIssues(ctx context.Context, repo, state string) ([]Issue, error)

	// CreateIssue files a new issue.
	CreateIssue(ctx context.Context, repo string, p CreateIssueParams) (*Issue, error)

	// CloseIssue closes an open issue.
	CloseIssue(ctx context.Context, repo string, number int) error

	// CommitStatus returns CI checks for a commit ref (branch / SHA).
	CommitStatus(ctx context.Context, repo, ref string) ([]Check, error)

	// ListReleases returns recent releases for repo.
	ListReleases(ctx context.Context, repo string) ([]Release, error)

	// CreateRelease cuts a new release.
	CreateRelease(ctx context.Context, repo string, p CreateReleaseParams) (*Release, error)

	// ListLabels returns labels defined on repo.
	ListLabels(ctx context.Context, repo string) ([]Label, error)
}

// CmdTimeout is the default per-command timeout. Long enough for
// large repo lists; short enough that a wedged forge doesn't pin
// the request.
const CmdTimeout = 30 * time.Second

// ListTimeout is for paginated listings that may take longer.
const ListTimeout = 60 * time.Second

// ErrNotInstalled signals the backing CLI is not on PATH.
var ErrNotInstalled = errors.New("forges: CLI not installed")

// ErrNotLoggedIn signals the CLI is installed but no auth is configured
// for this host.
var ErrNotLoggedIn = errors.New("forges: not logged in")

// ParseRepo splits a "owner/name" string. Returns an error if the
// format is invalid. Both parts must be non-empty.
func ParseRepo(s string) (owner, name string, err error) {
	parts := strings.SplitN(strings.TrimSpace(s), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo %q (want owner/name)", s)
	}
	return parts[0], parts[1], nil
}
