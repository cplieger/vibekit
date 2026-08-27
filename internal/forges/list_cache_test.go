package forges

// The listing cache exists so arriving at a view costs no forge subprocess when
// the answer is already known, and every case here is about a CALL COUNT rather
// than a returned value: the value was never in doubt, the work was.
//
// The time-dependent cases run in a synctest BUBBLE. A TTL boundary is otherwise
// only reachable by sleeping past it, which trades a real second per case for an
// assertion that can still flake; in a bubble the clock advances only when every
// goroutine is durably blocked, so "one nanosecond past the TTL" is exact and the
// revalidation goroutine is joinable without polling for it.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/sync/semaphore"
)

// countingFill records how often the cache reached through it, and answers a
// different value each time, so a test can tell a cache hit from a refill.
type countingFill struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *countingFill) fill(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return []string{"v", string(rune('0' + f.calls))}, nil
}

func (f *countingFill) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newTestCache(ttl time.Duration) *listCache[[]string] {
	return newListCache[[]string](ttl, semaphore.NewWeighted(maxListFills))
}

func TestListCache_ColdFillThenServesFromCache(t *testing.T) {
	c := newTestCache(time.Hour)
	f := &countingFill{}

	first, err := c.get(t.Context(), "k", false, f.fill)
	if err != nil {
		t.Fatalf("get(cold) error = %v, want nil", err)
	}
	if len(first) != 2 {
		t.Fatalf("get(cold) = %v, want a 2-element listing", first)
	}
	if got := f.count(); got != 1 {
		t.Errorf("fill calls after cold get = %d, want 1", got)
	}

	// The whole point: a second read inside the TTL touches no forge.
	second, err := c.get(t.Context(), "k", false, f.fill)
	if err != nil {
		t.Fatalf("get(fresh) error = %v, want nil", err)
	}
	if got := f.count(); got != 1 {
		t.Errorf("fill calls after fresh get = %d, want 1 (served from cache)", got)
	}
	if first[1] != second[1] {
		t.Errorf("get(fresh) = %v, want the cached %v", second, first)
	}
}

func TestListCache_KeysAreIndependent(t *testing.T) {
	c := newTestCache(time.Hour)
	f := &countingFill{}

	for _, key := range []string{"a", "b"} {
		if _, err := c.get(t.Context(), key, false, f.fill); err != nil {
			t.Fatalf("get(%q) error = %v, want nil", key, err)
		}
	}
	if got := f.count(); got != 2 {
		t.Errorf("fill calls for two keys = %d, want 2", got)
	}
}

func TestListCache_ForceBypassesAFreshEntry(t *testing.T) {
	c := newTestCache(time.Hour)
	f := &countingFill{}

	if _, err := c.get(t.Context(), "k", false, f.fill); err != nil {
		t.Fatalf("get(cold) error = %v, want nil", err)
	}
	// A refresh control asks for the truth, so a fresh entry is no reason to
	// answer from it.
	got, err := c.get(t.Context(), "k", true, f.fill)
	if err != nil {
		t.Fatalf("get(force) error = %v, want nil", err)
	}
	if calls := f.count(); calls != 2 {
		t.Errorf("fill calls after forced get = %d, want 2", calls)
	}
	if got[1] != "2" {
		t.Errorf("get(force) = %v, want the second fill's value", got)
	}
}

func TestListCache_StaleEntryServesAtOnceThenRevalidates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const ttl = time.Minute
		c := newTestCache(ttl)
		// The revalidation must be observable as an EVENT rather than as a count
		// read after a sleep, or the test cannot tell "refreshed" from "not yet".
		refreshed := make(chan struct{}, 1)
		f := &countingFill{}
		fill := func(ctx context.Context) ([]string, error) {
			v, err := f.fill(ctx)
			select {
			case refreshed <- struct{}{}:
			default:
			}
			return v, err
		}

		if _, err := c.get(t.Context(), "k", false, fill); err != nil {
			t.Fatalf("get(cold) error = %v, want nil", err)
		}
		<-refreshed
		synctest.Sleep(ttl + time.Nanosecond)

		// Served from the stale copy: the caller gets the FIRST fill's value even
		// though a second one is on its way.
		got, err := c.get(t.Context(), "k", false, fill)
		if err != nil {
			t.Fatalf("get(stale) error = %v, want nil", err)
		}
		if got[1] != "1" {
			t.Errorf("get(stale) = %v, want the cached first value (served, not awaited)", got)
		}

		<-refreshed
		synctest.Wait()
		if calls := f.count(); calls != 2 {
			t.Fatalf("fill calls after stale get = %d, want 2 (one cold, one revalidation)", calls)
		}
		// And the revalidation landed, so the NEXT read is the fresh value with no
		// further work.
		next, err := c.get(t.Context(), "k", false, fill)
		if err != nil {
			t.Fatalf("get(after revalidation) error = %v, want nil", err)
		}
		if next[1] != "2" {
			t.Errorf("get(after revalidation) = %v, want the revalidated value", next)
		}
		if calls := f.count(); calls != 2 {
			t.Errorf("fill calls after the revalidated read = %d, want 2", calls)
		}
	})
}

