package git

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/logsafe"
	"github.com/cplieger/vibekit/internal/workspace"
	"github.com/cplieger/webhttp/v2"
	"golang.org/x/sync/errgroup"
)

// allRepoStatus mirrors gitStatusResp but adds the repo name so the
// front-end multi-repo dashboard can group by source.
type allRepoStatus struct {
	Repo string `json:"repo"`
	gitStatusResp
}

// Scan budgets. statusScanBudget bounds one full scan; perRepoBudget bounds each
// repo inside it so a single wedged repo degrades to a partial row instead of
// stalling the whole dashboard. statusMaxAge is how old a snapshot may be before
// a read refreshes behind its answer — under the client's 15s poll, so an idle
// poll finds a snapshot it can answer from AND leaves a fresh one behind.
// statusColdWait bounds the one read that has nothing to answer with.
const (
	statusScanBudget = 30 * time.Second
	perRepoBudget    = 10 * time.Second
	statusMaxAge     = 10 * time.Second
	statusColdWait   = perRepoBudget
	// scanConcurrency caps repos scanned in parallel. Each is two short-lived git
	// subprocesses, so this bounds fork+exec pressure rather than CPU.
	scanConcurrency = 8
)

// handleStatusAll answers the multi-repo dashboard from the newest completed scan
// plus its age, and refreshes behind the answer (status_cache.go says why).
//
// Two callers still wait, and only they: the FIRST read of a process, which has
// nothing to answer with, and `?fetch=1`, the "Refresh all" gesture whose whole
// point is fresh data. The plain poll skips the network fetch deliberately — N
// parallel fetches every 15 seconds is too aggressive for slow forges — and every
// scan is detached from the request, so an abandoned poll still leaves a snapshot.
func (h *Handler) handleStatusAll(w http.ResponseWriter, r *http.Request) {
	doFetch := r.URL.Query().Get("fetch") == "1"
	key := statusKeyPoll
	if doFetch {
		key = statusKeyFetch
	}
	snap, running := h.statusCache.read(key)
	if doFetch || snap.stale(statusMaxAge) {
		running = h.refreshStatusAll(r, key, doFetch)
	}
	if wait := h.coldWait(snap, doFetch); wait > 0 {
		snap = h.awaitStatusAll(r, key, running, wait)
		_, running = h.statusCache.read(key)
	}
	webhttp.WriteJSON(w, statusAllResp{
		Repos:    snap.rows(),
		AgeMS:    snap.age().Milliseconds(),
		Scanning: running != nil,
	})
}

// statusAllResp is the dashboard's answer: the newest completed scan, how old it
// is, and whether one is running behind it.
//
// The age is what lets a client show data it knows is a few seconds old instead of
// waiting for certainty, and it is why the answer can be immediate at all.
type statusAllResp struct {
	Repos    []allRepoStatus `json:"repos"`
	AgeMS    int64           `json:"age_ms"`
	Scanning bool            `json:"scanning"`
}

// coldWait is how long this read may wait for the scan in flight: nothing for an
// ordinary poll that has a snapshot, the cold budget for the first read of a
// process, and the whole-scan budget for a forced refresh.
func (h *Handler) coldWait(snap *statusSnapshot, doFetch bool) time.Duration {
	switch {
	case doFetch:
		return statusScanBudget
	case snap == nil:
		return statusColdWait
	default:
		return 0
	}
}

// refreshStatusAll starts a scan for key unless one is already in flight, and
// returns the channel that closes when the scan in flight publishes.
//
// The scan runs on its own goroutine under a context DETACHED from the request:
// its lifetime is the scan budget, and a client that walks away mid-poll must not
// abort work the next poll would otherwise repeat from scratch.
func (h *Handler) refreshStatusAll(r *http.Request, key string, doFetch bool) chan struct{} {
	done, started := h.statusCache.claim(key)
	if !started {
		return done
	}
	parent := context.WithoutCancel(r.Context())
	go func() {
		ctx, cancel := context.WithTimeout(parent, statusScanBudget)
		defer cancel()
		// Publishes unconditionally, including an empty result: the claim is
		// released here and nowhere else, so a scan that returns nothing must still
		// hand the slot back or this variant never refreshes again.
		h.statusCache.publish(key, h.scanRepos(ctx, doFetch))
	}()
	return done
}

// awaitStatusAll waits for the scan in flight to publish, bounded by budget and by
// the request going away, then returns whatever the holder has. A caller that
// times out answers from the older snapshot (or an empty one) rather than holding
// the request open.
func (h *Handler) awaitStatusAll(r *http.Request, key string, running chan struct{}, budget time.Duration) *statusSnapshot {
	if running != nil {
		timer := time.NewTimer(budget)
		defer timer.Stop()
		select {
		case <-running:
		case <-r.Context().Done():
		case <-timer.C:
		}
	}
	snap, _ := h.statusCache.read(key)
	return snap
}

