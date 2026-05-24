package git

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"vibekit/internal/api"
	"vibekit/internal/gitexec"

	"golang.org/x/sync/singleflight"
)

// Compile-time interface assertion.
var _ api.GitHandler = (*Handler)(nil)

// Option configures a Handler at construction time.
type Option func(*Handler)

// WithUtilityPrompt wires the AI utility bridge at construction,
// eliminating the half-initialized window. When nil or not provided,
// AI-backed endpoints return a clear error response.
func WithUtilityPrompt(p api.UtilityPrompter) Option {
	return func(h *Handler) {
		h.prompter = p
	}
}

// Handler implements git HTTP endpoints.
type Handler struct {
	prompter    api.UtilityPrompter
	fetchFlight singleflight.Group
	workDir     string
	timeouts    gitexec.Timeouts
}

// NewHandler returns a Handler scoped to workDir. AI-backed endpoints
// (commit-message, pr-description) require WithUtilityPrompt; without
// it they return a clear error response.
func NewHandler(workDir string, opts ...Option) *Handler {
	h := &Handler{workDir: workDir, timeouts: gitexec.DefaultTimeouts()}
	for _, o := range opts {
		o(h)
	}
	return h
}

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
	mux.HandleFunc("/api/git/commit-message", h.handleCommitMessage)
	mux.HandleFunc("/api/git/pr-description", h.handlePRDescription)
	mux.HandleFunc("/api/git/pr-fetch", h.handlePRFetch)
}

// --- helpers ---

// repoDir resolves a client-supplied repo name against h.workDir,
// rejecting attempts to escape the workspace root via `..` or
// absolute paths. Falls back to workDir for empty / "." / invalid
// inputs so the workspace root is the default "repo".
//
// Symlinks inside workDir are NOT resolved: users may symlink a real
// git repo into the workspace and expect the UI to address it by its
// symlink name. Git itself doesn't follow symlinks into .git/, so
// this is safe for the read operations this package performs. Write
// operations in the hub's fs bridge resolve symlinks via EvalSymlinks
// — that path has stricter requirements.
//
// The `..` check is lexical and will reject legitimate directory
// names containing ".." (e.g. "foo..bar"). Accepted tradeoff; such
// names are vanishingly rare.
func (h *Handler) repoDir(repo string) string {
	if repo == "" || repo == "." || strings.Contains(repo, "..") || filepath.IsAbs(repo) {
		return h.workDir
	}
	return filepath.Join(h.workDir, filepath.Clean(repo))
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
	raw, err := gitExec(ctx, dir, "status", "--porcelain", "-uall").CombinedOutput()
	if err != nil {
		slog.Warn("git status failed", "repo", dir, "error", err, "out", gitexec.ScrubAuth(string(raw)))
		return nil
	}
	return parseGitStatusOutput(raw)
}