func TestListCache_OneRevalidationAtATime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const ttl = time.Minute
		c := newTestCache(ttl)
		release := make(chan struct{})
		var inFlight atomic.Int64
		f := &countingFill{}
		fill := func(ctx context.Context) ([]string, error) {
			inFlight.Add(1)
			<-release
			return f.fill(ctx)
		}

		go func() { close(release) }()
		if _, err := c.get(t.Context(), "k", false, fill); err != nil {
			t.Fatalf("get(cold) error = %v, want nil", err)
		}
		synctest.Sleep(ttl + time.Nanosecond)

		// A blocked revalidation must not be joined by every later reader: a stale
		// entry read ten times in a row is one refresh, not ten.
		release = make(chan struct{})
		inFlight.Store(0)
		for range 10 {
			if _, err := c.get(t.Context(), "k", false, fill); err != nil {
				t.Fatalf("get(stale) error = %v, want nil", err)
			}
		}
		synctest.Wait()
		if got := inFlight.Load(); got != 1 {
			t.Errorf("revalidations in flight after 10 stale reads = %d, want 1", got)
		}
		close(release)
		synctest.Wait()
	})
}

func TestListCache_ConcurrentColdReadsShareOneFill(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newTestCache(time.Hour)
		release := make(chan struct{})
		f := &countingFill{}
		fill := func(ctx context.Context) ([]string, error) {
			<-release
			return f.fill(ctx)
		}

		const readers = 8
		var wg sync.WaitGroup
		for range readers {
			wg.Go(func() {
				if _, err := c.get(t.Context(), "k", false, fill); err != nil {
					t.Errorf("get(concurrent cold) error = %v, want nil", err)
				}
			})
		}
		// Every reader is parked on the shared fetch before it is allowed to run,
		// which is what makes the count below a statement about sharing rather
		// than about scheduling luck.
		synctest.Wait()
		close(release)
		wg.Wait()

		if got := f.count(); got != 1 {
			t.Errorf("fill calls for %d concurrent cold reads = %d, want 1", readers, got)
		}
	})
}

func TestListCache_FillErrorIsReturnedAndNotCached(t *testing.T) {
	c := newTestCache(time.Hour)
	wantErr := errors.New("gh: exit status 1")
	f := &countingFill{err: wantErr}

	if _, err := c.get(t.Context(), "k", false, f.fill); !errors.Is(err, wantErr) {
		t.Fatalf("get(failing) error = %v, want %v", err, wantErr)
	}
	// A failure must not become a cached empty listing, or one bad round trip
	// renders "no pull requests" until the TTL expires.
	f.err = nil
	got, err := c.get(t.Context(), "k", false, f.fill)
	if err != nil {
		t.Fatalf("get(after failure) error = %v, want nil", err)
	}
	if len(got) == 0 {
		t.Errorf("get(after failure) = %v, want the retried listing", got)
	}
	if calls := f.count(); calls != 2 {
		t.Errorf("fill calls across a failure and a retry = %d, want 2", calls)
	}
}

func TestListCache_CancelledReadDoesNotBlock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newTestCache(time.Hour)
		f := &countingFill{}
		fill := func(ctx context.Context) ([]string, error) {
			<-ctx.Done()
			return f.fill(ctx)
		}

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := c.get(ctx, "k", false, fill)
			done <- err
		}()
		synctest.Wait()
		cancel()

		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("get(cancelled) error = %v, want context.Canceled", err)
		}
	})
}

