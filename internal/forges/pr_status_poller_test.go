package forges

// D101: push when a pull request you opened flips green or red.
//
// The two gates are the decision's own cost claim ("costs zero with nothing
// pending"), so they are asserted as ABSENCE OF WORK rather than as absence of a
// notification: with no subscribers the source is never consulted at all, and with
// no open PR the per-PR pass never runs.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// fakeSource counts how often it was asked, which is what makes "no forge work"
// assertable.
type fakeSource struct {
	err   error
	prs   []WatchedPR
	calls int
}

func (f *fakeSource) OpenAuthoredPRs(context.Context) ([]WatchedPR, error) {
	f.calls++
	return f.prs, f.err
}

type sentPush struct {
	body    string
	kind    api.PushKind
	subject api.PushSubject
}

// fakeNotifier records every Send and reports a configurable subscriber state.
type fakeNotifier struct {
	sent        []sentPush
	subscribers bool
	asked       int
}

func (f *fakeNotifier) HasSubscribers() bool {
	f.asked++
	return f.subscribers
}

func (f *fakeNotifier) Send(_ context.Context, _, body string, kind api.PushKind, subject api.PushSubject) {
	f.sent = append(f.sent, sentPush{body: body, kind: kind, subject: subject})
}

func newTestPoller(src PRSource, n PRNotifier) *PRStatusPoller {
	p := NewPRStatusPoller(src, n)
	// Only Run reads these; sweep is driven directly.
	p.tick = time.Millisecond
	p.discovery = time.Millisecond
	return p
}

func pr(number int, check string) WatchedPR {
	return WatchedPR{
		ForgeID: "github:github.com",
		Repo:    "cplieger/vibekit",
		Number:  number,
		Title:   "A change",
		Check:   check,
	}
}

// TestPoller_DoesNoForgeWorkWithoutSubscribers is gate 1, and the assertion is on
// the SOURCE, not on the notifications: each forge call is a subprocess with a 60s
// timeout, so a tick that listed first and checked subscribers second would spend
// them on a box nobody subscribed from.
func TestPoller_DoesNoForgeWorkWithoutSubscribers(t *testing.T) {
	src := &fakeSource{prs: []WatchedPR{pr(1, checkFailing)}}
	n := &fakeNotifier{subscribers: false}
	p := newTestPoller(src, n)

	p.sweep(t.Context())
	p.sweep(t.Context())

	if src.calls != 0 {
		t.Errorf("the source was consulted %d times with no subscribers; the tick must cost nothing", src.calls)
	}
	if len(n.sent) != 0 {
		t.Errorf("pushed %d notifications with no subscribers", len(n.sent))
	}
	if n.asked == 0 {
		t.Error("the subscriber gate was never consulted")
	}
}

// TestPoller_DoesNoPerPRWorkWithNoOpenPR is gate 2: one source call, then nothing.
func TestPoller_DoesNoPerPRWorkWithNoOpenPR(t *testing.T) {
	src := &fakeSource{prs: nil}
	n := &fakeNotifier{subscribers: true}
	p := newTestPoller(src, n)

	p.sweep(t.Context())

	if src.calls != 1 {
		t.Errorf("source calls = %d, want 1", src.calls)
	}
	if len(n.sent) != 0 {
		t.Errorf("pushed %d notifications with no open PR", len(n.sent))
	}
	if len(p.seen) != 0 {
		t.Errorf("kept %d state entries with no open PR", len(p.seen))
	}
}

// TestPoller_FirstSightingSeedsSilently is the boot rule: without it every restart
// would announce every open PR, and a PR that went green while the container was
// down would arrive as a fresh alert hours late.
func TestPoller_FirstSightingSeedsSilently(t *testing.T) {
	src := &fakeSource{prs: []WatchedPR{pr(7, checkPassing)}}
	n := &fakeNotifier{subscribers: true}
	p := newTestPoller(src, n)

	p.sweep(t.Context())

	if len(n.sent) != 0 {
		t.Errorf("a first sighting pushed %d notifications: %+v", len(n.sent), n.sent)
	}
	if got := p.seen[prSubjectKey(pr(7, ""))]; got != checkPassing {
		t.Errorf("state after seeding = %q, want %q", got, checkPassing)
	}
}

