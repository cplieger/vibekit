// Package forges integrates remote git forges (GitHub, GitLab, Gitea/Codeberg)
// by orchestrating their first-party CLI tools (gh, glab, tea).
//
// Design: the CLI tools own API pagination, error mapping, the git
// credential helpers, AND their own credential stores — vibekit talks
// to each store exclusively through the CLI's documented subcommands
// (login/logout/status; see auth.go and discover.go) and never reads
// or writes another program's config file. The one documented
// exception is glab's read-only discovery parser (glab_config.go),
// kept only because glab ships no machine-readable status output.
// vibekit owns the UX, the unified API surface that the agent and the
// web UI both consume, and the GitHub OAuth device flow
// (oauth/device_flow.go — see login.go for the split). No token-refresh
// path exists on either side: PATs and device-flow tokens are used
// until they expire or are disconnected.
//
// There is no encrypted credential store. The CLIs persist tokens in
// their own stores under the persistent /config volume. The container
// is single-user; file permissions are sufficient.
package forges

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/forges/cliexec"
)

// Kind identifies a forge backend.
type Kind string

// KindGitHub and the following constants define the valid Kind values identifying forge backends.
const (
	KindGitHub   Kind = "github"
	KindGitLab   Kind = "gitlab"
	KindGitea    Kind = "gitea"    // also covers Codeberg (codeberg.org is a Gitea instance)
	KindCodeberg Kind = "codeberg" // synonym for KindGitea, host=codeberg.org
)

// kindMetaEntry holds the per-kind metadata used by the lookup methods.
// Login and Logout are the CLI-native auth verbs (see auth.go); the
// read verb (discovery) lives in discover.go.
type kindMetaEntry struct {
	NewProvider func(Kind, string) ForgeOps
	Login       func(ctx context.Context, host, token string) error
	Logout      func(ctx context.Context, host string) error
	CLI         string
	DefaultHost string
}

// kindMeta is the single source of truth for forge kind properties.
// Adding a new forge requires only a new map entry.
var kindMeta map[Kind]kindMetaEntry

func init() {
	kindMeta = map[Kind]kindMetaEntry{
		KindGitHub: {
			CLI: "gh", DefaultHost: "github.com",
			NewProvider: func(_ Kind, host string) ForgeOps { return newGitHub(host) },
			Login:       loginGH,
			Logout:      logoutGH,
		},
		KindGitLab: {
			CLI: "glab", DefaultHost: "gitlab.com",
			NewProvider: func(_ Kind, host string) ForgeOps { return newGitLab(host) },
			Login:       loginGLab,
			Logout:      logoutGLab,
		},
		KindCodeberg: {
			CLI: cliTea, DefaultHost: "codeberg.org",
			NewProvider: func(k Kind, host string) ForgeOps { return newGitea(k, host) },
			Login:       loginTea,
			Logout:      logoutTea,
		},
		KindGitea: {
			CLI: "tea", DefaultHost: "",
			NewProvider: func(k Kind, host string) ForgeOps { return newGitea(k, host) },
			Login:       loginTea,
			Logout:      logoutTea,
		},
	}
}

// Valid reports whether k is a known forge kind.
func (k Kind) Valid() bool {
	_, ok := kindMeta[k]
	return ok
}

// CLI returns the CLI tool name backing this kind.
func (k Kind) CLI() string {
	return kindMeta[k].CLI
}

// DefaultHost returns the canonical hostname for the kind, or "" if
// no default exists (self-hosted Gitea/Forgejo).
func (k Kind) DefaultHost() string {
	return kindMeta[k].DefaultHost
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

// ListState is a typed enum for PR/issue listing state filters.
type ListState string

// StateOpen and the following constants define the valid ListState filter values for PR and issue listings.
const (
	StateOpen   ListState = "open"
	StateClosed ListState = "closed"
	StateMerged ListState = "merged"
	StateAll    ListState = "all"
)

// MergeMethod is a typed enum for PR merge strategies.
type MergeMethod string

// MergeCommit and the following constants define the valid MergeMethod values for PR merge strategies.
const (
	MergeCommit MergeMethod = "merge"
	MergeSquash MergeMethod = "squash"
	MergeRebase MergeMethod = "rebase"
)

// ForgeOps is the unified abstraction over a specific forge backend.
// Each implementation shells out to the corresponding CLI tool.
//
// Methods take a context for cancellation and a host for routing
// (each Provider instance is bound to one host but methods accept
// it explicitly so multi-host instances are possible later).
//
// Identity (kind + host) deliberately does NOT live here: callers reach a
// provider through Manager.Provider(id), which resolves the persisted
// ConfiguredForge record first, so they already hold both fields. Adding
// identity accessors back would duplicate that record on an ephemeral value.
type ForgeOps interface {
	// Whoami returns the authenticated account, or an error if not
	// logged in (or the CLI is not installed).
	Whoami(ctx context.Context) (*User, error)

	// ListRepos returns repositories accessible to the authenticated
	// account (owned + member).
	ListRepos(ctx context.Context) ([]Repo, error)

	// ListPRs lists pull/merge requests for repo.
	ListPRs(ctx context.Context, repo string, state ListState) ([]PR, error)

	// CreatePR opens a new pull/merge request.
	CreatePR(ctx context.Context, repo string, p *CreatePRParams) (*PR, error)

	// MergePR merges an open PR.
	MergePR(ctx context.Context, repo string, number int, method MergeMethod) error

	// ClosePR closes (without merging) an open PR.
	ClosePR(ctx context.Context, repo string, number int) error

	// ListIssues lists issues for repo.
	ListIssues(ctx context.Context, repo string, state ListState) ([]Issue, error)

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

// cliTea is the gitea/forgejo CLI binary name.
const cliTea = "tea"

// ErrNotInstalled signals the backing CLI is not on PATH. Aliased to
// the cliexec sentinel (which runCmd actually returns) — the package
// previously declared a SEPARATE errors.New with the same message, so
// every errors.Is against this symbol silently never matched.
var ErrNotInstalled = cliexec.ErrNotInstalled

// ErrNotLoggedIn signals the CLI is installed but no auth is configured
// for this host. Aliased for the same reason as ErrNotInstalled.
var ErrNotLoggedIn = cliexec.ErrNotLoggedIn

// ParseRepo splits a "owner/name" string. Returns an error if the
// format is invalid. Both parts must be non-empty.
func ParseRepo(s string) (owner, name string, err error) {
	parts := strings.SplitN(strings.TrimSpace(s), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo %q (want owner/name)", s)
	}
	return parts[0], parts[1], nil
}