func TestListCache_ClearDropsEntriesAndInFlightFills(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newTestCache(time.Hour)
		release := make(chan struct{})
		f := &countingFill{}
		fill := func(ctx context.Context) ([]string, error) {
			<-release
			return f.fill(ctx)
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			if _, err := c.get(t.Context(), "k", false, fill); err != nil {
				t.Errorf("get(cold) error = %v, want nil", err)
			}
		}()
		synctest.Wait()

		// A sign-in landing here is the real case: the fill already read the forge
		// as the PREVIOUS account, so its answer must reach the caller who asked
		// and go no further.
		c.clear()
		close(release)
		<-done

		if _, ok := c.entries["k"]; ok {
			t.Error("entry cached by a fill that started before clear(); want it dropped")
		}
		if _, err := c.get(t.Context(), "k", false, f.fill); err != nil {
			t.Fatalf("get(after clear) error = %v, want nil", err)
		}
		if calls := f.count(); calls != 2 {
			t.Errorf("fill calls after clear = %d, want 2 (the dropped one, then a real refill)", calls)
		}
	})
}

func TestListCache_FillsAreBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const limit = 3
		c := newListCache[[]string](time.Hour, semaphore.NewWeighted(limit))
		release := make(chan struct{})
		var live, peak atomic.Int64
		fill := func(context.Context) ([]string, error) {
			n := live.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			<-release
			live.Add(-1)
			return []string{"v"}, nil
		}

		var wg sync.WaitGroup
		for i := range limit * 4 {
			key := string(rune('a' + i))
			wg.Go(func() {
				if _, err := c.get(t.Context(), key, false, fill); err != nil {
					t.Errorf("get(%q) error = %v, want nil", key, err)
				}
			})
		}
		synctest.Wait()
		if got := peak.Load(); got > limit {
			t.Errorf("concurrent fills = %d, want at most %d", got, limit)
		}
		close(release)
		wg.Wait()
	})
}

// fakeOps is a ForgeOps that counts the three calls these cases exercise.
// Everything else is promoted from the embedded nil interface, which is what
// makes an accidental call to an unlisted method a panic rather than a silent
// zero value.
type fakeOps struct {
	ForgeOps
	mu     sync.Mutex
	repos  int
	prs    int
	merges int
	seen   []string
}

func (f *fakeOps) ListRepos(context.Context) ([]Repo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repos++
	return []Repo{{Owner: "o", Name: "n", FullName: "o/n"}}, nil
}

func (f *fakeOps) ListPRs(_ context.Context, repo string, state ListState) ([]PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prs++
	f.seen = append(f.seen, repo+" "+string(state))
	return []PR{{Number: f.prs, Title: repo}}, nil
}

func (f *fakeOps) MergePR(context.Context, string, int, MergeOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merges++
	return nil
}

func (f *fakeOps) counts() (repos, prs, merges int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repos, f.prs, f.merges
}

func newDecorated(force bool) (cachedListings, *fakeOps) {
	f := &fakeOps{}
	return cachedListings{ForgeOps: f, caches: newListCaches(), forgeID: "github:github.com", force: force}, f
}

func TestCachedListings_ListingsAreCachedAndMutationsAreNot(t *testing.T) {
	c, f := newDecorated(false)
	ctx := t.Context()

	for range 3 {
		if _, err := c.ListRepos(ctx); err != nil {
			t.Fatalf("ListRepos() error = %v, want nil", err)
		}
		if _, err := c.ListPRs(ctx, "o/n", StateOpen); err != nil {
			t.Fatalf("ListPRs() error = %v, want nil", err)
		}
		// A merge is the reason the decorator overrides two methods rather than
		// wrapping the interface: caching one would report a landed merge that
		// never happened, or refuse a second one as already answered.
		if err := c.MergePR(ctx, "o/n", 1, MergeOptions{}); err != nil {
			t.Fatalf("MergePR() error = %v, want nil", err)
		}
	}

	repos, prs, merges := f.counts()
	if repos != 1 {
		t.Errorf("ListRepos reached the forge %d times over 3 calls, want 1", repos)
	}
	if prs != 1 {
		t.Errorf("ListPRs reached the forge %d times over 3 calls, want 1", prs)
	}
	if merges != 3 {
		t.Errorf("MergePR reached the forge %d times over 3 calls, want 3 (never cached)", merges)
	}
}

