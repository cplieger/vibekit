// Push when a pull request you opened flips green or red.
//
// # Why the poll is server-side
//
// CI finishes three to six minutes after the push that triggered it, which is
// long after the turn ended and usually with nothing open. A client poll cannot
// fire when the tab is closed, so the only place this can live is the server.
//
// # What it costs when nothing is pending, stated honestly
//
// It is NOT zero. Discovery IS the source query — there is no cheaper question to
// ask, because no forge CLI has a cross-repo PR listing — so learning whether an
// open authored PR exists costs one `Whoami` per connected forge plus one
// `ListPRs` per watched clone, whatever the answer turns out to be.
//
// So there are TWO RATES rather than one, and the slow one is what the idle box
// pays:
//
//  1. HasSubscribers() is the FIRST thing every sweep does, before any forge work.
//     Each forge call is a `gh`/`glab`/`tea` subprocess with a 60s ListTimeout, so
//     a sweep that consulted the source first and the subscriber set second would
//     spend those subprocesses on a box nobody has subscribed from. This gate is
//     genuinely free.
//  2. With nothing being watched, the next sweep is PRDiscoveryInterval away. The
//     60-second rate engages only while at least one open authored PR is being
//     tracked, and falls back to discovery the moment the last one closes.
//
// The per-PR CI verdict needs no second call: PR.CheckStatus already carries the
// folded state of the head commit, populated by ListPRs. So a sweep is one
// subprocess per watched repo plus one per forge, never one per PR.
//
// # Which forges can actually answer, and which edges
//
// The verdict comes from the list call and nothing else — no per-PR status fetch,
// no compensating layer — so the feature is exactly as complete as each provider's
// list response, and that is uneven:
//
//   - GitHub folds `statusCheckRollup` into `pr list`, so both edges arrive: a run
//     turning green and a run turning red.
//   - GitLab's `detailed_merge_status` distinguishes only `ci_still_running` and
//     `ci_must_pass` (mapGLabCheckStatus). It has no passing value, because a
//     project that does not require pipelines is `mergeable` with a red pipeline
//     and reading that as green would paint over a failure. So a GitLab MR
//     notifies on a FAILURE and can never announce a recovery.
//   - Gitea and Codeberg carry no CI state on the PR object at all (giteaProvider
//     .parsePRs leaves CheckStatus empty), so they notify on nothing.
//
// That limit is stated in the setting's own copy rather than left for a user to
// discover as silence, and TestProviderCheckVerdicts_MatchTheStatedScope pins each
// claim against the provider code so the copy cannot go stale quietly.
//
// # Why a first sighting never pushes
//
// A PR's first observation SEEDS its state silently; only a change from a seeded
// state is news. Without that, every boot would push about every open PR, and a
// PR that went green while the container was down would arrive as a fresh alert
// hours late. Same judgement as schedule.MissGrace: waking to a batch of stale
// notifications is worse than missing one.

package forges

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/push"
)

// PRPollInterval is the ACTIVE rate: how often the poller looks while it is
// tracking at least one open authored PR. Sixty seconds against a three-to-six
// minute CI run: fast enough that the notice is still useful, slow enough that a
// run is not sampled a dozen times. Matches schedule.TickInterval, and for the
// same reason — one shared timer rather than a timer per subject.
const PRPollInterval = time.Minute

// PRDiscoveryInterval is the IDLE rate: how often the poller asks whether an open
// authored PR has appeared, when it is tracking none.
//
// Five minutes, and the number is a trade between two costs that pull opposite
// ways. Against the old always-60s loop it cuts the idle sweep count twelve-fold
// (12 an hour rather than 60), which is what the "costs almost nothing pending"
// claim now means: twelve `Whoami`-plus-`ListPRs` passes an hour on a box with
// clones and a subscription and no open PR.
//
// The cost of going slower than this is the seeding rule two sections down. A PR is
// SEEDED silently on first sighting, so a run whose whole lifetime fits inside one
// discovery gap is observed already settled and never announced. CI here takes
// three to six minutes, so five minutes keeps the common case (push, then discover
// while the run is still pending, then announce the settle) on the right side of
// the gap while still paying a twelfth of the old idle cost. Do not raise it
// without re-reading that interaction: at fifteen minutes most single-run PRs would
// be seeded green and the feature would go quiet.
const PRDiscoveryInterval = 5 * time.Minute

// WatchedPR is one open pull request the poller tracks, reduced to the fields the
// notice needs. Deliberately not the full PR: the poller keeps one of these per
// subject between ticks, and holding a whole PR would keep its body too.
type WatchedPR struct {
	ForgeID string
	Repo    string
	Title   string
	// Check is the folded CI verdict, in PR.CheckStatus's vocabulary: "" (the
	// forge reported no checks), "pending", "passing", "failing".
	Check  string
	Number int
}

// PRSource answers the poller's one question: which open pull requests did the
// connected identity author, and what is CI saying about each.
//
// One method rather than three (identity, repos, PRs) because the poller has one
// question and the mechanics of answering it — which forges are connected, which
// repos are checked out, whose login to filter on — are the implementation's, not
// the loop's. It is also the seam that lets the loop be tested with no subprocess,
// the same shape schedule.Launcher has.
type PRSource interface {
	OpenAuthoredPRs(ctx context.Context) ([]WatchedPR, error)
}

// PRNotifier is the slice of the push service the poller uses: 2 of the 8
// methods *push.Service offers. The signatures match the concrete methods
// exactly, so it satisfies this directly and there is no adapter to keep in
// step. Identical in shape to internal/hub's pushNotifier, and deliberately a
// separate declaration — two consumers restating two methods each is cheaper
// than one contract every package imports.
type PRNotifier interface {
	HasSubscribers() bool
	Send(ctx context.Context, title, body string, kind api.PushKind, subject api.PushSubject)
}

