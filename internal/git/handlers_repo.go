package git

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/fileutil"
	"github.com/cplieger/vibekit/internal/gitexec"
	"golang.org/x/sync/errgroup"
)

// allRepoStatus mirrors gitStatusResp but adds the repo name so the
// front-end multi-repo dashboard can group by source. Defined here
// (next to the handler that produces a slice of these) rather than
// in handlers.go so the data shape lives next to its first consumer.
type allRepoStatus struct {
	Repo string `json:"repo"`
	gitStatusResp
}

// statusAllBudget bounds one full status-all scan; perRepoBudget bounds
// each repo inside it so a single wedged repo degrades to a partial row
// instead of stalling the whole dashboard.
const (
	statusAllBudget = 30 * time.Second
	perRepoBudget   = 10 * time.Second
)

// handleStatusAll fans out collectStatus across every cloned repo
// under workDir (plus workDir itself if it's a repo) and returns a
// merged array. This is what the Changes tab on the new git page
// fetches once per refresh, instead of N round-trips for N repos.
//
// The scan is singleflighted and DETACHED from the request context:
// boot fires several concurrent callers (changes tab + badge poll),
// and a client-side abort used to cancel the shared context mid-scan,
// SIGKILLing every in-flight `git status` and logging a WARN burst per
// repo. Now concurrent callers join one scan, an abandoned scan runs
// to completion (bounded by statusAllBudget), and the next poll gets a
// fast answer.
//
// By default the network fetch (`fetch --quiet`) is skipped inside each
// per-repo collectStatus call: doing N fetches in parallel on every
// 15s badge poll is too aggressive for slow forges. ?fetch=1 (the
// user-initiated "Refresh all" / git-tab activation) opts in so
// ahead/behind counts are actually refreshed against the remotes —
// without it `behind` went permanently stale because no UI path ever
// fetched. Fetching callers get their own singleflight key so a
// cheap poll never piggybacks a fetch-less result onto them (and vice
// versa).
func (h *Handler) handleStatusAll(w http.ResponseWriter, r *http.Request) {
	doFetch := r.URL.Query().Get("fetch") == "1"
	key := "status-all"
	if doFetch {
		key = "status-all-fetch"
	}
	v, _, _ := h.statusFlight.Do(key, func() (any, error) {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), statusAllBudget)
		defer cancel()
		repos := h.cachedDiscoverRepos(sctx)
		results := make([]allRepoStatus, len(repos))
		g, gctx := errgroup.WithContext(sctx)
		g.SetLimit(8)
		for i, e := range repos {
			g.Go(func() error {
				rctx, rcancel := context.WithTimeout(gctx, perRepoBudget)
				defer rcancel()
				st := collectStatus(rctx, e.Dir, h.timeouts, &h.fetchFlight, doFetch)
				results[i] = allRepoStatus{Repo: e.Name, gitStatusResp: st}
				return nil
			})
		}
		_ = g.Wait()
		return results, nil
	})
	results, _ := v.([]allRepoStatus)
	// Treated as read-only by every singleflight sharer.
	api.WriteJSON(w, map[string]any{"repos": results})
}

func (h *Handler) handleRepos(w http.ResponseWriter, r *http.Request) {
	discovered := h.cachedDiscoverRepos(r.Context())
	repos := make([]string, len(discovered))
	for i, d := range discovered {
		repos[i] = d.Name
	}
	api.WriteJSON(w, map[string]any{"repos": repos})
}

// handleFileDiff returns the working-tree diff for a single file
// against HEAD. Used by the Changes tab's inline diff viewer.
//
// Path validation mirrors handleShow: relative paths only, no
// traversal, no leading `-`, no control bytes.
func (h *Handler) handleFileDiff(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("path")
	if file == "" {
		api.BadRequest(w, "path required")
		return
	}
	if !validateFilePath(file) {
		slog.Warn("git file-diff: invalid path rejected", "repo", h.repoDir(repoFromQuery(r)), "path_len", len(file))
		api.BadRequest(w, "invalid path")
		return
	}
	dir := h.repoDir(repoFromQuery(r))
	if !fileutil.IsGitRepo(r.Context(), dir) {
		api.BadRequest(w, msgNotAGitRepo)
		return
	}
	// `git diff HEAD -- <path>` gives both staged and unstaged
	// changes for the file in a single unified-diff output. Untracked
	// files have no HEAD entry; fall back to `--no-index` against
	// /dev/null, which renders the file as all-additions.
	//
	// --no-textconv on both: a repo's diff.<driver>.textconv is a command git
	// runs to render a path it selected via .gitattributes. The --no-index
	// call needs it just as much, and less obviously — "outside the index"
	// does not mean outside the attributes, which are read from the working
	// tree either way.
	out, err := gitCmd(r.Context(), dir, "diff", "--no-textconv", "HEAD", "--", file)
	if err != nil || strings.TrimSpace(out) == "" {
		// `--no-index` exits non-zero when there's a diff (not an
		// error condition). Capture combined output regardless.
		out2, _ := gitCmd(r.Context(), dir, "diff", "--no-textconv", "--no-index", "--", "/dev/null", file)
		if strings.TrimSpace(out2) != "" {
			out = out2
		}
	}
	api.WriteJSON(w, map[string]string{"diff": out})
}

