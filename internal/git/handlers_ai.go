package git

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/cplieger/vibekit/internal/api"
)

// AIHandler registers the AI-backed git endpoints (commit-message,
// pr-description). Separated from Handler because these have a
// fundamentally different dependency profile: they need an AI bridge
// but no git subprocess execution beyond basic diff/log.
type AIHandler struct {
	prompter api.UtilityPrompter
	workDir  string
}

// NewAIHandler returns an AIHandler. The prompter must be non-nil.
func NewAIHandler(workDir string, prompter api.UtilityPrompter) *AIHandler {
	return &AIHandler{
		prompter: prompter,
		workDir:  workDir,
	}
}

// RegisterRoutes registers the AI-backed git endpoints.
func (a *AIHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/git/commit-message", a.handleCommitMessage)
	mux.HandleFunc("/api/git/pr-description", a.handlePRDescription)
}

// repoDir resolves a client-supplied repo name against workDir (same
// logic as Handler.repoDir).
func (a *AIHandler) repoDir(repo string) string {
	return resolveRepoDir(a.workDir, repo)
}

// getRecentCommits returns the last n non-merge commits as "hash subject" lines.
func getRecentCommits(ctx context.Context, dir string, n int) string {
	out, err := gitCmd(ctx, dir, "log", "--oneline", "--no-merges",
		"-n"+strconv.Itoa(n))
	if err != nil || strings.TrimSpace(out) == "" {
		return "No commit history available"
	}
	return strings.TrimSpace(out)
}

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

func (a *AIHandler) handleCommitMessage(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body repoBody
	if !decodePostBody(w, r, &body, "bad request") {
		return
	}
	dir := a.repoDir(body.Repo)

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

	result, err := a.prompter.UtilityPrompt(r.Context(), prompt)
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

func (a *AIHandler) handlePRDescription(w http.ResponseWriter, r *http.Request) {
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
	dir := a.repoDir(body.Repo)

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

	result, err := a.prompter.UtilityPrompt(r.Context(), prompt)
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