// PRStatusPoller notifies on a CI flip. Construct with NewPRStatusPoller and call
// Run in a goroutine; it returns when ctx is cancelled.
type PRStatusPoller struct {
	src  PRSource
	push PRNotifier
	// seen is the last CI verdict per subject key. Bounded by the number of open
	// PRs the identity has, and pruned every sweep: a merged PR's entry is dropped
	// so a long-lived process does not accumulate one slot per PR it ever saw.
	//
	// It is also the RATE selector: empty means nothing is being tracked, which is
	// the state that earns the discovery interval rather than the active one.
	seen      map[string]string
	tick      time.Duration
	discovery time.Duration
}

// NewPRStatusPoller wires a poller over a source and the push service.
func NewPRStatusPoller(src PRSource, notifier PRNotifier) *PRStatusPoller {
	return &PRStatusPoller{
		src:       src,
		push:      notifier,
		seen:      make(map[string]string),
		tick:      PRPollInterval,
		discovery: PRDiscoveryInterval,
	}
}

// Run polls until ctx is cancelled. It does NOT sweep on entry — the first sweep
// only seeds state anyway, so sweeping at boot would spend a subprocess per repo
// during startup to learn something the next sweep learns for free.
//
// A TIMER RESET FROM COMPLETION, never a ticker. A ticker retains one pending tick
// while the receiver is busy, so a sweep that outran the interval — several slow
// repos, each `ListPRs` bounded at 60s — would return and immediately consume that
// retained tick, running forge subprocesses back to back with no quiet period.
// Scheduling the next sweep after the previous one RETURNS makes the interval a
// floor on the gap between sweeps rather than on their start times.
//
// Shutdown is context cancellation, and the composition root now owns a context it
// actually cancels (App.Shutdown), so the loop stops with the process's services
// rather than outliving a closed push service. A Send already in flight is bounded
// by the push service's own retry budget and its merged service context.
func (p *PRStatusPoller) Run(ctx context.Context) {
	t := time.NewTimer(p.nextDelay())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.sweep(ctx)
			t.Reset(p.nextDelay())
		}
	}
}

// nextDelay picks the rate for the next sweep from what the last one found.
//
// Tracking something means the active rate; tracking nothing means discovery. Read
// and written only from Run's goroutine (sweep is the sole writer of seen), so no
// lock is involved.
func (p *PRStatusPoller) nextDelay() time.Duration {
	if len(p.seen) > 0 {
		return p.tick
	}
	return p.discovery
}

// sweep is one tick.
func (p *PRStatusPoller) sweep(ctx context.Context) {
	// GATE 1, first, before any forge work. Clearing the state on the way out is
	// deliberate: a device subscribing later must not be told about every flip that
	// happened while nobody could receive one.
	if !p.push.HasSubscribers() {
		clear(p.seen)
		return
	}
	prs, err := p.src.OpenAuthoredPRs(ctx)
	if err != nil {
		// A forge that is down, rate-limited or logged out. Say so once per tick
		// and keep the loop alive; the next tick may find it back.
		slog.Warn("pr status: listing open pull requests failed", "error", err)
		return
	}
	// GATE 2: nothing pending, so no per-PR work. The state goes with it, because
	// the PRs it described are no longer open — which also drops the loop back to
	// the discovery rate, since seen is what nextDelay reads.
	if len(prs) == 0 {
		clear(p.seen)
		return
	}
	live := make(map[string]struct{}, len(prs))
	for _, pr := range prs {
		key := prSubjectKey(pr)
		live[key] = struct{}{}
		prev, known := p.seen[key]
		p.seen[key] = pr.Check
		if !known || prev == pr.Check || !isSettledCheck(pr.Check) {
			// A first sighting seeds; an unchanged verdict is not news; and a flip
			// INTO pending is the run starting, which the user caused by pushing.
			continue
		}
		p.push.Send(ctx, push.DefaultTitle, prStatusBody(pr), api.PushKindPRStatus,
			api.PRSubject(pr.ForgeID, pr.Repo, pr.Number))
	}
	// Drop the PRs that closed or merged since the last tick, so a process running
	// for weeks holds one entry per OPEN pull request rather than per PR ever seen.
	for key := range p.seen {
		if _, ok := live[key]; !ok {
			delete(p.seen, key)
		}
	}
}

// prSubjectKey is the poller's own state key, and it is the notification's subject
// key so the two cannot describe different things.
func prSubjectKey(pr WatchedPR) string {
	return api.PRSubject(pr.ForgeID, pr.Repo, pr.Number).Key
}

// isSettledCheck reports whether a verdict is one worth interrupting for.
//
// Only green and red are. "pending" is the run STARTING, which the user caused by
// pushing seconds earlier, and "" means the forge reported no checks at all —
// notifying on either would make the channel noise and teach the user to ignore
// it.
func isSettledCheck(check string) bool {
	return check == checkPassing || check == checkFailing
}

// prStatusBody is what the notification says. Short by design: the tray truncates,
// and fitToCap trims against the payload cap, so the verdict and the number lead
// and the title follows.
func prStatusBody(pr WatchedPR) string {
	verdict := "checks failed"
	if pr.Check == checkPassing {
		verdict = "checks passed"
	}
	body := pr.Repo + " #" + strconv.Itoa(pr.Number) + " " + verdict
	if pr.Title != "" {
		body += ": " + pr.Title
	}
	return body
}
