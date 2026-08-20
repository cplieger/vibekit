package agent

import "sync"

// utilityLease owns the lazily-built utility runtime and EVERY access to it.
//
// It exists because the field it replaced was guarded two different ways at once,
// which is a race the detector found the moment a delete began reaching it. The
// build ran inside a sync.Once with no lock, while three readers took the
// process-lifetime mutex to look at the same field — so a reader that never calls
// the builder (the orphan-session sweep asking for the live session id) had no
// synchronisation with the build at all. sync.Once orders its own callers and
// nobody else.
//
// The fix is structural rather than another lock at the call sites: there is no
// exported field, so the only way to reach the runtime is through a method that
// takes the mutex. One owner, one lock, no way to get it wrong from outside.
//
// It also unpicks a mutex that guarded two unrelated invariants. The lifetime
// mutex covered both this slot and a buffer delete during chat teardown; with the
// slot self-guarding, that mutex is back to one job.
type utilityLease struct {
	// build constructs the runtime. Supplied by the Runtime rather than closed
	// over here because the hooks it injects point back into runtime services —
	// a bidirectional edge that must stay visible at the wiring site.
	build func() *utilityRuntime
	rt    *utilityRuntime
	mu    sync.Mutex
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
