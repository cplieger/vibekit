package git

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"vibekit/internal/api"
	"vibekit/internal/gitexec"
)

// diffTruncatedSuffix is the canonical suffix appended when a diff
// exceeds the byte cap for prompt construction.
const diffTruncatedSuffix = "\n\n[Diff truncated due to size]"

// truncateDiff caps diff at maxBytes bytes, appending the canonical suffix
// when truncation occurs.
func truncateDiff(diff string, maxBytes int) string {
	if len(diff) > maxBytes {
		return diff[:maxBytes] + diffTruncatedSuffix
	}
	return diff
}

func (h *Handler) handleCommitMessage(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body repoBody
	if !decodePostBody(w, r, &body, "bad request") {
		return
	}
	// Fail fast when the utility bridge isn't wired: the AI-backed
	// endpoints require a UtilityPrompter passed via WithUtilityPrompt
	// at construction time. Skip the git subprocesses below — their
	// output would only be used to build a prompt we can't send.
	if h.prompter == nil {
		api.WriteJSONStatus(w, http.StatusServiceUnavailable, map[string]string{api.JSONKeyError: api.ErrMsgUtilityUnavailable})
		return
	}
	dir := h.repoDir(body.Repo)

	// Check for staged changes.
	diff, err := gitCmd(r.Context(), dir, "diff", "--cached", "--stat")
	if err != nil || strings.TrimSpace(diff) == "" {
		writeGitError(w, KindNoStaged, "")
		return
	}

	// Get the full diff, capped at 8KB.
	fullDiff, dErr := gitCmd(r.Context(), dir, "diff", "--cached")
	if dErr != nil {
		fullDiff = diff
	}
	fullDiff = truncateDiff(fullDiff, 8*1024)

	// Get recent commit history for pattern matching.
	commitHistory := getRecentCommits(r.Context(), dir, 10)

	prompt := buildCommitPrompt(commitHistory, fullDiff)

	result, err := h.prompter.UtilityPrompt(r.Context(), prompt)
	if err != nil {
		slog.Error("commit message generation failed", "error", err)
		writeGitError(w, KindGenerationFailed, err.Error())
		return
	}

	msg := extractCommitMessage(result)
	api.WriteJSON(w, map[string]string{jsonKeyOutput: msg})
}

// defaultPRBase is the assumed base branch when a PR-description
// request doesn't supply one.
const defaultPRBase = "main"

func (h *Handler) handlePRDescription(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	}
	if !decodePostBody(w, r, &body, "bad request") {
		return
	}
	// Fail fast when the utility bridge isn't wired (see
	// handleCommitMessage for the rationale).
	if h.prompter == nil {
		api.WriteJSONStatus(w, http.StatusServiceUnavailable, map[string]string{api.JSONKeyError: api.ErrMsgUtilityUnavailable})
		return
	}
	dir := h.repoDir(body.Repo)

	// Get the base branch (default: main).
	base := defaultPRBase
	if body.Branch != "" {
		if !isValidGitRef(body.Branch) {
			slog.Warn("git pr-description: invalid branch rejected",
				"repo", body.Repo, "branch", body.Branch)
			api.BadRequest(w, "invalid branch name")
			return
		}
		base = body.Branch
	}

	// Get the diff between current branch and base.
	diff, err := gitCmd(r.Context(), dir, "diff", base+"...HEAD")
	if err != nil || strings.TrimSpace(diff) == "" {
		// Try origin/main if local main doesn't exist.
		diff, err = gitCmd(r.Context(), dir, "diff", "origin/"+base+"...HEAD")
		if err != nil || strings.TrimSpace(diff) == "" {
			writeGitError(w, KindNoChanges, "against "+base)
			return
		}
	}

	// Cap diff at 12KB for PR descriptions (larger than commit messages).
	diff = truncateDiff(diff, 12*1024)

	// Get commit log for the branch.
	log, err := gitCmd(r.Context(), dir, "log", "--oneline", base+"..HEAD")
	if err != nil || strings.TrimSpace(log) == "" {
		if fallbackLog, fallbackErr := gitCmd(r.Context(), dir, "log", "--oneline", "origin/"+base+"..HEAD"); fallbackErr == nil {
			log = fallbackLog
		}
	}

	prompt := buildPRPrompt(log, diff)

	result, err := h.prompter.UtilityPrompt(r.Context(), prompt)
	if err != nil {
		slog.Error("PR description generation failed", "error", err)
		writeGitError(w, KindGenerationFailed, err.Error())
		return
	}

	// Clean up: strip markdown fences (including language-tagged
	// variants like ```markdown / ```diff) via the shared helper so
	// this flow stays in sync with extractCommitMessage.
	result = strings.TrimSpace(result)
	result = api.StripCodeFence(result)
	result = strings.TrimSpace(result)

	api.WriteJSON(w, map[string]string{jsonKeyOutput: result})
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
	remote, err := gitCmd(r.Context(), dir, "remote", "get-url", "origin")
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
	host := gitexec.ParseRemoteHost(remote)
	if strings.Contains(host, "gitlab") {
		return "refs/merge-requests/%d/head"
	}
	return "refs/pull/%d/head"
}
