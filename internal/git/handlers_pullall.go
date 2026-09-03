package git

// The Pull-all pass: one sweep over every discovered repository that fetches,
// judges whether a fast-forward is safe, pulls the ones that are, and reports a
// verdict for every one it looked at.
//
// Shaped like the status fan-out, but it keeps a singleflight rather than a
// snapshot holder: a pull is a gesture whose result the caller waits for. What it
// adds is the PRE-FLIGHT, which is why it is not a client-side fan-out over
// handlePull — "is a fast-forward safe here" has to be atomic with the pull it
// guards, or the tree changes between the answer and the action.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/logsafe"
	"github.com/cplieger/webhttp/v2"
	"golang.org/x/sync/errgroup"
)

// pullVerdict names what the pass did to one repository. The four values are
// mutually exclusive and cover every repository, so a reader gets exactly one
// answer per repo and the client needs no residual bucket:
//
//	pulled  — the pull ran and git accepted it.
//	blocked — the pre-flight refused; Reason names which hazard, Detail says which files or commits.
//	failed  — the pull ran and git refused it; Detail carries git's own words.
//	skipped — there was nothing to do; Reason says why.
//
// blocked and failed are the two a reader has to act on, and the two the panel
// flags on a repo's own block.
type pullVerdict string

const (
	verdictPulled  pullVerdict = "pulled"
	verdictBlocked pullVerdict = "blocked"
	verdictFailed  pullVerdict = "failed"
	verdictSkipped pullVerdict = "skipped"
)

// Reasons a repository was left alone. Each set is exhaustive over its verdict
// and the blocked set is ordered by severity: the first hazard that holds is the
// one reported, so no repo carries two.
const (
	// blocked
	reasonInProgress   = "in_progress"   // a merge, rebase, cherry-pick or revert is underway
	reasonConflict     = "conflict"      // the index holds unmerged entries
	reasonUnreadable   = "unreadable"    // the working tree could not be read, so nothing may be assumed about it
	reasonDiverged     = "diverged"      // local commits are not on the upstream, so no fast-forward exists
	reasonLocalChanges = "local_changes" // a locally-changed path is one the incoming commits rewrite

	// skipped
	reasonNotARepo     = "not_a_repo"
	reasonDetachedHead = "detached_head"
	reasonNoUpstream   = "no_upstream"
	reasonUpToDate     = "up_to_date"
	reasonOutOfTime    = "out_of_time"
)

// pullResult is one repository's row in the response.
type pullResult struct {
	Repo    string      `json:"repo"`
	Verdict pullVerdict `json:"verdict"`
	Reason  string      `json:"reason,omitempty"`
	Detail  string      `json:"detail,omitempty"`
}

// pullAllBudget bounds the whole pass, perRepoPullBudget bounds each repository
// inside it, and minPullBudget is the floor a pull needs before it may START.
//
// That floor is the point of the three: a `git pull --ff-only` killed part-way
// through its checkout leaves a half-updated worktree and possibly an
// index.lock, which is precisely the state this pass exists to avoid producing.
// So a repository the remaining budget cannot see through is reported
// out_of_time and never touched, rather than pulled and then interrupted.
//
// pullAllBudget sits under @cplieger/fetch's 30s request timeout on purpose, so
// the client always receives a complete answer rather than timing out over a
// pass that is still running. The fetch half of this pass is the same work
// /api/git/status-all?fetch=1 does on every Refresh, which the client already
// caps at 15s, and the pulls it adds are local checkouts against objects the
// fetch has just brought down.
const (
	pullAllBudget     = 25 * time.Second
	perRepoPullBudget = 20 * time.Second
	minPullBudget     = 8 * time.Second
)

// handlePullAll fetches every repository, fast-forwards the ones where that is
// safe, and reports why it left the others alone.
//
// Singleflighted, so two presses join one pass. DETACHED from the request
// context (context.WithoutCancel), because a client that navigates away
// mid-pass must not SIGKILL a git pull in the middle of its checkout — the same
// reasoning handleStatusAll gives for its own scan, with a mutation behind it
// instead of a read.
func (h *Handler) handlePullAll(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	v, _, _ := h.pullFlight.Do("pull-all", func() (any, error) {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), pullAllBudget)
		defer cancel()
		deadline, _ := sctx.Deadline()
		repos := h.cachedDiscoverRepos(sctx)
		results := make([]pullResult, len(repos))
		g, gctx := errgroup.WithContext(sctx)
		g.SetLimit(8)
		for i, e := range repos {
			g.Go(func() error {
				rctx, rcancel := context.WithTimeout(gctx, perRepoPullBudget)
				defer rcancel()
				results[i] = h.pullOne(rctx, e, deadline)
				return nil
			})
		}
		_ = g.Wait()
		return results, nil
	})
	results, _ := v.([]pullResult)
	// Treated as read-only by every singleflight sharer.
	webhttp.WriteJSON(w, map[string]any{jsonKeyRepos: results})
}

