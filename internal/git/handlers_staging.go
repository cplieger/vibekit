package git

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/logsafe"
	"github.com/cplieger/webhttp/v2"
	"golang.org/x/sync/singleflight"
)

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	dir := h.repoDir(repoFromQuery(r))
	if !IsRepo(ctx, dir) {
		webhttp.WriteJSON(w, gitStatusResp{IsRepo: false, Files: []gitFile{}})
		return
	}
	st := collectStatus(ctx, dir, h.timeouts, &h.fetchFlight, r.URL.Query().Get("quick") == "")
	webhttp.WriteJSON(w, st)
}

// collectStatus answers one repository's status in TWO git invocations: the status
// call that reports the branch, ahead/behind, the stash count and the file list
// together (porcelain.go), plus `remote get-url origin` for the URL v2 omits.
// `doFetch=false` skips the network fetch, for the dashboard where fetching N
// repos in parallel would be costly and noisy.
//
// Order is load-bearing: the fetch runs BEFORE the status call, so ahead/behind is
// measured against the refreshed remote ref.
func collectStatus(ctx context.Context, dir string, timeouts gitTimeouts, fetchFlight *singleflight.Group, doFetch bool) gitStatusResp {
	if !IsRepo(ctx, dir) {
		return gitStatusResp{IsRepo: false, Files: []gitFile{}}
	}
	st := gitStatusResp{IsRepo: true}
	if rem, err := gitCmd(ctx, dir, subRemote, "get-url", remoteOrigin); err == nil {
		st.Remote = scrubAuth(rem)
	}
	if doFetch {
		fetchStatus(ctx, dir, timeouts.Fetch, fetchFlight)
	}
	// A failed status leaves the zero values: the row still says the repo exists,
	// which is what keeps one wedged repository from blanking the dashboard.
	ps, _ := readStatus(ctx, dir)
	st.Branch = ps.Branch
	st.Ahead = ps.Ahead
	st.Behind = ps.Behind
	st.Stashes = ps.Stashes
	// Never let Files marshal to JSON null: the wire contract (and the client's
	// GitRepoStatus.files) is a non-nullable array. The parser returns nil for a
	// clean repo or a failed call, and a nil slice marshals to `null`, which makes
	// the Changes tab's `for (const f of r.files)` throw "r.files is not iterable"
	// and blanks the whole git page.
	st.Files = ps.Files
	if st.Files == nil {
		st.Files = []gitFile{}
	}
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
			slog.Debug("git fetch during status failed", "repo", logsafe.Field(dir), "error", logsafe.Field(err.Error()), "out", scrubAuth(out))
		}
		return nil, nil
	})
}

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
	args := append([]string{subAdd, "--"}, files...)
	if out, err := gitCmd(r.Context(), dir, args...); err != nil {
		webhttp.WriteJSON(w, httpreply.ErrorJSON(scrubAuth(out)))
		return
	}
	webhttp.Ok(w)
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
		webhttp.WriteJSON(w, httpreply.ErrorJSON(scrubAuth(out)))
		return
	}
	webhttp.Ok(w)
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
			errs = append(errs, subCheckout+": "+cmdFailure(out, err))
		}
	}
	if len(untracked) > 0 {
		args := append([]string{subClean, "-fd", "--"}, untracked...)
		if out, err := gitCmd(ctx, dir, args...); err != nil {
			slog.Warn("git discard clean failed", "repo", body.Repo, "count", len(untracked), "error", err, "out", scrubAuth(out))
			errs = append(errs, subClean+": "+cmdFailure(out, err))
		}
	}
	if len(errs) > 0 {
		webhttp.WriteJSON(w, httpreply.ErrorJSON(scrubAuth(strings.Join(errs, "\n"))))
		return
	}
	webhttp.Ok(w)
}
