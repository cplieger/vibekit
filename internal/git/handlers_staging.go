package git

import (
	"context"
	"log/slog"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/httpreply"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	dir := h.repoDir(repoFromQuery(r))
	if !IsRepo(ctx, dir) {
		httpreply.WriteJSON(w, gitStatusResp{IsRepo: false, Files: []gitFile{}})
		return
	}
	st := collectStatus(ctx, dir, h.timeouts, &h.fetchFlight, r.URL.Query().Get("quick") == "")
	httpreply.WriteJSON(w, st)
}

// collectStatus runs the same shape of git queries handleStatus uses,
// returning a fully-populated gitStatusResp. Extracted so handleStatusAll
// can fan-out the same logic across every cloned repo. `doFetch=false`
// skips the network fetch (useful for the multi-repo dashboard where
// fetching N repos in parallel would be costly + noisy).
func collectStatus(ctx context.Context, dir string, timeouts gitTimeouts, fetchFlight *singleflight.Group, doFetch bool) gitStatusResp {
	if !IsRepo(ctx, dir) {
		return gitStatusResp{IsRepo: false, Files: []gitFile{}}
	}
	st := gitStatusResp{IsRepo: true}
	if b, err := gitCmd(ctx, dir, "branch", "--show-current"); err == nil {
		st.Branch = b
	}
	if rem, err := gitCmd(ctx, dir, subRemote, "get-url", "origin"); err == nil {
		st.Remote = scrubAuth(rem)
	}
	if doFetch {
		fetchStatus(ctx, dir, timeouts.Fetch, fetchFlight)
	}

	// Post-fetch queries are independent — run them concurrently.
	var (
		ahead, behind int
		files         []gitFile
		stashes       int
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		ahead, behind = aheadBehind(gctx, dir)
		return nil
	})
	g.Go(func() error {
		files = parseGitStatus(gctx, dir)
		return nil
	})
	g.Go(func() error {
		stashes = countStashes(gctx, dir)
		return nil
	})
	_ = g.Wait()

	st.Ahead = ahead
	st.Behind = behind
	// Never let Files marshal to JSON null: the wire contract (and the
	// client's GitRepoStatus.files) is a non-nullable array. parseGitStatus
	// returns nil for a clean repo or on error, and a nil slice marshals to
	// `null`, which makes the Changes tab's `for (const f of r.files)` throw
	// "r.files is not iterable" and blanks the whole git page.
	if files == nil {
		files = []gitFile{}
	}
	st.Files = files
	st.Stashes = stashes
	st.HasGH = hasGH()
	st.HasDirty = len(st.Files) > 0
	return st
}

// fetchStatus runs a best-effort `git fetch --quiet`, deduped per-dir via
// the shared singleflight group. A failure is logged at debug and
// otherwise ignored — a status read must not fail because the network is
// down or the remote is unreachable.
func fetchStatus(ctx context.Context, dir string, timeout time.Duration, fetchFlight *singleflight.Group) {
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, _, _ = fetchFlight.Do(dir, func() (any, error) {
		if out, err := gitCmd(fetchCtx, dir, "fetch", "--quiet"); err != nil {
			slog.Debug("git fetch during status failed", "repo", dir, "error", err, "out", scrubAuth(out))
		}
		return nil, nil
	})
}

// aheadBehind reports how many commits HEAD is ahead of and behind its
// upstream. Both are 0 when there is no upstream or the count can't be
// parsed (rev-list is gated by allowedSubcommands, so a missing upstream yields an
// error and the zero values).
func aheadBehind(ctx context.Context, dir string) (ahead, behind int) {
	ab, err := gitCmd(ctx, dir, "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	if err != nil {
		return 0, 0
	}
	parts := strings.Fields(ab)
	if len(parts) != 2 {
		return 0, 0
	}
	if n, aerr := strconv.Atoi(parts[0]); aerr == nil {
		ahead = n
	}
	if n, berr := strconv.Atoi(parts[1]); berr == nil {
		behind = n
	}
	return ahead, behind
}