// TestPoller_PushesOnASettledFlip is the feature.
func TestPoller_PushesOnASettledFlip(t *testing.T) {
	cases := []struct {
		name     string
		from     string
		to       string
		wantBody string
	}{
		{name: "PendingToPassing", from: checkPending, to: checkPassing, wantBody: "checks passed"},
		{name: "PendingToFailing", from: checkPending, to: checkFailing, wantBody: "checks failed"},
		{name: "PassingToFailing", from: checkPassing, to: checkFailing, wantBody: "checks failed"},
		{name: "FailingToPassing", from: checkFailing, to: checkPassing, wantBody: "checks passed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeSource{prs: []WatchedPR{pr(42, tc.from)}}
			n := &fakeNotifier{subscribers: true}
			p := newTestPoller(src, n)
			p.sweep(t.Context()) // seed

			src.prs = []WatchedPR{pr(42, tc.to)}
			p.sweep(t.Context())

			if len(n.sent) != 1 {
				t.Fatalf("sent %d notifications for a %s -> %s flip, want 1: %+v",
					len(n.sent), tc.from, tc.to, n.sent)
			}
			got := n.sent[0]
			if got.kind != api.PushKindPRStatus {
				t.Errorf("kind = %q, want %q", got.kind, api.PushKindPRStatus)
			}
			if !strings.Contains(got.body, tc.wantBody) {
				t.Errorf("body %q does not say %q", got.body, tc.wantBody)
			}
			if !strings.Contains(got.body, "#42") {
				t.Errorf("body %q does not name the PR", got.body)
			}
		})
	}
}

// TestPoller_DoesNotPushIntoPendingOrNoChecks: a run STARTING is not news (the
// user caused it seconds earlier by pushing) and "no checks at all" is not a
// verdict, so neither interrupts.
func TestPoller_DoesNotPushIntoPendingOrNoChecks(t *testing.T) {
	for _, to := range []string{checkPending, ""} {
		t.Run("to_"+to, func(t *testing.T) {
			src := &fakeSource{prs: []WatchedPR{pr(3, checkPassing)}}
			n := &fakeNotifier{subscribers: true}
			p := newTestPoller(src, n)
			p.sweep(t.Context())

			src.prs = []WatchedPR{pr(3, to)}
			p.sweep(t.Context())

			if len(n.sent) != 0 {
				t.Errorf("pushed on a flip into %q: %+v", to, n.sent)
			}
			// The state still MOVES, so the next flip back to passing is a change
			// rather than a repeat that gets swallowed.
			if got := p.seen[prSubjectKey(pr(3, ""))]; got != to {
				t.Errorf("state = %q, want %q", got, to)
			}
		})
	}
}

// TestPoller_UnchangedVerdictIsSilent: a PR sitting green for an hour is sixty
// ticks and zero notifications.
func TestPoller_UnchangedVerdictIsSilent(t *testing.T) {
	src := &fakeSource{prs: []WatchedPR{pr(9, checkPending)}}
	n := &fakeNotifier{subscribers: true}
	p := newTestPoller(src, n)
	p.sweep(t.Context())
	src.prs = []WatchedPR{pr(9, checkFailing)}
	p.sweep(t.Context())
	for range 10 {
		p.sweep(t.Context())
	}
	if len(n.sent) != 1 {
		t.Errorf("sent %d notifications for one flip followed by ten unchanged ticks", len(n.sent))
	}
}

// TestPoller_SubjectIsPerPR is the coalescing-tag half of the reuse: two PRs
// flipping inside one window must occupy their own tray slots.
func TestPoller_SubjectIsPerPR(t *testing.T) {
	src := &fakeSource{prs: []WatchedPR{pr(1, checkPending), pr(2, checkPending)}}
	n := &fakeNotifier{subscribers: true}
	p := newTestPoller(src, n)
	p.sweep(t.Context())

	src.prs = []WatchedPR{pr(1, checkPassing), pr(2, checkFailing)}
	p.sweep(t.Context())

	if len(n.sent) != 2 {
		t.Fatalf("sent %d notifications for two flips, want 2: %+v", len(n.sent), n.sent)
	}
	if n.sent[0].subject.Key == n.sent[1].subject.Key {
		t.Errorf("two PRs share a subject key %q, so one would replace the other in the tray",
			n.sent[0].subject.Key)
	}
	for _, s := range n.sent {
		if !strings.HasPrefix(s.subject.Key, api.PRSubjectPrefix) {
			t.Errorf("subject %q lacks the PR prefix the client routes on", s.subject.Key)
		}
		if s.subject.ChatID != "" {
			t.Errorf("a PR notification carries a chat id (%q); it has no chat behind it", s.subject.ChatID)
		}
	}
}