// scanRepos collects the status of every cloned repo under workDir (plus workDir
// itself if it is a repo), bounded per repo so one wedged repository degrades to a
// partial row.
func (h *Handler) scanRepos(ctx context.Context, doFetch bool) []allRepoStatus {
	repos := h.cachedDiscoverRepos(ctx)
	results := make([]allRepoStatus, len(repos))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(scanConcurrency)
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
	return results
}

func (h *Handler) handleRepos(w http.ResponseWriter, r *http.Request) {
	discovered := h.cachedDiscoverRepos(r.Context())
	repos := make([]string, len(discovered))
	for i, d := range discovered {
		repos[i] = d.Name
	}
	webhttp.WriteJSON(w, map[string]any{jsonKeyRepos: repos})
}

// handleShow serves a file's CONTENT at a git ref — the base side of the
// editor's diff-vs-HEAD pane, whose working-tree side comes from /api/file.
func (h *Handler) handleShow(w http.ResponseWriter, r *http.Request) {
	requested := r.URL.Query().Get("path")
	if requested == "" {
		httpreply.BadRequest(w, "path required")
		return
	}
	// Reject path traversal and flag smuggling. The client only
	// sends relative paths from `git status` output; `..` and
	// control bytes never appear in legitimate use and keep log
	// lines clean. Reject the full ASCII control range (including
	// tab, ESC) plus DEL so slog/Loki readers see readable values
	// and no invisible bytes survive into downstream tooling.
	if !validateFilePath(requested) {
		slog.Warn("git show: invalid path rejected", "repo", logsafe.Field(h.repoDir(repoFromQuery(r))), "path_len", len(requested))
		httpreply.BadRequest(w, "invalid path")
		return
	}
	ref := cmp.Or(r.URL.Query().Get("ref"), refHEAD)
	if !isValidGitRef(ref) {
		slog.Warn("git show: invalid ref rejected", "repo", logsafe.Field(h.repoDir(repoFromQuery(r))), "ref", ref)
		httpreply.BadRequest(w, "invalid ref")
		return
	}
	// An absent `repo` means "resolve it": the path is workspace-relative
	// and the caller does not know which repository owns it — the shape
	// produced by translate.relPath for a turn's changed-file ledger or a
	// tool card, which strips the workspace prefix and knows nothing
	// about repos.
	repo := repoFromQuery(r)
	file := requested
	if repo == "" {
		owner, inRepo, ok := h.ownerOf(r.Context(), requested)
		if !ok {
			// Not a failure: there is no committed revision to show. The
			// client renders an all-add diff against an empty base.
			writeGitError(w, KindNotInRepo, "")
			return
		}
		repo, file = owner, inRepo
	}
	dir := h.repoDir(repo)
	// gitShowCmd carries --no-textconv, so this resolution path inherits
	// the raw-blob pin rather than needing its own.
	out, err := gitShowCmd(r.Context(), dir, ref, file)
	if err != nil {
		if errors.Is(err, ErrPathNotInRef) {
			// File didn't exist at ref — return empty content so the
			// diff renders as all-add for new files.
			webhttp.WriteJSON(w, map[string]string{"content": ""})
			return
		}
		slog.Warn("git show failed", "repo", logsafe.Field(dir), "ref", ref, "path", logsafe.Field(file), "error", logsafe.Field(err.Error()), "out", scrubAuth(out))
		writeGitError(w, KindShowFailed, scrubAuth(out))
		return
	}
	webhttp.WriteJSON(w, map[string]string{"content": out})
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
		slog.Debug("git log failed", "repo", logsafe.Field(dir), "ref", ref, "error", logsafe.Field(err.Error()), "out", scrubAuth(out))
		webhttp.WriteJSON(w, map[string]any{"entries": []string{}, "remote": "", "behind": 0, "commit_url_prefix": ""})
		return
	}
	// Not pinned to []string{} the way `branches` below is: git log
	// answering successfully means at least one commit line, and the
	// no-commits repo takes the error path above, which writes the empty
	// array explicitly.
	var lines []string
	for line := range strings.SplitSeq(out, "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	remote, rErr := gitCmd(ctx, dir, subRemote, "get-url", remoteOrigin)
	if rErr != nil {
		slog.Debug("git remote get-url failed during log", "repo", logsafe.Field(dir), "error", logsafe.Field(rErr.Error()))
	}
	// Scrubbed once, so the commit prefix is derived from the same
	// credential-free string the client is handed.
	remote = scrubAuth(remote)
	behind := 0
	if ab, err := gitCmd(ctx, dir, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"); err == nil {
		parts := strings.Fields(ab)
		if len(parts) == 2 {
			if n, berr := strconv.Atoi(parts[1]); berr == nil {
				behind = n
			}
		}
	}
	webhttp.WriteJSON(w, map[string]any{
		"entries":           lines,
		"remote":            remote,
		"behind":            behind,
		"commit_url_prefix": commitURLPrefix(remote),
	})
}

