package git

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cplieger/pathinside/v2"
	"github.com/cplieger/vibekit/internal/api"
	"golang.org/x/sync/singleflight"
)

// Compile-time interface assertion.
var _ api.RouteHandler = (*Handler)(nil)

// Option configures a Handler at construction time.
type Option func(*Handler)

// Handler implements git HTTP endpoints (non-AI operations).
type Handler struct {
	fetchFlight  singleflight.Group
	repoFlight   singleflight.Group
	statusFlight singleflight.Group
	workDir      string
	timeouts     gitTimeouts
}

// NewHandler returns a Handler scoped to workDir.
func NewHandler(workDir string, opts ...Option) *Handler {
	h := &Handler{workDir: workDir, timeouts: defaultTimeouts()}
	for _, o := range opts {
		o(h)
	}
	return h
}

// RegisterRoutes installs the /api/git/* mux entries.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/git/repos", h.handleRepos)
	mux.HandleFunc("/api/git/status", h.handleStatus)
	mux.HandleFunc("/api/git/status-all", h.handleStatusAll)
	mux.HandleFunc("/api/git/stage", h.handleStage)
	mux.HandleFunc("/api/git/unstage", h.handleUnstage)
	mux.HandleFunc("/api/git/discard", h.handleDiscard)
	mux.HandleFunc("/api/git/commit", h.handleCommit)
	mux.HandleFunc("/api/git/push", h.handlePush)
	mux.HandleFunc("/api/git/pull", h.handlePull)
	mux.HandleFunc("/api/git/show", h.handleShow)
	mux.HandleFunc("/api/git/file-diff", h.handleFileDiff)
	mux.HandleFunc("/api/git/clone", h.handleClone)
	mux.HandleFunc("/api/git/log", h.handleLog)
	mux.HandleFunc("/api/git/branches", h.handleBranches)
	mux.HandleFunc("/api/git/checkout", h.handleCheckout)
	mux.HandleFunc("/api/git/stash", h.handleStash)
	mux.HandleFunc("/api/git/stash-pop", h.handleStashPop)
	mux.HandleFunc("/api/git/remove", h.handleRemove)
	mux.HandleFunc("/api/git/reclone", h.handleReclone)
	mux.HandleFunc("/api/git/pr-fetch", h.handlePRFetch)
}

// --- helpers ---

// resolveRepoDir resolves a client-supplied repo name against a workDir,
// rejecting attempts to escape the workspace root via a `..` component or
// an absolute path. Falls back to workDir for empty / "." / invalid
// inputs so the workspace root is the default "repo".
//
// The traversal test is pathinside.HasDotDot: the name is judged AS
// WRITTEN, before it is joined onto anything, which is the syntactic-
// hygiene axis rather than the containment one (there is no root to
// compare against yet). It is component-precise, so a directory whose
// NAME merely contains two adjacent dots ("foo..bar") now resolves as
// the repo it is instead of silently falling back to workDir.
func resolveRepoDir(workDir, repo string) string {
	if repo == "" || repo == "." || pathinside.HasDotDot(repo) || filepath.IsAbs(repo) {
		return workDir
	}
	return filepath.Join(workDir, filepath.Clean(repo))
}

// repoDir resolves a client-supplied repo name against h.workDir,
// rejecting attempts to escape the workspace root via a `..` component
// or an absolute path. Falls back to workDir for empty / "." / invalid
// inputs so the workspace root is the default "repo".
//
// Symlinks inside workDir are NOT resolved: users may symlink a real
// git repo into the workspace and expect the UI to address it by its
// symlink name. Git itself doesn't follow symlinks into .git/, so
// this is safe for the read operations this package performs. Write
// operations in the hub's fs bridge resolve symlinks via EvalSymlinks
// — that path has stricter requirements. The check is therefore
// deliberately LEXICAL-ONLY: an os.Root here would refuse the
// symlinked repos that are a feature of this surface.
//
// The `..` check is component-precise (pathinside.HasDotDot), so the
// old over-refusal of legitimate directory names containing ".."
// (e.g. "foo..bar") is gone; a real `..` segment is still rejected.
func (h *Handler) repoDir(repo string) string {
	return resolveRepoDir(h.workDir, repo)
}

// repoDirForDelete is repoDir plus the containment check a DESTRUCTIVE caller
// needs. It returns "" for a path it will not vouch for.
//
// It exists because the lexical-only rule above is justified on a premise that
// does not cover deletion: "safe for the read operations this package performs".
//
// What it checks is the resolved PARENT, and that is the whole subtlety.
// os.RemoveAll does not follow a symlink at the FINAL component (it unlinks the
// link itself), so a repo the user symlinked into the workspace is already safe to
// name directly and must stay addressable — refusing it would break the feature
// the lexical rule exists for. Every INTERMEDIATE component is resolved by the
// kernel during traversal, so with /workspace/link pointing outside,
// {"repo":"link/victim"} is lexically clean (no `..`, not absolute, under workDir
// as written) and deletes <target>/victim. Checking the parent refuses exactly
// that and nothing else.
//
// The returned path is the LEXICAL one, not the resolved target, so RemoveAll
// still unlinks a named symlink rather than deleting what it points at.
func (h *Handler) repoDirForDelete(repo string) string {
	dir := resolveRepoDir(h.workDir, repo)
	root, err := filepath.EvalSymlinks(h.workDir)
	if err != nil {
		slog.Warn("git remove: cannot resolve workspace root", "error", err)
		return ""
	}
	// The workspace root itself resolves to root and has no parent to check; the
	// caller refuses it separately, because "remove the workspace" is a different
	// refusal from "that path escapes".
	if dir == h.workDir {
		return dir
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(dir))
	if err != nil {
		slog.Warn("git remove: cannot resolve the repo's parent", "repo", repo, "error", err)
		return ""
	}
	if parent != root && !pathinside.Root(root).Contains(parent) {
		slog.Warn("git remove: refusing a path whose parent resolves outside the workspace",
			"repo", repo, "resolved_parent", parent)
		return ""
	}
	return dir
}

