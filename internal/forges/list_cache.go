// list_cache.go — a read-through cache over the two forge LISTING calls.
//
// Why only these two: the PRs tab aggregates client-side, so one visit is
// `GET /api/forges`, then one `/repos` per connected forge, then one
// `/prs` per repository — and each of those last two is a `gh`/`glab`/`tea`
// subprocess plus an upstream API call. Nothing was cached, so the cost was
// paid again on every visit, and the repo listing is worse than its share
// suggests: no PR request can start until `gh repo list --limit 300` returns.
//
// What this deliberately does NOT do is run anything in the background. There
// is no ticker and no goroutine at rest; a fill happens only because a request
// asked for the data. The one goroutine that exists is the revalidation of an
// already-served stale value, which lives for one fill and is bounded by
// ListTimeout.
//
// Mutations are never cached. The decorator promotes them from the embedded
// ForgeOps untouched, so a merge, a close or a create always reaches the forge.

package forges

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/keyenc"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"
)

// repoListTTL bounds how stale an account's repository listing may get. The set
// changes when someone clones, creates or archives a repository, which is rare
// against the rate this endpoint is read, and a forge connection change clears
// the cache outright (Manager.Invalidate) rather than waiting for the TTL.
const repoListTTL = 5 * time.Minute

// prListTTL is deliberately much shorter than repoListTTL, and CI is the reason
// rather than the PR set. A row carries the folded check verdict of its head
// commit, so a stale entry can show a green chip for a commit whose checks have
// since gone red. A merge cannot land the wrong COMMIT — MergeOptions.HeadSHA
// pins it and the forge refuses when the branch moved — but nothing here stops
// a merge of the right commit against a verdict that has changed, so the window
// is kept to a minute and the UI's refresh control bypasses the cache entirely.
const prListTTL = time.Minute

// maxListFills caps how many listing subprocesses may run at once, across both
// caches and every forge. It exists to bound the SPIKE: a cold PRs tab asks for
// one listing per cloned repository simultaneously, and this workspace has
// dozens, so without a ceiling each visit forks that many `gh` processes at the
// same instant and fires the same number of parallel API calls, which is what
// upstream secondary rate limiting is for.
//
// 16 is a spike ceiling rather than a tuned value. It is set high enough that a
// cold visit still completes in a handful of waves, because the client abandons
// the fan-out after 20 seconds and a bound that serialises the cold path would
// trade a slow first visit for a failed one.
const maxListFills = 16

// listEntry is one cached listing.
type listEntry[T any] struct {
	// at is the zero time until the first successful fill, which is what
	// separates "nothing to serve" from "something stale to serve".
	at   time.Time
	val  T
	busy bool // a revalidation is in flight; do not start a second
}

// listCache caches one KIND of listing, keyed by whatever identifies a single
// listing of that kind. The type parameter belongs on the type rather than on
// get: an instance holds entries of exactly one T, and a per-method parameter
// would let a []Repo and a []PR share one map.
type listCache[T any] struct {
	entries map[string]*listEntry[T]
	sf      singleflight.Group
	fills   *semaphore.Weighted
	ttl     time.Duration
	// gen is bumped by clear(). A fill that started before an invalidation
	// still RETURNS its value to the caller who asked for it, but does not
	// store it: it may have read the forge before the change that invalidated
	// the cache, and storing it would strand that change until the TTL.
	gen int
	mu  sync.Mutex
}

func newListCache[T any](ttl time.Duration, fills *semaphore.Weighted) *listCache[T] {
	return &listCache[T]{
		entries: make(map[string]*listEntry[T]),
		fills:   fills,
		ttl:     ttl,
	}
}

// get serves key, filling through fill only when it must.
//
// Four cases, in the order they are tested:
//
//   - force: fill now and replace. What the UI's refresh control asks for.
//   - nothing cached: fill now. A cold entry has nothing to serve.
//   - fresh: return the cached value, touching no forge.
//   - stale: return the cached value IMMEDIATELY and revalidate behind the
//     caller, so a visit past the TTL paints at once rather than waiting on a
//     subprocess.
func (c *listCache[T]) get(ctx context.Context, key string, force bool,
	fill func(context.Context) (T, error),
) (T, error) {
	if !force {
		if cached, ok := c.serve(ctx, key, fill); ok {
			return cached, nil
		}
	}
	return c.fillNow(ctx, key, fill)
}