func (h *Handler) handleBranches(w http.ResponseWriter, r *http.Request) {
	dir := h.repoDir(repoFromQuery(r))
	out, err := gitCmd(r.Context(), dir, "branch", "-a", "--format=%(refname:short)\t%(HEAD)")
	if err != nil {
		webhttp.WriteJSON(w, map[string]any{"branches": []any{}, "current": ""})
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
	webhttp.WriteJSON(w, map[string]any{"branches": branches, "current": current})
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
		httpreply.BadRequest(w, "branch required")
		return
	}
	// Reject branch names that look like options or contain characters
	// forbidden by git-check-ref-format. The `--` barrier that makes
	// most git subcommands safe can't be used here because
	// `git checkout -- <name>` changes the semantics to "restore file",
	// not "switch branch".
	if !isValidGitRef(body.Branch) {
		slog.Warn("git checkout: invalid branch rejected", "repo", body.Repo, "branch", body.Branch)
		httpreply.BadRequest(w, "invalid branch name")
		return
	}
	dir := h.repoDir(body.Repo)
	args := []string{subCheckout}
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
		httpreply.BadRequest(w, "repo name required (cannot remove workspace root)")
		return
	}
	dir := h.repoDir(body.Repo)
	// The lexical resolver answers workDir for every input it will not vouch for
	// (empty, ".", a `..` component, an absolute path), so this one comparison is
	// both the escape refusal and the "remove the workspace" refusal.
	if dir == h.workDir {
		httpreply.BadRequest(w, "cannot remove workspace root")
		return
	}
	if err := h.removeRepoDir(dir); err != nil {
		if errors.Is(err, ErrUnsafeRepoPath) {
			slog.Warn("git remove: refused", "repo", body.Repo, "error", err)
			httpreply.BadRequest(w, "that repo path is not inside the workspace")
			return
		}
		slog.Error("git remove: failed", "repo", body.Repo, "error", err)
		webhttp.WriteJSON(w, httpreply.ErrorJSON("remove failed"))
		return
	}
	slog.Info("git remove", "repo", body.Repo)
	webhttp.Ok(w)
}

// removeRepoDir unlinks dir through a parent pinned inside a confined root on the
// workspace, never by name. A lexical containment check cannot carry its answer
// forward: between the check and the remove the kernel re-resolves every
// component from the root, so an ordinary directory that passed, replaced by a
// symlink before the unlink, sends the delete wherever the link points — with no
// root on the path the target is not even bounded to the workspace, and this
// container's /config holds the chat store, the secret store, the tool tree and
// the installed agent runtime.
//
// atomicfile.OpenParentInRoot descends component by component, Lstat-ing each and
// refusing a symlink rather than following it, then confirms with os.SameFile that
// the directory it opened is the one it inspected. Naming only the final element
// through that handle removes every ancestor from the unlink's path.
//
// A repo the user symlinked into the workspace stays removable: the descent
// refuses a symlink only at an INTERMEDIATE component and hands back the parent
// for the final one, whose own RemoveAll unlinks a symlink rather than following
// it — atomicfile.RemoveFileInRoot would refuse it with ErrNotRegular instead,
// the right rule for a writer sweeping names it created, the wrong one here.
// internal/agent's _kiro/fs/delete records the same choice.
func (h *Handler) removeRepoDir(dir string) error {
	root, err := os.OpenRoot(h.workDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	rel, err := workspace.RelPath(h.workDir, dir)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnsafeRepoPath, err)
	}
	parent, base, err := atomicfile.OpenParentInRoot(root, rel)
	if err != nil {
		// os.RemoveAll answered success for a missing path, and a parent directory
		// that is gone is the same answer to the caller. Only the not-exist verdict:
		// a component refused for being a symlink or a non-directory is a real
		// failure and must surface, as a REFUSAL rather than as a disk error.
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("%w: %w", ErrUnsafeRepoPath, err)
	}
	defer func() { _ = parent.Close() }()
	return parent.RemoveAll(base)
}