// TestPoller_ForgetsAClosedPR keeps the state bounded by OPEN pull requests rather
// than by every PR the process ever saw.
func TestPoller_ForgetsAClosedPR(t *testing.T) {
	src := &fakeSource{prs: []WatchedPR{pr(1, checkPassing), pr(2, checkPassing)}}
	n := &fakeNotifier{subscribers: true}
	p := newTestPoller(src, n)
	p.sweep(t.Context())
	if len(p.seen) != 2 {
		t.Fatalf("state entries = %d, want 2", len(p.seen))
	}
	src.prs = []WatchedPR{pr(1, checkPassing)}
	p.sweep(t.Context())
	if len(p.seen) != 1 {
		t.Errorf("state entries = %d after a PR merged, want 1", len(p.seen))
	}
}

// TestPoller_UnsubscribingResetsTheState: a device subscribing later must not be
// told about every flip that happened while nobody could receive one.
func TestPoller_UnsubscribingResetsTheState(t *testing.T) {
	src := &fakeSource{prs: []WatchedPR{pr(5, checkPending)}}
	n := &fakeNotifier{subscribers: true}
	p := newTestPoller(src, n)
	p.sweep(t.Context())

	n.subscribers = false
	p.sweep(t.Context())
	if len(p.seen) != 0 {
		t.Errorf("state survived losing every subscriber: %+v", p.seen)
	}

	n.subscribers = true
	src.prs = []WatchedPR{pr(5, checkFailing)}
	p.sweep(t.Context())
	if len(n.sent) != 0 {
		t.Errorf("re-subscribing announced a flip that happened while nobody listened: %+v", n.sent)
	}
}

// TestPoller_SourceFailureIsSurvivable: a forge that is down, rate-limited or
// logged out must not silence the loop or lose the state it already holds.
func TestPoller_SourceFailureIsSurvivable(t *testing.T) {
	src := &fakeSource{prs: []WatchedPR{pr(1, checkPending)}}
	n := &fakeNotifier{subscribers: true}
	p := newTestPoller(src, n)
	p.sweep(t.Context())

	src.err = errors.New("gh: rate limited")
	src.prs = nil
	p.sweep(t.Context())
	if len(p.seen) != 1 {
		t.Errorf("a failed listing discarded the state: %+v", p.seen)
	}

	src.err = nil
	src.prs = []WatchedPR{pr(1, checkPassing)}
	p.sweep(t.Context())
	if len(n.sent) != 1 {
		t.Errorf("sent %d notifications after recovering, want 1", len(n.sent))
	}
}

