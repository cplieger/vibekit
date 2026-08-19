// Package server — the canonical-request-path gate over the API surface.
//
// http.ServeMux canonicalizes a request path BEFORE it selects a pattern and
// answers 307 with a Location when the cleaned path differs. No registered
// pattern can intercept that, because the cleaning runs first. For a browser
// the redirect is invisible and correct. For the two machine senders vibekit
// actually documents it is neither, because 307 is a SUCCESS status to a client
// that does not follow redirects:
//
//   - the operator repair call in the README —
//     `curl -X POST localhost:9847/api/kiro-cli/rescan`, no -L. The rescan
//     never runs, curl exits 0, and the install the operator just repaired by
//     hand stays unpublished.
//   - the image's baked readiness probe —
//     `curl -sf http://127.0.0.1:9847/api/health`, no -L. The probe never
//     reaches the handler, so `docker ps` reads healthy while the kiro-cli
//     verdict this endpoint exists to publish (installing / retrying /
//     unavailable / settings-not-enforced) is never consulted.
//
// Both failures are silent in both directions: nothing says the URL was
// malformed, and the caller's own exit status says success. So the API surface
// refuses a non-canonical spelling itself rather than letting a redirect answer
// for it.
package server

import (
	"net/http"
	"strings"

	"github.com/cplieger/vibekit/internal/httpwire"
	"github.com/cplieger/webhttp"
)

// apiPathPrefix is the subtree this guard covers, and it is vibekit's whole
// HTTP surface bar one mount: every route registered in ListenAndServe and in
// every RegisterRoutes under internal/{hub,chat,git,filebrowse,auth,mcp,
// forges,push} sits under /api/, and the single exception is the "/" catch-all
// that serves the SPA and the embedded static tree.
//
// That mount is deliberately NOT guarded. It is browser-facing, and there a
// cleaned-path redirect is both legitimate and wanted: a browser follows it,
// gets the asset, and normalises dot segments before sending anyway. Refusing
// //app.js or /./app.js there would trade a working request for a 400 to
// protect a caller that does not exist on that mount — ServeMux's cleaning
// redirect and the SPA's index fallback keep answering exactly as before.
const apiPathPrefix = "/api/"

// msgNonCanonicalPath is the refusal, in vibekit's bare {"error": …} taxonomy.
// It names the CLASS and not the path: the cleaned spelling is derived from
// caller-controlled bytes, and the access line already carries the path (capped
// by webhttp), the status, the request id and client_ip for correlation.
const msgNonCanonicalPath = "non-canonical request path"

// canonicalAPIPath refuses a request whose path is not the path http.ServeMux
// would route it as, when that request addresses — or once cleaned would land
// on — the API surface.
//
// # Status and body
//
// 400 with vibekit's bare error envelope (httpwire.BadRequest). The status is the
// load-bearing half: >= 400 is what makes `curl -f` / `curl -sf` exit non-zero,
// which is the entire point — the failure has to become visible to the
// non-following clients above, and any 2xx or 3xx leaves them reporting
// success. 400 rather than 404 because the resource is not what is wrong: the
// request target is, and a 404 would send an operator looking for a route that
// is registered and healthy.
//
// # Which value is fed to the library
//
// The DECODED r.URL.Path, not r.URL.EscapedPath(). EscapedPath is what
// ServeMux itself cleans, so it would reproduce the redirect verdict exactly —
// and that is precisely why it is not enough here. Measured on this toolchain
// (go1.26.5), the encoded spelling of the same mistake does not redirect at
// all: GET /api/%2e%2e/api/health is already canonical in escaped form, so
// ServeMux draws no 307, no pattern matches the decoded /../api/health, and the
// request falls through to the "/" catch-all and is answered 200 with
// index.html. For `curl -sf` that is strictly worse than the 307 it replaces —
// a 200 with an HTML body and not even a Location to give the game away. Only
// the decoded path refuses both spellings of one intent, and it is also the
// path the sender believed it was addressing.
//
// The wider verdict was measured for false refusals before it was chosen, and
// it costs nothing here: the extra refusals are exactly the paths with a
// segment that decodes to "." or ".." (or an empty segment), and no identifier
// vibekit's routes carry can be one. Every wildcard and subtree route
// (/api/chats/{id}, /api/runs/{id}, /api/hooks/{id}/…, /api/schedules/{id},
// /api/knowledge/{name}) stays canonical when decoded even with dots inside a
// segment — "my.base" and "c-1.." both clean to themselves — and a chat id, run
// id or knowledge name of exactly ".." is not something the client can produce.
// What it does newly refuse is a handler being handed ".." as an identifier,
// which is the shape you want stopped at the edge rather than trusted three
// packages in.
//
// # Why BOTH the raw and the cleaned path decide the scope
//
// The prefix test runs on the raw spelling OR the cleaned one, because either
// alone leaks, in opposite directions, and both leaks were measured:
//
//   - Cleaning can move a path INTO the API surface. /%2e%2e/api/health has a
//     raw path of /../api/health — not under /api/ — while its clean is plainly
//     /api/health. Testing the raw path alone lets it past into the same
//     false-200 described above.
//   - Cleaning can move a path OUT of it, because a trailing ".." pops the
//     segment before it. /api/health/.. and /api/knowledge/%2e%2e both clean to
//     /api (not under /api/ — the prefix carries its slash), so testing the
//     clean alone lets the first through to a 307 and the second through to the
//     DELETE handler carrying ".." as the base name.
//
// A request is on this surface if it either ADDRESSES /api/ or would LAND on
// /api/, so the guard asks both. The cost is that /api/../app.js — raw under
// /api/, clean on the static mount — is refused rather than redirected to
// /app.js. That is the right answer anyway: the sender addressed the API, no
// browser emits dot segments, and answering an API-addressed request with a
// redirect onto a static asset is the kind of quiet reinterpretation this guard
// exists to stop.
//
// # What it does not claim
//
// Only the cleaning class, matching webhttp.CanonicalRequestPath's own
// contract. ServeMux's OTHER redirect — /tree to /tree/ when only the subtree
// pattern is registered — depends on the route table rather than the spelling,
// so a pure function over the path cannot see it, and this guard neither
// catches nor disturbs it. It does not arise on today's API surface: every
// subtree route vibekit registers (/api/chats/, /api/mcp/, /api/forges/,
// /api/tools/) also registers its exact form, so there is no trailing-slash
// redirect to mislead anyone. A future subtree-only route would reopen this
// class for a non-following caller, and the fix there is to register both
// spellings, not to grow this guard into something route-table-aware.
//
// CONNECT needs no special case even though net/http exempts it from
// canonicalization: its target is authority-form, so r.URL.Path is empty,
// CanonicalRequestPath answers ("/", false), and "/" is outside the guarded
// prefix — the request passes through on the value itself rather than on a
// method branch that would have to be kept true.
func canonicalAPIPath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean, canonical := webhttp.CanonicalRequestPath(r.URL.Path)
		if !canonical && onAPISurface(r.URL.Path, clean) {
			httpwire.BadRequest(w, msgNonCanonicalPath)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// onAPISurface reports whether a request either addresses the API subtree or
// would land on it once canonicalized. See canonicalAPIPath's "Why BOTH the raw
// and the cleaned path decide the scope" for what each leg catches on its own.
func onAPISurface(raw, clean string) bool {
	return strings.HasPrefix(raw, apiPathPrefix) || strings.HasPrefix(clean, apiPathPrefix)
}
