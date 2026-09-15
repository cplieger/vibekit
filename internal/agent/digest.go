package agent

import (
	"context"
	"strconv"
	"time"

	"github.com/cplieger/sse"
	"github.com/cplieger/vibekit/internal/subject"
	"github.com/cplieger/vibekit/internal/vibekit"
)

const (
	// digestConcurrency bounds resolutions in flight. The client is single-flight
	// per tab, so four slots serve four devices waking at once without queueing
	// and a flood degrades to waiting rather than to lock contention on the
	// streaming writers.
	digestConcurrency = 4
	// digestTimeout is the RouteTimeout on POST /api/sync, so a request parked on
	// a lock cannot hold its slot for as long as the peer stays connected.
	digestTimeout = 10 * time.Second
)

// resolveDigest is the hub's Resolver: one State per Held, in any order, from the
// registry the writers mint into. It takes NO per-chat store mutex — Mutate holds
// that across an fsynced file rewrite — and NO lock a writer holds across I/O; each
// answer is a few uncontended mutex reads.
func (rt *Runtime) resolveDigest(ctx context.Context, held []sse.Held) ([]sse.State, error) {
	if err := rt.digestSlots.acquire(ctx); err != nil {
		return nil, err
	}
	defer rt.digestSlots.release()
	if rt.digestHook != nil {
		rt.digestHook()
	}
	out := make([]sse.State, 0, len(held))
	for i := range held {
		out = append(out, rt.resolveOne(&held[i]))
	}
	return out, nil
}

// resolveOne answers one subject. A kind this server does not serve is gone, so
// the client forgets it rather than asking again on every digest.
func (rt *Runtime) resolveOne(h *sse.Held) sse.State {
	st := sse.State{Subject: h.Subject}
	switch subject.Kind(h.Kind) {
	case subject.KindChat:
		if !rt.chatStore.Exists(vibekit.ChatID(h.Ref)) {
			st.Status = sse.StatusGone
			return st
		}
		st.Version, _ = rt.versions.Current(subject.KindChat, h.Ref)
	case subject.KindChats, subject.KindPending, subject.KindRuns, subject.KindCatalog, subject.KindStatus:
		st.Version, _ = rt.versions.Current(subject.Kind(h.Kind), h.Ref)
	case subject.KindLiveTurn:
		// The turn registry, never Buffer.Started or MessageID: a SplitSegment
		// leaves MessageID empty on a turn that is still live.
		facts, open := rt.coord.turns.openTurnFor(vibekit.ChatID(h.Ref))
		if !open || facts.Buf == nil {
			st.Status = sse.StatusGone
			return st
		}
		st.Version = facts.Buf.Version()
	case subject.KindTabs:
		var version uint64
		if rt.tabs != nil {
			_, version = rt.tabs.List()
		}
		st.Version = strconv.FormatUint(version, 10)
	default:
		st.Status = sse.StatusGone
	}
	return st
}

// digestSemaphore is a context-aware counting semaphore: a slot is acquired with
// the request context, so a waiter whose request expires fails with ctx.Err()
// instead of queueing behind the resolutions that filled the slots.
type digestSemaphore chan struct{}

func newDigestSemaphore(n int) digestSemaphore { return make(chan struct{}, n) }

func (s digestSemaphore) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s digestSemaphore) release() { <-s }
