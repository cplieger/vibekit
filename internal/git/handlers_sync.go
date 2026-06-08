package git

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/cplieger/vibekit/internal/api"
)

func (h *Handler) handleCommit(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		repoBody

		Message string `json:"message"`
	}
	if !decodePostBody(w, r, &body, "message required") {
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		api.BadRequest(w, "message required")
		return
	}
	dir := h.repoDir(body.Repo)
	slog.Info("git commit", "repo", body.Repo)
	out, err := gitCmd(r.Context(), dir, "commit", "-m", body.Message)
	writeCmdResult(w, out, err)
}

// handlePush runs `git push` with git's default behaviour: no
// --force, no --set-upstream, no --tags. Force-push is intentionally
// not exposed — a user who wants to rewrite published history should
// use the shell. The default also fails cleanly on unconfigured
// upstreams (the error surfaces via writeCmdResult) rather than
// silently guessing a remote/branch, which keeps the contract
// predictable.
func (h *Handler) handlePush(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body repoBody
	decodePostBodyOptional(w, r, &body)
	dir := h.repoDir(body.Repo)
	slog.Info("git push", "repo", body.Repo)
	out, err := h.gitCmdWithCreds(r.Context(), h.timeouts.Push, dir, "", "push")
	writeCmdResult(w, out, err)
}

func (h *Handler) handlePull(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body repoBody
	decodePostBodyOptional(w, r, &body)
	dir := h.repoDir(body.Repo)
	slog.Info("git pull", "repo", body.Repo)
	out, err := h.gitCmdWithCreds(r.Context(), h.timeouts.Push, dir, "", "pull", "--ff-only")
	writeCmdResult(w, out, err)
}

func (h *Handler) handleStash(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body repoBody
	decodePostBodyOptional(w, r, &body)
	dir := h.repoDir(body.Repo)
	slog.Info("git stash", "repo", body.Repo)
	out, err := gitCmd(r.Context(), dir, "stash", "push", "-m", "vibekit auto-stash")
	writeCmdResult(w, out, err)
}

func (h *Handler) handleStashPop(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body repoBody
	decodePostBodyOptional(w, r, &body)
	dir := h.repoDir(body.Repo)
	slog.Info("git stash-pop", "repo", body.Repo)
	out, err := gitCmd(r.Context(), dir, "stash", "pop")
	writeCmdResult(w, out, err)
}
