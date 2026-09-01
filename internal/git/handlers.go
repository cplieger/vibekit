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
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/logsafe"
	"golang.org/x/sync/singleflight"
)

// Option configures a Handler at construction time.
type Option func(*Handler)

// Handler implements git HTTP endpoints (non-AI operations).
type Handler struct {
	fetchFlight  singleflight.Group
	repoFlight   singleflight.Group
	statusFlight singleflight.Group
	pullFlight   singleflight.Group
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
	mux.HandleFunc("/api/git/pull-all", h.handlePullAll)
	mux.HandleFunc("/api/git/show", h.handleShow)
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

// resolveRepoDir resolves a client-supplied repo name against a workDir,
// rejecting attempts to escape the workspace root via a `..` component or
// an absolute path. Falls back to workDir for empty / "." / invalid
// inputs so the workspace root is the default "repo".
//
// The traversal test is pathinside.HasDotDot: the name is judged AS
// WRITTEN, before it is joined onto anything, since there is no root to
// compare against yet. It is component-precise, so a directory whose
// NAME merely contains two adjacent dots ("foo..bar") resolves as the
// repo it is instead of falling back to workDir.
func resolveRepoDir(workDir, repo string) string {
	if repo == "" || repo == "." || pathinside.HasDotDot(repo) || filepath.IsAbs(repo) {
		return workDir
	}
	return filepath.Join(workDir, filepath.Clean(repo))
}

// repoDir resolves repo against h.workDir; see resolveRepoDir.
//
// Symlinks inside workDir are NOT resolved: users may symlink a real
// git repo into the workspace and expect the UI to address it by its
// symlink name. Git itself doesn't follow symlinks into .git/, so
// this is safe for the read operations this package performs; the
// runtime's fs bridge resolves symlinks via EvalSymlinks for writes,
// which has stricter requirements. The check here is deliberately
// LEXICAL-ONLY: an os.Root would refuse the symlinked repos that are
// a feature of this surface.
func (h *Handler) repoDir(repo string) string {
	return resolveRepoDir(h.workDir, repo)
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
		slog.Warn("git status failed", "repo", logsafe.Field(dir), "error", logsafe.Field(err.Error()), "out", scrubAuth(string(raw)))
		return nil
	}
	return parseGitStatusOutput(raw)
}

// handlePRFetch fetches a pull request's head ref into a local branch
// named "pr-<number>" (or body.Head, if set). Works for GitHub + Gitea
// family (both expose pull/{n}/head refs); GitLab's equivalent is
// merge-requests/{n}/head — prRefShape derives the ref shape from the
// remote URL.
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
		httpreply.BadRequest(w, "invalid PR number")
		return
	}
	// body.Head is optional; when set it's used as a local branch name,
	// so apply the same validation as handleCheckout via the shared
	// isValidGitRef helper to block flag smuggling and ref-injection.
	if body.Head != "" && !isValidGitRef(body.Head) {
		slog.Warn("git pr-fetch: invalid head rejected", "repo", body.Repo, "number", body.Number, "head", body.Head)
		httpreply.BadRequest(w, "invalid head name")
		return
	}
	dir := h.repoDir(body.Repo)
	remote, err := gitCmd(r.Context(), dir, subRemote, "get-url", remoteOrigin)
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
	args := []string{subFetch, remoteOrigin, fmt.Sprintf(refShape, body.Number) + ":" + local}
	slog.Info("git pr-fetch", "repo", body.Repo, "number", body.Number, "local", local)
	out, err := gitCmdWithCreds(r.Context(), h.timeouts.Push, dir, remote, args...)
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