// countStashes returns the number of stash entries, or 0 on error or an
// empty stash list.
func countStashes(ctx context.Context, dir string) int {
	out, err := gitCmd(ctx, dir, "stash", "list")
	if err != nil || out == "" {
		return 0
	}
	return strings.Count(out, "\n") + 1
}

// hasGH reports whether the gh CLI is available on PATH. Memoized: the
// answer is process-global and status-all was paying one PATH walk per
// repo per scan for it.
var hasGH = sync.OnceValue(func() bool {
	_, err := exec.LookPath("gh")
	return err == nil
})

func (h *Handler) handleStage(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		repoBody

		Files []string `json:"files"`
	}
	if !decodePostBody(w, r, &body, "bad request") {
		return
	}
	files, perr := sanitizeRepoPaths(body.Files)
	if perr != nil {
		httpreply.BadRequest(w, perr.Error())
		return
	}
	dir := h.repoDir(body.Repo)
	slog.Info("git stage", "repo", body.Repo, "files", len(files))
	args := append([]string{"add", "--"}, files...)
	if out, err := gitCmd(r.Context(), dir, args...); err != nil {
		httpreply.WriteJSON(w, httpreply.ErrorJSON(scrubAuth(out)))
		return
	}
	httpreply.Ok(w)
}

func (h *Handler) handleUnstage(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		repoBody

		Files []string `json:"files"`
	}
	if !decodePostBody(w, r, &body, "bad request") {
		return
	}
	files, perr := sanitizeRepoPaths(body.Files)
	if perr != nil {
		httpreply.BadRequest(w, perr.Error())
		return
	}
	dir := h.repoDir(body.Repo)
	slog.Info("git unstage", "repo", body.Repo, "files", len(files))
	args := append([]string{subReset, refHEAD, "--"}, files...)
	if out, err := gitCmd(r.Context(), dir, args...); err != nil {
		httpreply.WriteJSON(w, httpreply.ErrorJSON(scrubAuth(out)))
		return
	}
	httpreply.Ok(w)
}

func (h *Handler) handleDiscard(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body struct {
		repoBody

		Files []string `json:"files"`
	}
	if !decodePostBody(w, r, &body, "files required") {
		return
	}
	if len(body.Files) == 0 {
		httpreply.BadRequest(w, "files required")
		return
	}
	files, perr := sanitizeRepoPaths(body.Files)
	if perr != nil {
		httpreply.BadRequest(w, perr.Error())
		return
	}
	ctx := r.Context()
	dir := h.repoDir(body.Repo)
	// Unstage the requested paths first: `checkout --` restores the
	// worktree FROM THE INDEX, so a staged modification silently survived
	// "Discard all", and a staged NEW file (no index-vs-worktree diff)
	// made checkout error with "pathspec did not match". Best-effort —
	// paths with nothing staged are a no-op for reset.
	if out, err := gitCmd(ctx, dir, append([]string{subReset, "-q", refHEAD, "--"}, files...)...); err != nil {
		slog.Debug("git discard: reset before discard failed (continuing)",
			"repo", body.Repo, "error", err, "out", scrubAuth(out))
	}
	tracked, untracked := splitTrackedUntracked(ctx, dir, files)
	slog.Info("git discard", "repo", body.Repo, "tracked_count", len(tracked), "untracked_count", len(untracked))
	var errs []string
	if len(tracked) > 0 {
		args := append([]string{subCheckout, "--"}, tracked...)
		if out, err := gitCmd(ctx, dir, args...); err != nil {
			slog.Warn("git discard checkout failed", "repo", body.Repo, "count", len(tracked), "error", err, "out", scrubAuth(out))
			errs = append(errs, "checkout: "+out)
		}
	}
	if len(untracked) > 0 {
		args := append([]string{"clean", "-fd", "--"}, untracked...)
		if out, err := gitCmd(ctx, dir, args...); err != nil {
			slog.Warn("git discard clean failed", "repo", body.Repo, "count", len(untracked), "error", err, "out", scrubAuth(out))
			errs = append(errs, "clean: "+out)
		}
	}
	if len(errs) > 0 {
		httpreply.WriteJSON(w, httpreply.ErrorJSON(scrubAuth(strings.Join(errs, "\n"))))
		return
	}
	httpreply.Ok(w)
}
