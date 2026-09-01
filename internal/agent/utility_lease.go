package agent

import "sync"

// utilityLease owns the lazily-built utility runtime and EVERY access to it.
//
// It replaced a field guarded two different ways at once: a sync.Once build with
// no lock, plus three readers taking a shared lifetime mutex — so a reader that
// never calls the builder (the orphan-session sweep) had no synchronisation with
// the build at all. There is no exported field, so the only way to reach the
// runtime is through a method that takes the mutex — one owner, one lock.
type utilityLease struct {
	// build constructs the runtime. Supplied by the Runtime rather than closed
	// over here because the hooks it injects point back into runtime services —
	// a bidirectional edge that must stay visible at the wiring site.
	build func() *utilityRuntime
	// rt is nil until first use — the whole point of the lease. Optional at
	// construction by design, not by omission.
	rt *utilityRuntime `wiring:"optional"`
	mu sync.Mutex
}

// get returns the utility runtime, building it on first use.
//
// Replaces sync.Once. The Once bought lazy-exactly-once and nothing else, and a
// mutex around a nil check buys the same thing while also ordering the readers.
func (l *utilityLease) get() *utilityRuntime {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rt == nil {
		l.rt = l.build()
	}
	return l.rt
}

// peek returns the runtime only if it has already been built, and never builds
// one. For the callers that want to know about a live utility bridge without
// bringing one into existence to ask — the idle cull and the session sweep, both
// of which would otherwise spawn the very thing they are inspecting.
func (l *utilityLease) peek() *utilityRuntime {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rt
}

// take removes and returns the runtime, so the caller can stop it knowing
// nothing else holds it. The next get() builds a fresh one.
func (l *utilityLease) take() *utilityRuntime {
	l.mu.Lock()
	defer l.mu.Unlock()
	rt := l.rt
	l.rt = nil
	return rt
}