func TestCachedListings_KeyedByRepoAndState(t *testing.T) {
	c, f := newDecorated(false)
	ctx := t.Context()

	calls := []struct {
		repo  string
		state ListState
	}{
		{"o/one", StateOpen},
		{"o/two", StateOpen},
		{"o/one", StateClosed},
		{"o/one", StateOpen}, // the only repeat
	}
	for _, call := range calls {
		if _, err := c.ListPRs(ctx, call.repo, call.state); err != nil {
			t.Fatalf("ListPRs(%q, %q) error = %v, want nil", call.repo, call.state, err)
		}
	}

	_, prs, _ := f.counts()
	// Three distinct listings, so a shared key would show as a count below 3 and
	// a missing key as 4.
	if prs != 3 {
		t.Errorf("ListPRs reached the forge %d times for 3 distinct listings, want 3 (got %v)", prs, f.seen)
	}
}

func TestCachedListings_ForceReachesTheForgeEveryTime(t *testing.T) {
	c, f := newDecorated(true)
	ctx := t.Context()

	for range 3 {
		if _, err := c.ListPRs(ctx, "o/n", StateOpen); err != nil {
			t.Fatalf("ListPRs() error = %v, want nil", err)
		}
	}
	if _, prs, _ := f.counts(); prs != 3 {
		t.Errorf("forced ListPRs reached the forge %d times over 3 calls, want 3", prs)
	}
}

func TestManager_InvalidateDropsCachedListings(t *testing.T) {
	m := NewManager()
	f := &fakeOps{}
	c := cachedListings{ForgeOps: f, caches: m.lists, forgeID: "github:github.com"}
	ctx := t.Context()

	if _, err := c.ListRepos(ctx); err != nil {
		t.Fatalf("ListRepos() error = %v, want nil", err)
	}
	// A sign-in or a disconnect decides which repositories are visible at all, so
	// the previous account's listing must not survive it.
	m.Invalidate()
	if _, err := c.ListRepos(ctx); err != nil {
		t.Fatalf("ListRepos(after Invalidate) error = %v, want nil", err)
	}

	if repos, _, _ := f.counts(); repos != 2 {
		t.Errorf("ListRepos reached the forge %d times across an Invalidate, want 2", repos)
	}
}

func TestManager_ProviderCachesAndProviderFreshDoesNot(t *testing.T) {
	// Provider resolves a real CLI-backed ForgeOps, so this asserts the WIRING —
	// which of the two constructors sets force — rather than driving a forge.
	m := NewManager()
	m.forges["github:github.com"] = &ConfiguredForge{
		ID: "github:github.com", Kind: KindGitHub, Host: "github.com", Connected: true,
	}

	cached, err := m.Provider("github:github.com")
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	c, ok := cached.(cachedListings)
	if !ok {
		t.Fatalf("Provider() returned %T, want cachedListings", cached)
	}
	if c.force {
		t.Error("Provider().force = true, want false (an arrival must be servable from cache)")
	}

	fresh, err := m.ProviderFresh("github:github.com")
	if err != nil {
		t.Fatalf("ProviderFresh() error = %v, want nil", err)
	}
	fc, ok := fresh.(cachedListings)
	if !ok {
		t.Fatalf("ProviderFresh() returned %T, want cachedListings", fresh)
	}
	if !fc.force {
		t.Error("ProviderFresh().force = false, want true")
	}
	if fc.caches != c.caches {
		t.Error("ProviderFresh() holds a different cache; want the Manager's one cache, or a forced read replaces nothing")
	}
}

// TestListCacheTTLs pins the two windows, with the values written out rather
// than compared to each other. A self-referential assertion cannot see a
// constant collapse — `time.Minute` mutated to `0` satisfies
// `prListTTL < repoListTTL` just as well as the real value does — and these two
// numbers are the whole of the staleness argument the cache rests on.
func TestListCacheTTLs(t *testing.T) {
	// A repository set moves when someone clones, creates or archives one, and a
	// connection change clears the cache outright rather than waiting for this.
	if repoListTTL != 5*time.Minute {
		t.Errorf("repoListTTL = %v, want 5m", repoListTTL)
	}
	// The PR window is short because of CI rather than the PR set: a row carries
	// the folded check verdict of its head commit, so a stale entry can show a
	// green chip for a commit whose checks have since gone red. Widening this
	// widens exactly that window; MergeOptions.HeadSHA only stops the wrong
	// COMMIT being merged, not a merge against a changed verdict.
	if prListTTL != time.Minute {
		t.Errorf("prListTTL = %v, want 1m", prListTTL)
	}
	// The bound is a spike ceiling: a cold PRs tab asks for one listing per
	// cloned repository at once. It has to stay well above 1, or a cold visit
	// serialises and the client abandons the fan-out after 20s.
	if maxListFills != 16 {
		t.Errorf("maxListFills = %d, want 16", maxListFills)
	}
}
