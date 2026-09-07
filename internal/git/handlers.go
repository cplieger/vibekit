package git

import (
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cplieger/pathinside/v2"
	"github.com/cplieger/vibekit/internal/httpreply"
	"golang.org/x/sync/singleflight"
)

// Option configures a Handler at construction time.
type Option func(*Handler)

// Handler implements git HTTP endpoints (non-AI operations).
type Handler struct {
	fetchFlight singleflight.Group
	repoFlight  singleflight.Group
	pullFlight  singleflight.Group
	// statusCache is the dashboard's snapshot holder; see status_cache.go.
	statusCache statusCache
	workDir     string
	timeouts    gitTimeouts
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

// resolveRepoDir resolves a client-supplied repo name against a workDir, rejecting
// escape via a `..` component or an absolute path. Empty, "." and rejected inputs
// fall back to workDir, so the workspace root is the default "repo".
//
// HasDotDot judges the name AS WRITTEN, before it is joined onto anything, because
// there is no root to compare against yet. It is component-precise, so a directory
// whose name merely contains two adjacent dots ("foo..bar") still resolves.
func resolveRepoDir(workDir, repo string) string {
	if repo == "" || repo == "." || pathinside.HasDotDot(repo) || filepath.IsAbs(repo) {
		return workDir
	}
	return filepath.Join(workDir, filepath.Clean(repo))
}

// repoDir resolves repo against h.workDir; see resolveRepoDir.
//
// The check is deliberately LEXICAL-ONLY, so symlinks inside workDir are not
// resolved: a symlinked repo addressed by its symlink name is a feature of this
// surface, and an os.Root would refuse it. Git does not follow symlinks into
// .git/, so this is safe for the reads this package performs.
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

// handlePRFetch fetches a pull request's head ref into a local branch named
// "pr-<number>", or body.Head when set. prRefShape owns which ref shape the
// remote uses.
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
	// body.Head becomes a local branch name, so it needs handleCheckout's ref
	// validation against flag smuggling and ref injection.
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

// prRefShape picks the ref-spec template for the origin URL's host: GitLab uses
// "refs/merge-requests/<n>/head", the GitHub and Gitea family "refs/pull/<n>/head",
// which is also the default for an unknown or unparseable remote. Matching the host
// segment only keeps a GitHub-hosted repo whose path contains "gitlab" on the
// GitHub shape.
func prRefShape(remote string) string {
	host := parseRemoteHost(remote)
	if strings.Contains(host, "gitlab") {
		return "refs/merge-requests/%d/head"
	}
	return "refs/pull/%d/head"
}