// serve answers from the cache when it can, reporting whether it did. A stale
// hit also arms the revalidation, which is why this holds the lock across both
// decisions rather than reading state and acting on it afterwards.
func (c *listCache[T]) serve(ctx context.Context, key string,
	fill func(context.Context) (T, error),
) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || e.at.IsZero() {
		var zero T
		return zero, false
	}
	if time.Since(e.at) >= c.ttl && !e.busy {
		e.busy = true
		go c.revalidate(ctx, key, c.gen, fill)
	}
	return e.val, true
}

// fillNow fetches key and caches the result. Concurrent callers for one key
// share a single fetch: two browser tabs opening the PRs tab together, or a
// reload landing on top of the tab-activation fetch, would otherwise each fork
// their own subprocess per repository.
func (c *listCache[T]) fillNow(ctx context.Context, key string,
	fill func(context.Context) (T, error),
) (T, error) {
	c.mu.Lock()
	gen := c.gen
	c.mu.Unlock()

	ch := c.sf.DoChan(key, func() (any, error) {
		if err := c.fills.Acquire(ctx, 1); err != nil {
			return nil, err
		}
		defer c.fills.Release(1)
		val, err := fill(ctx)
		if err != nil {
			return nil, err
		}
		c.store(key, gen, val)
		return val, nil
	})

	var zero T
	select {
	case res := <-ch:
		if res.Err != nil {
			return zero, res.Err
		}
		val, _ := res.Val.(T)
		return val, nil
	case <-ctx.Done():
		// The shared fetch runs on until it settles, so a caller giving up does
		// not discard the work for whoever else is waiting on it.
		return zero, ctx.Err()
	}
}

// revalidate refreshes an entry whose stale value has already been served.
//
// ctx belongs to the request that noticed the staleness and dies when its
// response is written, so the fetch gets a detached one; the timeout is what
// stops this goroutine. A failure is not reported anywhere: the caller already
// has an answer, and the next request retries.
func (c *listCache[T]) revalidate(ctx context.Context, key string, gen int,
	fill func(context.Context) (T, error),
) {
	defer c.clearBusy(key)
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), ListTimeout)
	defer cancel()
	if err := c.fills.Acquire(bg, 1); err != nil {
		return
	}
	defer c.fills.Release(1)
	val, err := fill(bg)
	if err != nil {
		slog.Debug("forges: listing revalidation failed", "key", key, "error", err)
		return
	}
	c.store(key, gen, val)
}

// store records a successful fill, unless the cache was invalidated while it
// was in flight (see listCache.gen).
func (c *listCache[T]) store(key string, gen int, val T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	e, ok := c.entries[key]
	if !ok {
		e = &listEntry[T]{}
		c.entries[key] = e
	}
	e.val = val
	e.at = time.Now()
}

func (c *listCache[T]) clearBusy(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		e.busy = false
	}
}

// clear drops every entry and invalidates the fills already in flight.
func (c *listCache[T]) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*listEntry[T])
	c.gen++
}

// listCaches is the Manager's pair of listing caches. They share one fill
// semaphore because the thing being bounded is concurrent subprocesses, which
// both kinds of listing spawn.
type listCaches struct {
	repos *listCache[[]Repo]
	prs   *listCache[[]PR]
}

func newListCaches() *listCaches {
	fills := semaphore.NewWeighted(maxListFills)
	return &listCaches{
		repos: newListCache[[]Repo](repoListTTL, fills),
		prs:   newListCache[[]PR](prListTTL, fills),
	}
}

func (c *listCaches) clear() {
	c.repos.clear()
	c.prs.clear()
}

// cachedListings decorates a ForgeOps so its two listing calls read through the
// cache. Every other method is promoted from the embedded interface unchanged.
type cachedListings struct {
	ForgeOps
	caches  *listCaches
	forgeID string
	// force makes both listings bypass the cache and replace what is there.
	force bool
}

func (c cachedListings) ListRepos(ctx context.Context) ([]Repo, error) {
	return c.caches.repos.get(ctx, c.forgeID, c.force, c.ForgeOps.ListRepos)
}

func (c cachedListings) ListPRs(ctx context.Context, repo string, state ListState) ([]PR, error) {
	// keyenc rather than a separator: `state` arrives as a raw query parameter
	// and a repository name is upstream text, so a hand-joined key is forgeable
	// by whichever field carries the separator.
	key := keyenc.Join(c.forgeID, repo, string(state))
	return c.caches.prs.get(ctx, key, c.force, func(ctx context.Context) ([]PR, error) {
		return c.ForgeOps.ListPRs(ctx, repo, state)
	})
}
