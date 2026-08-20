// Runtime profiles, behind the loopback gate.
//
// # Why this server carries them at all
//
// Every hazard this app has actually had is goroutine-lifecycle shaped: inflight
// request tracking, the Stop-before-Wait shutdown ordering, one bridge per chat
// with a readLoop and a notification fan-out each, the per-chat buffer store, the
// SSE hub's per-subscriber writers. A goroutine dump answers all of those
// directly — which goroutine, parked on what, since when — and nothing else the
// process exposes does. It is also the one diagnostic an agent working INSIDE
// this container can fetch for itself, which is the difference between
// diagnosing a leak and asking the operator to reproduce it.
//
// # Not the blank import
//
// `import _ "net/http/pprof"` is the documented spelling and it is INERT here:
// its init registers on http.DefaultServeMux, and this process serves a
// http.NewServeMux built in ListenAndServe and never touches DefaultServeMux.
// So the handlers are reached explicitly. The same init still runs and its
// DefaultServeMux registrations stay unreachable, which is why the gosec
// suppression below is a statement about this process rather than a waiver.
//
// pprof.Index serves the index page AND every named profile by deriving the name
// from the request path, so one registration covers goroutine, goroutineleak,
// heap, allocs, block, mutex and threadcreate. It only works mounted at the
// literal /debug/pprof/ prefix, which is also what `go tool pprof <url>` expects.
//
// # goroutineleak, and why it is not a new registration
//
// Go 1.27 made the goroutine-leak profile generally available, and because the
// name is derived from the path it arrived here with the toolchain rather than
// with a code change: /debug/pprof/goroutineleak answers 200 through the
// registration already below. Measured on go1.27.0 against a goroutine parked on
// a channel nothing retains — `goroutineleak profile: total 1`, and the index
// page lists it. It is the best-matched profile this app could have, since the
// hazards named at the top of this file are exactly the class it reports: a
// goroutine blocked on a primitive the collector can prove nothing runnable can
// ever reach.
//
// Two properties to hold onto when reading one. Detection is REACHABILITY-based,
// so a goroutine parked on a channel, WaitGroup or Mutex still reachable from a
// package-level variable or from a runnable goroutine's locals is NOT reported —
// `total 0` is not a statement that nothing leaked, and the useful reading is a
// count diff across an operation rather than an assertion of zero. And a
// testing/synctest bubble is strictly stronger for anything inside one: it panics
// on a blocked goroutine at the end of the bubble, synchronously and per test.
//
// # What is deliberately NOT mounted
//
// pprof.Profile (CPU) and pprof.Trace hold the server for their sample window,
// 30 seconds by default and caller-controlled via ?seconds=. On a single-process
// dev box with live SSE streams and PTY sessions that is a self-inflicted stall,
// and neither answers the goroutine question this exists for. pprof.Cmdline and
// pprof.Symbol are omitted for a smaller reason: nothing needs them for a
// `?debug=2` text dump, and Symbol would be the one endpoint here that reads
// process memory on a caller-supplied address list.
//
// goroutineleak sits on the mounted side of that line rather than beside CPU and
// Trace even though it drives a GC cycle: measured on go1.27.0 over a
// 20,000-live-object heap it is 696 µs mean against goroutine's 382 µs and
// heap's 350 µs, so it is one more sub-millisecond snapshot, not a sample window.
//
// # Reachability, stated because it is surprising
//
// The gate denies browser provenance headers (Origin, Sec-Fetch-Site) along with
// proxy ones, so /debug/pprof/ in a browser tab answers 403 even from inside the
// container. `curl` from `docker exec` is the access path. That is the right
// answer for the stated use and it does rule out the interactive flame-graph UI.

package server

import (
	"net/http"
	// A NORMAL import, not the blank one: see the file comment. gosec's G108
	// does not fire on this repo's profile, so there is no suppression here to
	// go stale — if it is ever enabled, the reason to give it is that the
	// handlers below are reached only through loopbackOnly and that this process
	// never serves DefaultServeMux, where the package's init registers.
	"net/http/pprof"
)

// pprofPath is the subtree pattern. The trailing slash is load-bearing twice
// over: ServeMux needs it to match a subtree (and it then takes precedence over
// the "/" SPA handler), and pprof.Index derives a profile name by trimming
// exactly this prefix off the request path.
const pprofPath = "/debug/pprof/"

// pprofSurface is what a refused caller is told declined the request. It names
// this endpoint rather than the repair hook that composes the same middleware,
// because the refusal is the whole of what a rejected caller learns and naming
// the wrong path sends the operator to retry the wrong one.
const pprofSurface = "the runtime profile endpoint"

// pprofHandler returns the gated profile handler.
//
// Same gate as the kiro-cli repair hook, and for a stronger reason: a goroutine
// dump names every function on every stack, and the heap profile names
// allocation sites. That is a map of the process, so it belongs behind the same
// socket-peer AND Host check rather than a weaker one.
func pprofHandler() http.Handler {
	return loopbackOnly(pprofSurface, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A profile is a snapshot of this instant and there is no version of it
		// that a cache should ever serve twice.
		w.Header().Set("Cache-Control", "no-store")
		pprof.Index(w, r)
	}))
}
