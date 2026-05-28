package git

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"vibekit/internal/gitexec"

	"vibekit/internal/api"
	"vibekit/internal/fileutil"

	"golang.org/x/sync/errgroup"
)

// maxRepoEntries caps how many directory entries we inspect under
// workDir. A misconfigured or rogue-scripted clone producing tens of
// thousands of entries would otherwise do an O(N) os.Stat sweep on
// every page load; cap + log gives us a visible Loki signal without
// degrading the whole UI.
const maxRepoEntries = 1024

// allRepoStatus mirrors gitStatusResp but adds the repo name so the
// front-end multi-repo dashboard can group by source. Defined here
// (next to the handler that produces a slice of these) rather than
// in handlers.go so the data shape lives next to its first consumer.
type allRepoStatus struct {
	Repo string `json:"repo"`
	gitStatusResp
}

// handleStatusAll fans out collectStatus across every cloned repo
// under workDir (plus workDir itself if it's a repo) and returns a
// merged array. This is what the Changes tab on the new git page
// fetches once per refresh, instead of N round-trips for N repos.
//
// We deliberately skip the network fetch (`fetch --quiet`) inside
// each per-repo collectStatus call: doing N fetches in parallel on
// every page load is too aggressive for slow forges and wastes
// network. The fetch still runs on per-repo refresh through the
// single-repo /api/git/status endpoint.
func (h *Handler) handleStatusAll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	type entry struct {
		name string
		dir  string
	}
	var repos []entry
	if fileutil.IsGitRepo(h.workDir) {
		repos = append(repos, entry{name: ".", dir: h.workDir})
	}
	if entries, err := os.ReadDir(h.workDir); err == nil {
		if len(entries) > maxRepoEntries {
			slog.Warn("git status-all: workDir entry count exceeds cap",
				"path", h.workDir, "count", len(entries), "cap", maxRepoEntries)
			entries = entries[:maxRepoEntries]
		}
		var (
			mu    sync.Mutex
			found []entry
		)
		g, _ := errgroup.WithContext(ctx)
		g.SetLimit(8)
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			name := e.Name()
			dir := filepath.Join(h.workDir, name)
			g.Go(func() error {
				if fileutil.IsGitRepo(dir) {
					mu.Lock()
					found = append(found, entry{name: name, dir: dir})
					mu.Unlock()
				}
				return nil
			})
		}
		_ = g.Wait()
		sort.Slice(found, func(i, j int) bool { return found[i].name < found[j].name })
		repos = append(repos, found...)
	}

	results := make([]allRepoStatus, len(repos))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for i, e := range repos {
		g.Go(func() error {
			st := collectStatus(gctx, e.dir, h.timeouts, &h.fetchFlight, false)
			results[i] = allRepoStatus{Repo: e.name, gitStatusResp: st}
			return nil
		})
	}
	_ = g.Wait()
	api.WriteJSON(w, map[string]any{"repos": results})
}