// pullOne answers for one repository. Every exit carries a verdict: a row with
// no verdict would reach the client as an unclassifiable repo, so the zero value
// is out_of_time rather than empty.
func (h *Handler) pullOne(ctx context.Context, e repoEntry, deadline time.Time) pullResult {
	res := pullResult{Repo: e.Name, Verdict: verdictSkipped, Reason: reasonOutOfTime}
	if ctx.Err() != nil {
		return res
	}
	if !IsRepo(ctx, e.Dir) {
		res.Reason = reasonNotARepo
		return res
	}
	// Fetch first, or `behind` is whatever the last poll happened to see and a
	// repository that fell behind since would be reported up to date. Shares the
	// per-directory singleflight with the status fan-out, so pressing Pull all
	// straight after Refresh costs one fetch rather than two.
	fetchStatus(ctx, e.Dir, h.timeouts.Fetch, &h.fetchFlight)

	branch, err := gitCmd(ctx, e.Dir, "branch", "--show-current")
	if err != nil || branch == "" {
		res.Reason = reasonDetachedHead
		return res
	}
	ahead, behind, ok := upstreamDivergence(ctx, e.Dir)
	if !ok {
		res.Reason = reasonNoUpstream
		return res
	}
	if behind == 0 {
		res.Reason = reasonUpToDate
		return res
	}
	// Past here the repository HAS something to pull, so every remaining answer
	// is either a pull or a hazard the reader must see. An expired context is
	// re-checked because the reads below report their failure as a REASON, and a
	// cancelled read would otherwise be reported as a property of the repo.
	if ctx.Err() != nil {
		res.Reason = reasonOutOfTime
		return res
	}
	if blocker := preflight(ctx, e.Dir, branch, ahead); blocker != nil {
		blocker.Repo = e.Name
		return *blocker
	}
	budget := min(time.Until(deadline), h.timeouts.Push)
	if budget < minPullBudget {
		res.Reason = reasonOutOfTime
		return res
	}
	slog.Info("git pull-all: pulling", "repo", logsafe.Field(e.Name), "behind", behind)
	out, perr := gitCmdWithCreds(ctx, budget, e.Dir, "", "pull", "--ff-only")
	if perr != nil {
		return pullResult{Repo: e.Name, Verdict: verdictFailed, Detail: scrubAuth(cmdFailure(out, perr))}
	}
	return pullResult{Repo: e.Name, Verdict: verdictPulled}
}

// preflight judges whether a fast-forward is safe in a repository already known
// to be behind, returning the blocking verdict or nil.
//
// Ordered by severity, first hit wins, so the reported hazard is the worst one
// present. `git pull --ff-only` would refuse in every one of these states — the
// pass runs the checks anyway because git's own message names the symptom rather
// than the cause ("You have unstaged changes" for a rebase in progress), and
// because a reader scanning a list of repositories needs the cause.
func preflight(ctx context.Context, dir, branch string, ahead int) *pullResult {
	if operationInProgress(ctx, dir) {
		return blocked(reasonInProgress, "A merge, rebase or cherry-pick is in progress here.")
	}
	dirty, conflicted, ok := worktreeState(ctx, dir)
	if !ok {
		return blocked(reasonUnreadable, "The working tree could not be read.")
	}
	if conflicted {
		return blocked(reasonConflict, "The index holds unresolved conflicts.")
	}
	if ahead > 0 {
		commits := plural(ahead, "local commit", "local commits")
		return blocked(reasonDiverged,
			fmt.Sprintf("%s has %s the upstream does not, so there is no fast-forward.", branch, commits))
	}
	// The one hazard whose answer needs both sides: which paths carry a local
	// change, and which paths the incoming commits rewrite.
	incoming, iok := incomingFiles(ctx, dir)
	if !iok {
		return blocked(reasonUnreadable, "The incoming changes could not be read.")
	}
	if clash := overlap(dirty, incoming); len(clash) > 0 {
		return blocked(reasonLocalChanges,
			fmt.Sprintf("Local changes to %s would be overwritten.", nameSome(clash)))
	}
	return nil
}

// blocked builds a blocking verdict. Repo is filled in by the caller, which is
// the one field preflight has no business knowing.
func blocked(reason, detail string) *pullResult {
	return &pullResult{Verdict: verdictBlocked, Reason: reason, Detail: detail}
}