func repoFromQuery(r *http.Request) string { return r.URL.Query().Get("repo") }

type repoBody struct {
	Repo string `json:"repo"`
}

type gitStatusResp struct {
	Branch   string    `json:"branch"`
	Remote   string    `json:"remote"`
	Files    []gitFile `json:"files"`
	Ahead    int       `json:"ahead"`
	Behind   int       `json:"behind"`
	Stashes  int       `json:"stashes"`
	IsRepo   bool      `json:"is_repo"`
	HasGH    bool      `json:"has_gh"`
	HasDirty bool      `json:"has_dirty"`
}

func parseGitStatus(ctx context.Context, dir string) []gitFile {
	raw, err := gitExec(ctx, dir, "status", "--porcelain=v1", "-z", "-uall").CombinedOutput()
	if err != nil {
		// Cancellation (per-repo budget, server shutdown) SIGKILLs the
		// subprocess — an expected partial result, not a git failure;
		// keep WARN for genuine errors only so a busy dashboard doesn't
		// flood the log with kill noise.
		if ctx.Err() != nil {
			slog.Debug("git status canceled", "repo", dir, "cause", ctx.Err())
			return nil
		}
		slog.Warn("git status failed", "repo", dir, "error", err, "out", scrubAuth(string(raw)))
		return nil
	}
	return parseGitStatusOutput(raw)
}

// handlePRFetch fetches a pull request's head ref into a local branch
// named "pr-<number>". Works for GitHub + Gitea family (both expose
// pull/{n}/head refs); GitLab's equivalent is merge-requests/{n}/head.
// We derive the ref shape from the remote URL.
//
// Payload: {"repo": "name", "number": 42, "head": "feature-branch"}.
// "head" is a best-effort label for the local branch — when set, we
// use it as the local branch name; otherwise we fall back to
// pr-<number>.
func (h *Handler) handlePRFetch(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		Repo   string `json:"repo"`
		Head   string `json:"head"`
		Number int    `json:"number"`
	}
	if !decodePostBody(w, r, &body, "bad request") {
		return
	}
	if body.Number <= 0 || body.Number > 10_000_000 {
		slog.Warn("git pr-fetch: invalid PR number rejected", "repo", body.Repo, "number", body.Number)
		api.BadRequest(w, "invalid PR number")
		return
	}
	// body.Head is optional; when set it's used as a local branch name,
	// so apply the same validation as handleCheckout via the shared
	// isValidGitRef helper to block flag smuggling and ref-injection.
	if body.Head != "" && !isValidGitRef(body.Head) {
		slog.Warn("git pr-fetch: invalid head rejected", "repo", body.Repo, "number", body.Number, "head", body.Head)
		api.BadRequest(w, "invalid head name")
		return
	}
	dir := h.repoDir(body.Repo)
	remote, err := gitCmd(r.Context(), dir, subRemote, "get-url", "origin")
	if err != nil {
		slog.Warn("git pr-fetch: origin lookup failed", "repo", body.Repo, "pr", body.Number, "error", err)
		writeCmdResult(w, remote, err)
		return
	}
	refShape := prRefShape(remote)
	local := "pr-" + strconv.Itoa(body.Number)
	if body.Head != "" {
		local = body.Head
	}
	args := []string{"fetch", "origin", fmt.Sprintf(refShape, body.Number) + ":" + local}
	slog.Info("git pr-fetch", "repo", body.Repo, "number", body.Number, "local", local)
	out, err := h.gitCmdWithCreds(r.Context(), h.timeouts.Push, dir, remote, args...)
	writeCmdResult(w, out, err)
}

// prRefShape picks the right ref-spec template based on the origin
// URL host. GitHub + Gitea/Gogs/Forgejo expose "refs/pull/<n>/head";
// GitLab uses "refs/merge-requests/<n>/head". We match on the host
// segment only (not the full URL) so a GitHub-hosted repo whose path
// contains the substring "gitlab" (e.g. github.com/gitlab/tooling)
// still picks the GitHub shape. For unknown or unparseable remotes
// we default to the GitHub shape — it's the most common and a failed
// fetch surfaces a clear error to the user.
func prRefShape(remote string) string {
	host := parseRemoteHost(remote)
	if strings.Contains(host, "gitlab") {
		return "refs/merge-requests/%d/head"
	}
	return "refs/pull/%d/head"
}