// TestPoller_RunStopsOnContextCancel pins the shutdown contract: cancellation and
// nothing else, with no goroutine left running.
func TestPoller_RunStopsOnContextCancel(t *testing.T) {
	src := &fakeSource{}
	n := &fakeNotifier{subscribers: false}
	p := newTestPoller(src, n)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// TestPoller_TwoRates is the honest cost model: the 60-second rate is spent only
// while something is being tracked, and an idle box falls back to discovery.
//
// The rate selector is `seen`, so this asserts the pairing rather than a wall clock:
// a sweep that found PRs leaves the active rate armed, and one that found none (or
// lost every subscriber) leaves the discovery rate armed.
func TestPoller_TwoRates(t *testing.T) {
	src := &fakeSource{}
	n := &fakeNotifier{subscribers: true}
	p := NewPRStatusPoller(src, n)

	if got := p.nextDelay(); got != PRDiscoveryInterval {
		t.Errorf("a fresh poller arms %v, want the discovery interval %v", got, PRDiscoveryInterval)
	}

	src.prs = []WatchedPR{pr(1, checkPending)}
	p.sweep(t.Context())
	if got := p.nextDelay(); got != PRPollInterval {
		t.Errorf("with a PR tracked the poller arms %v, want the active interval %v", got, PRPollInterval)
	}

	src.prs = nil
	p.sweep(t.Context())
	if got := p.nextDelay(); got != PRDiscoveryInterval {
		t.Errorf("with the PR closed the poller arms %v, want to fall back to discovery %v",
			got, PRDiscoveryInterval)
	}

	src.prs = []WatchedPR{pr(1, checkPending)}
	p.sweep(t.Context())
	n.subscribers = false
	p.sweep(t.Context())
	if got := p.nextDelay(); got != PRDiscoveryInterval {
		t.Errorf("with every subscriber gone the poller arms %v, want discovery %v",
			got, PRDiscoveryInterval)
	}

	// The active rate is faster than discovery, or the two names mean nothing.
	if PRPollInterval >= PRDiscoveryInterval {
		t.Errorf("the active interval (%v) is not faster than discovery (%v)",
			PRPollInterval, PRDiscoveryInterval)
	}
}

// slowSource records when each listing started and finished, and takes `delay` to
// answer. The delay IS the fixture: it models several slow repos, each ListPRs
// bounded at 60s, which is the case that turned the loop hot.
type slowSource struct {
	delay  time.Duration
	mu     sync.Mutex
	starts []time.Time
	ends   []time.Time
}

func (s *slowSource) OpenAuthoredPRs(context.Context) ([]WatchedPR, error) {
	s.mu.Lock()
	s.starts = append(s.starts, time.Now())
	s.mu.Unlock()
	time.Sleep(s.delay)
	s.mu.Lock()
	s.ends = append(s.ends, time.Now())
	s.mu.Unlock()
	return []WatchedPR{pr(1, checkPending)}, nil
}

// TestPoller_SchedulesFromCompletion is the anti-hot-loop rule. A ticker retains one
// tick while the receiver is busy, so an overlong sweep returned and immediately
// consumed it — running forge subprocesses back to back with no quiet period at all.
// Scheduling from completion makes the interval a floor on the GAP.
//
// The assertion is on the observed gap between one listing finishing and the next
// starting, measured by the fixture rather than against the production budget. A
// ticker leaves that gap at effectively zero, so this fails closed on the old shape.
func TestPoller_SchedulesFromCompletion(t *testing.T) {
	const interval = 40 * time.Millisecond
	src := &slowSource{delay: 2 * interval}
	n := &fakeNotifier{subscribers: true}
	p := NewPRStatusPoller(src, n)
	p.tick = interval
	p.discovery = interval

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	// Long enough for three sweeps at (delay + interval) each, plus slack.
	time.Sleep(6 * (src.delay + interval))
	cancel()
	<-done

	src.mu.Lock()
	starts := append([]time.Time(nil), src.starts...)
	ends := append([]time.Time(nil), src.ends...)
	src.mu.Unlock()

	if len(starts) < 2 {
		t.Fatalf("only %d sweeps ran; the fixture cannot witness the gap", len(starts))
	}
	// Half the interval, not the whole one: timer firing has real imprecision, and
	// the defect being caught leaves the gap near zero rather than merely short.
	floor := interval / 2
	for i := 0; i+1 < len(starts) && i < len(ends); i++ {
		if gap := starts[i+1].Sub(ends[i]); gap < floor {
			t.Errorf("sweep %d started %v after sweep %d finished, want at least %v: "+
				"the next sweep is being scheduled from a retained tick rather than from completion",
				i+1, gap, i, floor)
		}
	}
}

// TestPoller_RunDoesNotSweepOnEntry: the first sweep only seeds, so sweeping at
// boot would spend a subprocess per repo during startup to learn what the next
// sweep learns for free.
func TestPoller_RunDoesNotSweepOnEntry(t *testing.T) {
	src := &fakeSource{}
	n := &fakeNotifier{subscribers: true}
	p := NewPRStatusPoller(src, n)
	// No sweep will fire inside this test, at either rate.
	p.tick = time.Hour
	p.discovery = time.Hour
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	cancel()
	<-done
	if n.asked != 0 || src.calls != 0 {
		t.Errorf("Run swept on entry (gate asked %d, source called %d)", n.asked, src.calls)
	}
}

// TestPRStatusBody covers the wording without a poller: the notice has to name the
// repo, the number and the verdict, because a tray banner is all the reader gets.
func TestPRStatusBody(t *testing.T) {
	got := prStatusBody(WatchedPR{Repo: "cplieger/vibekit", Number: 12, Title: "Fix the thing", Check: checkPassing})
	for _, want := range []string{"cplieger/vibekit", "#12", "checks passed", "Fix the thing"} {
		if !strings.Contains(got, want) {
			t.Errorf("body %q missing %q", got, want)
		}
	}
	// A PR with no title still says what happened.
	bare := prStatusBody(WatchedPR{Repo: "a/b", Number: 1, Check: checkFailing})
	if !strings.Contains(bare, "checks failed") || strings.HasSuffix(bare, ": ") {
		t.Errorf("titleless body = %q", bare)
	}
}

// TestProviderCheckVerdicts_MatchTheStatedScope ties the poller's documented
// provider scope to the provider code that decides it.
//
// The verdict comes from the LIST call only — no per-PR status fetch, no
// compensating layer — so the feature is exactly as complete as each provider's list
// response, and the settings copy promises exactly that. This is what stops the
// promise and the providers drifting apart silently: a provider that gains or loses
// an edge fails here, next to the sentence that has to be rewritten.
func TestProviderCheckVerdicts_MatchTheStatedScope(t *testing.T) {
	t.Run("GitHubNotifiesBothEdges", func(t *testing.T) {
		green, _, _ := summarizeGHRollup([]ghRollupEntry{
			{Status: "COMPLETED", Conclusion: "SUCCESS"},
		})
		red, _, _ := summarizeGHRollup([]ghRollupEntry{
			{Status: "COMPLETED", Conclusion: "FAILURE"},
		})
		if !isSettledCheck(green) || !isSettledCheck(red) {
			t.Errorf("github verdicts (%q, %q) are not both settled; the copy promises both edges",
				green, red)
		}
		if green == red {
			t.Errorf("github folds success and failure to the same verdict %q", green)
		}
	})

	t.Run("GitLabNotifiesAFailureAndNeverARecovery", func(t *testing.T) {
		// Every value GitLab documents for detailed_merge_status that could
		// plausibly be read as a CI statement, plus the one that tempts a green
		// chip. `mergeable` must NOT become passing: a project that does not
		// require pipelines is mergeable with a red pipeline.
		vocabulary := []string{
			"mergeable", "ci_still_running", "ci_must_pass", "checking", "unchecked",
			"preparing", "draft_status", "conflict", "need_rebase", "not_approved",
			"discussions_not_resolved", "broken_status", "",
		}
		sawFailing := false
		for _, v := range vocabulary {
			got := mapGLabCheckStatus(v)
			if got == checkPassing {
				t.Errorf("mapGLabCheckStatus(%q) = %q: GitLab cannot state a pass, so a "+
					"recovery notification would be a guess", v, got)
			}
			if got == checkFailing {
				sawFailing = true
			}
		}
		if !sawFailing {
			t.Error("no GitLab value maps to failing, so GitLab notifies on nothing at all " +
				"and the copy overstates it")
		}
	})

	t.Run("GiteaNotifiesNothing", func(t *testing.T) {
		const payload = `[{"number":4,"title":"Ready","state":"open","mergeable":true,
		  "head":{"ref":"feat","sha":"aaaaaaa1111"},"base":{"ref":"main"}}]`
		prs, err := newGitea(KindGitea, "gitea.example").parsePRs([]byte(payload))
		if err != nil {
			t.Fatalf("parsePRs: %v", err)
		}
		if len(prs) != 1 {
			t.Fatalf("parsePRs returned %d PRs, want 1", len(prs))
		}
		if isSettledCheck(prs[0].CheckStatus) {
			t.Errorf("gitea reported a settled verdict %q; the copy says Gitea notifies nothing",
				prs[0].CheckStatus)
		}
	})
}
