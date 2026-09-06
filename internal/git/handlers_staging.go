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

// collectStatus answers one repository's status. doFetch=false skips the network
// fetch, for the dashboard where fetching N repos in parallel is costly.
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
	// A failed status leaves the zero counts, so one wedged repository cannot blank
	// the dashboard; readStatus still reports the branch, read off .git/HEAD.
	ps, _ := readStatus(ctx, dir)
	st.Branch = ps.Branch
	st.Ahead = ps.Ahead
	st.Behind = ps.Behind
	st.Stashes = ps.Stashes
	// The wire contract is a non-nullable array: a nil slice marshals to `null`, and
	// the Changes tab's `for (const f of r.files)` then throws and blanks the page.
	st.Files = ps.Files
	if st.Files == nil {
		st.Files = []gitFile{}
	}
	st.HasDirty = len(st.Files) > 0
	return st
}

// fetchStatus runs a best-effort `git fetch --quiet`, deduped per-dir. A failure is
// logged at debug and ignored: a status read must not fail on an unreachable remote.
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
	// Unstage first: `checkout --` restores the worktree FROM THE INDEX, so a staged
	// modification survives the discard and a staged NEW file makes checkout error.
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