// upstreamDivergence reports how far HEAD is from its upstream. ok is false when
// the branch tracks nothing, which is why the pass asks rev-list rather than
// reading the counts off the shared status call: porcelain v2 omits the
// ahead/behind header entirely for an untracked branch, so a status read answers
// (0, 0) for both that and being in sync, and the two want different verdicts
// here.
func upstreamDivergence(ctx context.Context, dir string) (ahead, behind int, ok bool) {
	out, err := gitCmd(ctx, dir, "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	if err != nil {
		return 0, 0, false
	}
	parts := strings.Fields(out)
	if len(parts) != 2 {
		return 0, 0, false
	}
	a, aerr := strconv.Atoi(parts[0])
	b, berr := strconv.Atoi(parts[1])
	if aerr != nil || berr != nil {
		return 0, 0, false
	}
	return a, b, true
}

// inProgressMarkers are git's own control entries for an operation that has
// stopped part-way. A pull during one of these is unambiguously wrong, and it is
// also the state where git's refusal is least informative.
var inProgressMarkers = []string{
	"MERGE_HEAD",
	"CHERRY_PICK_HEAD",
	"REVERT_HEAD",
	"rebase-merge",
	"rebase-apply",
}

// operationInProgress reports whether a merge, rebase, cherry-pick or revert has
// stopped part-way in dir.
//
// The markers are read out of --absolute-git-dir rather than out of dir/.git,
// because a worktree's and a submodule's .git is a FILE pointing elsewhere and
// the control entries live at the real directory. A path git does not answer
// absolutely is refused rather than joined: nothing is blocked on it, and the
// pull that follows reports git's own message.
func operationInProgress(ctx context.Context, dir string) bool {
	gitDir, err := gitCmd(ctx, dir, "rev-parse", "--absolute-git-dir")
	if err != nil || !filepath.IsAbs(gitDir) {
		return false
	}
	for _, name := range inProgressMarkers {
		if _, serr := os.Stat(filepath.Join(gitDir, name)); serr == nil { // #nosec G703 -- gitDir is git's own answer for a dir resolved through repoDir; the name is a package constant and no content is read
			return true
		}
	}
	return false
}

// worktreeState answers the pre-flight's two questions off ONE status read: does the
// index hold a merge conflict, and which paths carry a local change.
//
// The conflict half cannot come off the file rows — the row builder splits an XY pair
// into one row per side, and that pairing is what a conflict IS — so it reads v2's own
// unmerged record type. Every changed path counts as dirty, staged and untracked
// alike, and a rename's ORIGIN too: a fast-forward would have to write either end.
func worktreeState(ctx context.Context, dir string) (dirty map[string]struct{}, conflicted, ok bool) {
	st, err := readStatus(ctx, dir)
	if err != nil {
		// Fail closed: a tree whose status could not be read is not a clean one.
		return nil, false, false
	}
	dirty = make(map[string]struct{}, len(st.Files))
	for _, f := range st.Files {
		dirty[f.Path] = struct{}{}
		if f.OrigPath != "" {
			dirty[f.OrigPath] = struct{}{}
		}
	}
	return dirty, st.Conflicted, true
}

// incomingFiles lists the paths a fast-forward to the upstream would write. Only
// meaningful when HEAD is strictly behind, which is the only state preflight
// asks in.
//
// --no-textconv pins the raw comparison: --name-only prints no content, but the
// flag is what stops a repo-supplied textconv PROGRAM being run, not merely its
// output being shown.
func incomingFiles(ctx context.Context, dir string) (map[string]struct{}, bool) {
	out, err := gitCmd(ctx, dir, "diff", "--no-textconv", "--name-only", "-z", "HEAD..@{upstream}")
	if err != nil {
		return nil, false
	}
	files := make(map[string]struct{})
	for p := range strings.SplitSeq(out, "\x00") {
		if p != "" {
			files[p] = struct{}{}
		}
	}
	return files, true
}

// overlap returns the paths present in both sets, sorted so the reported names
// do not reshuffle between passes over an unchanged tree.
func overlap(dirty, incoming map[string]struct{}) []string {
	var both []string
	for p := range dirty {
		if _, hit := incoming[p]; hit {
			both = append(both, p)
		}
	}
	slices.Sort(both)
	return both
}

// maxNamedPaths bounds how many paths a blocked detail names before it counts
// the rest. Three is enough to recognise what is in the way; a hundred is a
// banner nobody reads.
const maxNamedPaths = 3

// nameSome renders a path list for a user-facing sentence, naming at most
// maxNamedPaths of them.
func nameSome(paths []string) string {
	if len(paths) <= maxNamedPaths {
		return strings.Join(paths, ", ")
	}
	rest := len(paths) - maxNamedPaths
	return fmt.Sprintf("%s and %d more", strings.Join(paths[:maxNamedPaths], ", "), rest)
}

// plural renders a count with the noun that agrees with it.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