func (h *Handler) handleRepos(w http.ResponseWriter, r *http.Request) {
	var repos []string
	if fileutil.IsGitRepo(h.workDir) {
		repos = append(repos, ".")
	}
	entries, err := os.ReadDir(h.workDir)
	if err != nil {
		slog.Debug("git repos: read workDir failed", "path", h.workDir, "error", err)
	} else {
		if len(entries) > maxRepoEntries {
			slog.Warn("git repos: workDir entry count exceeds cap",
				"path", h.workDir, "count", len(entries), "cap", maxRepoEntries)
			entries = entries[:maxRepoEntries]
		}
		var (
			mu    sync.Mutex
			found []string
		)
		g, _ := errgroup.WithContext(r.Context())
		g.SetLimit(8)
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			name := e.Name()
			g.Go(func() error {
				if fileutil.IsGitRepo(filepath.Join(h.workDir, name)) {
					mu.Lock()
					found = append(found, name)
					mu.Unlock()
				}
				return nil
			})
		}
		_ = g.Wait()
		sort.Strings(found)
		repos = append(repos, found...)
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
	if strings.HasPrefix(file, "-") ||
		strings.Contains(file, "..") ||
		strings.IndexFunc(file, func(r rune) bool { return r < 0x20 || r == 0x7f }) != -1 ||
		strings.HasPrefix(file, "/") {
		slog.Warn("git file-diff: invalid path rejected", "repo", h.repoDir(repoFromQuery(r)), "path_len", len(file))
		api.BadRequest(w, "invalid path")
		return
	}
	dir := h.repoDir(repoFromQuery(r))
	if !fileutil.IsGitRepo(dir) {
		api.BadRequest(w, "not a git repo")
		return
	}
	// `git diff HEAD -- <path>` gives both staged and unstaged
	// changes for the file in a single unified-diff output. Untracked
	// files have no HEAD entry; fall back to `--no-index` against
	// /dev/null, which renders the file as all-additions.
	out, err := gitCmd(r.Context(), dir, "diff", "HEAD", "--", file)
	if err != nil || strings.TrimSpace(out) == "" {
		// `--no-index` exits non-zero when there's a diff (not an
		// error condition). Capture combined output regardless.
		out2, _ := gitCmd(r.Context(), dir, "diff", "--no-index", "--", "/dev/null", file)
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
	if strings.HasPrefix(file, "-") ||
		strings.Contains(file, "..") ||
		strings.IndexFunc(file, func(r rune) bool { return r < 0x20 || r == 0x7f }) != -1 ||
		strings.HasPrefix(file, "/") {
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

func (h *Handler) handleClone(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if !decodePostBody(w, r, &body, "url required") {
		return
	}
	if body.URL == "" {
		api.BadRequest(w, "url required")
		return
	}
	if !isAllowedRemoteScheme(body.URL) {
		slog.Warn("git clone: invalid scheme rejected", "url", gitexec.ScrubAuth(body.URL))
		api.BadRequest(w, "only https:// and git@ URLs allowed")
		return
	}
	// Defense in depth against git argument-injection CVEs (1000117,
	// 11235, and future variants): pass `--` so git treats the URL
	// strictly as a positional argument even if parsing quirks would
	// otherwise interpret leading dashes as flags. The explicit scheme
	// prefix above already blocks `--flag=...`, but `--` is cheap and
	// makes the guarantee lexical rather than prefix-based.
	slog.Info("git clone", "url", gitexec.ScrubAuth(body.URL))
	cloneCtx, cancel := context.WithTimeout(r.Context(), h.timeouts.Clone)
	defer cancel()
	cmd := gitExec(cloneCtx, h.workDir, "clone", "--", body.URL)
	out, err := cmd.CombinedOutput()
	writeCmdResult(w, strings.TrimSpace(string(out)), err)
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
	dir := h.repoDir(body.Repo)
	if dir == h.workDir {
		api.BadRequest(w, "cannot remove workspace root")
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		slog.Error("git remove: failed", "repo", body.Repo, "error", err)
		api.WriteJSON(w, map[string]string{api.JSONKeyError: "remove failed"})
		return
	}
	slog.Info("git remove", "repo", body.Repo)
	api.Ok(w)
}

// handleReclone deletes the local copy of `repo` and re-clones it from its
// previously-configured origin URL. One-click recovery for divergent
// branches, detached HEAD, merge conflicts, or any other local-only
// mess the user doesn't want to fix by hand. If `repo` is empty or ".",
// we reject it: the workspace root isn't necessarily a git repo and
// accidentally nuking it would be a bad day.
//
// Caveat: this is a destructive operation with an atomicity gap — if
// the clone fails after os.RemoveAll, the user's repo is gone. A
// rename-then-clone-then-delete variant that restores the old tree
// on clone failure is tracked in .review/TODO.md.
func (h *Handler) handleReclone(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body repoBody
	if !decodePostBody(w, r, &body, "repo required") {
		return
	}
	if body.Repo == "" || body.Repo == "." {
		api.BadRequest(w, "re-clone requires a named repo (cannot target workspace root)")
		return
	}
	dir := h.repoDir(body.Repo)
	if dir == h.workDir {
		api.BadRequest(w, "cannot re-clone workspace root")
		return
	}
	if !fileutil.IsGitRepo(dir) {
		api.BadRequest(w, "not a git repo")
		return
	}
	remote, err := gitCmd(r.Context(), dir, "remote", "get-url", "origin")
	if err != nil || remote == "" {
		slog.Warn("git reclone: origin lookup failed", "repo", body.Repo, "error", err)
		api.WriteJSON(w, map[string]string{api.JSONKeyError: "no origin remote"})
		return
	}
	// Defense-in-depth: the origin URL came from git config and could
	// have been set to a non-standard scheme by a prior clone (shared
	// workspace, compromised upstream hook, etc.). Mirror handleClone's
	// scheme allowlist so a re-clone can't silently switch to
	// `file://`, `ext::`, or another transport family. Do this BEFORE
	// os.RemoveAll so a rejected reclone leaves the working tree
	// intact.
	if !isAllowedRemoteScheme(remote) {
		api.WriteJSON(w, map[string]string{api.JSONKeyError: "origin has unsupported scheme for re-clone"})
		return
	}
	slog.Info("git reclone starting", "repo", body.Repo)
	// Nuke and reclone in place. We delete after resolving the URL so a
	// partial delete doesn't strand the repo in an unreclonable state.
	if rmErr := os.RemoveAll(dir); rmErr != nil {
		slog.Error("git reclone: remove failed", "repo", body.Repo, "error", rmErr)
		api.WriteJSON(w, map[string]string{api.JSONKeyError: "remove failed"})
		return
	}
	// `--` barrier: the origin URL came from git config, but a prior
	// malicious clone could have stored a `-flag` there; treating it
	// strictly as a positional argument neutralises that attack.
	cmd := gitExec(r.Context(), h.workDir, "clone", "--", remote, filepath.Base(dir))
	out, cErr := cmd.CombinedOutput()
	if cErr != nil {
		slog.Error("git reclone: clone failed", "repo", body.Repo, "error", cErr, "out", gitexec.ScrubAuth(strings.TrimSpace(string(out))))
	} else {
		slog.Info("git reclone completed", "repo", body.Repo)
	}
	writeCmdResult(w, strings.TrimSpace(string(out)), cErr)
}

// isAllowedRemoteScheme reports whether url uses a transport scheme
// permitted for clone and re-clone operations: https:// or scp-style
// (git@). Restricted to those two to prevent the UI from accidentally
// driving insecure transports (http://) or remote helpers (ext::).
func isAllowedRemoteScheme(url string) bool {
	return strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "git@")
}
