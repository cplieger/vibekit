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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/pathinside/v2"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/logsafe"
	"github.com/cplieger/vibekit/internal/workspace"
	"github.com/cplieger/webhttp/v2"
	"golang.org/x/sync/errgroup"
)

// allRepoStatus is gitStatusResp plus the repo name, so the dashboard can group.
type allRepoStatus struct {
	Repo string `json:"repo"`
	gitStatusResp
}

// Scan budgets. perRepoBudget keeps one wedged repo to a partial row rather than a
// stalled dashboard. statusColdWait is the WHOLE scan budget, because a cold wait
// expiring first answers `{repos: []}` — a claim about the tree, not about the read.
const (
	statusScanBudget = 30 * time.Second
	perRepoBudget    = 10 * time.Second
	statusMaxAge     = 10 * time.Second
	statusColdWait   = statusScanBudget
	// scanConcurrency bounds fork+exec pressure, not CPU: each repo is two
	// short-lived git subprocesses.
	scanConcurrency = 8
	// statusPathsMax caps one scoped read's paths; the excess is dropped, since a
	// scoped refresh exists to scan FEWER repos than a full one.
	statusPathsMax = 64
)

// handleStatusAll answers the multi-repo dashboard from the newest completed scan plus
// its age, and refreshes behind the answer (status_cache.go says why). Only two callers
// wait: the FIRST read of a process, and `?fetch=1`. `?paths=` narrows the refresh to
// the repositories owning those paths, resolved server-side.
func (h *Handler) handleStatusAll(w http.ResponseWriter, r *http.Request) {
	doFetch := r.URL.Query().Get("fetch") == "1"
	key := statusKeyPoll
	if doFetch {
		key = statusKeyFetch
	}
	snap, running := h.statusCache.read(key)
	scoped, only := h.statusScope(r, snap)
	switch {
	case scoped && len(only) == 0:
		// Paths named but unowned: rescanning everything would be the opposite of the
		// request, so answer from the snapshot unchanged.
	case scoped:
		running = h.refreshStatus(r, key, doFetch, only)
	case doFetch || snap.stale(statusMaxAge):
		running = h.refreshStatus(r, key, doFetch, nil)
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

// statusScope resolves `?paths=` into the repository names owning those paths; scoped
// reports whether this read is a scoped one at all. A COLD read carrying paths is not
// scoped: publishing a two-repo result into no snapshot leaves a partial scan later
// reads cannot tell from a whole one. An unowned path is dropped.
func (h *Handler) statusScope(r *http.Request, snap *statusSnapshot) (scoped bool, only map[string]struct{}) {
	raw := r.URL.Query().Get("paths")
	if raw == "" || snap == nil {
		return false, nil
	}
	only = make(map[string]struct{}, 4)
	seen := 0
	for p := range strings.SplitSeq(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		seen++
		if seen > statusPathsMax {
			break
		}
		// Checked before ownerOf, which would otherwise resolve `../elsewhere` to the
		// workspace-root repo — it owns every path no subdirectory repo claims.
		if pathinside.RelEscapes(p) {
			continue
		}
		if repo, _, ok := h.ownerOf(r.Context(), p); ok {
			only[repo] = struct{}{}
		}
	}
	return true, only
}

// statusAllResp is the dashboard's answer: the newest completed scan, how old it is,
// and whether one is running behind it. The age is what lets the answer be immediate.
type statusAllResp struct {
	Repos    []allRepoStatus `json:"repos"`
	AgeMS    int64           `json:"age_ms"`
	Scanning bool            `json:"scanning"`
}

// coldWait is how long this read may wait for the scan in flight: nothing for an
// ordinary poll with a snapshot, the cold budget for a process's first read, and the
// whole-scan budget for a forced refresh.
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

// refreshStatus starts a scan for key unless one is in flight, returning the channel
// that closes when the refresh in flight publishes. `only` names the repositories to
// scan, nil the whole tree; a scoped result is MERGED. The scan runs DETACHED from the
// request, so a client walking away mid-poll does not abort work the next poll
// repeats, and it LOOPS to drain the intent a joining read leaves in the refresh slot.
func (h *Handler) refreshStatus(r *http.Request, key string, doFetch bool, only map[string]struct{}) chan struct{} {
	done, started := h.statusCache.claim(key, only)
	if !started {
		return done
	}
	parent := context.WithoutCancel(r.Context())
	go func() {
		for scope, run := only, true; run; {
			ctx, cancel := context.WithTimeout(parent, statusScanBudget)
			rows := h.scanRepos(ctx, doFetch, scope)
			cancel()
			scope, run = h.statusCache.finish(key, rows)
		}
	}()
	return done
}

// awaitStatusAll waits for the scan in flight to publish, bounded by budget and by
// the request going away, then returns whatever the holder has: a timeout answers
// from the older snapshot rather than holding the request open.
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

// scanRepos collects the status of every cloned repo under workDir, bounded per repo. A
// non-nil `only` narrows it, and the result is then a PARTIAL scan the caller must
// merge rather than publish.
func (h *Handler) scanRepos(ctx context.Context, doFetch bool, only map[string]struct{}) []allRepoStatus {
	repos := h.cachedDiscoverRepos(ctx)
	if only != nil {
		repos = slices.DeleteFunc(slices.Clone(repos), func(e repoEntry) bool {
			_, want := only[e.Name]
			return !want
		})
	}
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
	// Rejects traversal, flag smuggling, and the whole ASCII control range plus
	// DEL, so no invisible byte reaches a log sink or downstream tooling.
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
	// An absent `repo` means resolve it: the caller knows nothing of repos, so its path
	// is workspace-relative.
	repo := repoFromQuery(r)
	file := requested
	if repo == "" {
		owner, inRepo, ok := h.ownerOf(r.Context(), requested)
		if !ok {
			// Not a failure: no committed revision to show, so the client renders an
			// all-add diff against an empty base.
			writeGitError(w, KindNotInRepo, "")
			return
		}
		repo, file = owner, inRepo
	}
	dir := h.repoDir(repo)
	// gitShowCmd carries --no-textconv, so the raw-blob pin is inherited here.
	out, err := gitShowCmd(r.Context(), dir, ref, file)
	if err != nil {
		if errors.Is(err, ErrPathNotInRef) {
			// Absent at ref: empty content renders as an all-add diff.
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
	// Not pinned to []string{} the way `branches` is: a successful git log means at
	// least one line, and a no-commits repo takes the error path above.
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
	// Scrubbed once, so the commit prefix comes off the same credential-free string.
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
	// The `--` barrier is unavailable here: `git checkout -- <name>` means restore file,
	// not switch branch, so the name itself must be validated.
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
	// The lexical resolver answers workDir for every input it will not vouch for, so
	// this one comparison is both the escape refusal and the remove-the-workspace one.
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
// workspace, never by name: the kernel re-resolves every component at the unlink, so a
// lexically-checked directory later replaced by a symlink would send the delete
// wherever the link points. A repo the user symlinked in stays removable — the descent
// refuses a symlink only at an INTERMEDIATE component.
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
		// A gone parent is the same answer as a gone path. Only not-exist: a component
		// refused for being a symlink or non-directory is a real failure.
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("%w: %w", ErrUnsafeRepoPath, err)
	}
	defer func() { _ = parent.Close() }()
	return parent.RemoveAll(base)
}