func (h *Handler) handleShow(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("path")
	if file == "" {
		api.BadRequest(w, "path required")
		return
	}
	// Reject path traversal and flag smuggling. The client only
	// sends relative paths from `git status` output; `..` and
	// control bytes never appear in legitimate use and keep log
	// lines clean. Reject the full ASCII control range (including
	// tab, ESC) plus DEL so slog/Loki readers see readable values
	// and no invisible bytes survive into downstream tooling.
	if !validateFilePath(file) {
		slog.Warn("git show: invalid path rejected", "repo", h.repoDir(repoFromQuery(r)), "path_len", len(file))
		api.BadRequest(w, "invalid path")
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = refHEAD
	}
	if !isValidGitRef(ref) {
		slog.Warn("git show: invalid ref rejected", "repo", h.repoDir(repoFromQuery(r)), "ref", ref)
		api.BadRequest(w, "invalid ref")
		return
	}
	dir := h.repoDir(repoFromQuery(r))
	out, err := gitShowCmd(r.Context(), dir, ref, file)
	if err != nil {
		if errors.Is(err, ErrPathNotInRef) {
			// File didn't exist at ref — return empty content so the
			// diff renders as all-add for new files.
			api.WriteJSON(w, map[string]string{"content": ""})
			return
		}
		slog.Warn("git show failed", "repo", dir, "ref", ref, "path", file, "error", err, "out", gitexec.ScrubAuth(out))
		writeGitError(w, KindShowFailed, gitexec.ScrubAuth(out))
		return
	}
	api.WriteJSON(w, map[string]string{"content": out})
}

func (h *Handler) handleLog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	dir := h.repoDir(repoFromQuery(r))
	// Show remote branch log if available, fall back to local
	ref := refHEAD
	if branch, err := gitCmd(ctx, dir, "branch", "--show-current"); err == nil && branch != "" {
		if _, err := gitCmd(ctx, dir, "rev-parse", "--verify", "origin/"+branch); err == nil {
			ref = "origin/" + branch
		}
	}
	out, err := gitCmd(ctx, dir, "log", ref, "--oneline", "-20", "--no-decorate")
	if err != nil {
		slog.Debug("git log failed", "repo", dir, "ref", ref, "error", err, "out", gitexec.ScrubAuth(out))
		api.WriteJSON(w, map[string]any{"entries": []string{}, "remote": "", "behind": 0})
		return
	}
	lines := []string{}
	for line := range strings.SplitSeq(out, "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	remote, rErr := gitCmd(ctx, dir, "remote", "get-url", "origin")
	if rErr != nil {
		slog.Debug("git remote get-url failed during log", "repo", dir, "error", rErr)
	}
	behind := 0
	if ab, err := gitCmd(ctx, dir, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"); err == nil {
		parts := strings.Fields(ab)
		if len(parts) == 2 {
			if n, berr := strconv.Atoi(parts[1]); berr == nil {
				behind = n
			}
		}
	}
	api.WriteJSON(w, map[string]any{"entries": lines, "remote": gitexec.ScrubAuth(remote), "behind": behind})
}

func (h *Handler) handleBranches(w http.ResponseWriter, r *http.Request) {
	dir := h.repoDir(repoFromQuery(r))
	out, err := gitCmd(r.Context(), dir, "branch", "-a", "--format=%(refname:short)\t%(HEAD)")
	if err != nil {
		api.WriteJSON(w, map[string]any{"branches": []any{}, "current": ""})
		return
	}
	type branchEntry struct {
		Name    string `json:"name"`
		Current bool   `json:"current"`
	}
	branches := []branchEntry{}
	var current string
	for line := range strings.SplitSeq(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		name := parts[0]
		isCurrent := len(parts) > 1 && strings.TrimSpace(parts[1]) == "*"
		branches = append(branches, branchEntry{Name: name, Current: isCurrent})
		if isCurrent {
			current = name
		}
	}
	api.WriteJSON(w, map[string]any{"branches": branches, "current": current})
}

func (h *Handler) handleCheckout(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		repoBody

		Branch string `json:"branch"`
		Create bool   `json:"create"`
	}
	if !decodePostBody(w, r, &body, "branch required") {
		return
	}
	if body.Branch == "" {
		api.BadRequest(w, "branch required")
		return
	}
	// Reject branch names that look like options or contain characters
	// forbidden by git-check-ref-format. The `--` barrier that makes
	// most git subcommands safe can't be used here because
	// `git checkout -- <name>` changes the semantics to "restore file",
	// not "switch branch".
	if !isValidGitRef(body.Branch) {
		slog.Warn("git checkout: invalid branch rejected", "repo", body.Repo, "branch", body.Branch)
		api.BadRequest(w, "invalid branch name")
		return
	}
	dir := h.repoDir(body.Repo)
	args := []string{"checkout"}
	if body.Create {
		args = append(args, "-b")
	}
	args = append(args, body.Branch)
	slog.Info("git checkout", "repo", body.Repo, "branch", body.Branch, "create", body.Create)
	out, err := gitCmd(r.Context(), dir, args...)
	writeCmdResult(w, out, err)
}

func (h *Handler) handleRemove(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body repoBody
	if !decodePostBody(w, r, &body, "repo name required (cannot remove workspace root)") {
		return
	}
	if body.Repo == "" || body.Repo == "." {
		api.BadRequest(w, "repo name required (cannot remove workspace root)")
		return
	}
	// The resolved variant, because this is the one handler that DELETES: see
	// repoDirForDelete for the intermediate-symlink escape the lexical resolver
	// cannot see. It answers "" for a path it will not vouch for.
	dir := h.repoDirForDelete(body.Repo)
	if dir == "" {
		api.BadRequest(w, "that repo path is not inside the workspace")
		return
	}
	if dir == h.workDir {
		api.BadRequest(w, "cannot remove workspace root")
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		slog.Error("git remove: failed", "repo", body.Repo, "error", err)
		api.WriteJSON(w, api.ErrorJSON("remove failed"))
		return
	}
	slog.Info("git remove", "repo", body.Repo)
	api.Ok(w)
}
