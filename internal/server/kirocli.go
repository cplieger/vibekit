package server

import (
	"log/slog"
	"net/http"

	"github.com/cplieger/pinstall/v2"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/webhttp"
)

// kiroRescanPath is the loopback kiro-cli repair hook: it makes an install
// repaired INSIDE the container observable without recreating the container,
// which is the half of the healing posture bounded retries cannot cover (a
// restored version directory arrives after the retries are exhausted).
const kiroRescanPath = "/api/kiro-cli/rescan"

// kiroRescanSurface is what a refused caller is told declined the request.
const kiroRescanSurface = "the kiro-cli repair hook"

// The readiness reasons vibekit puts on the wire, one per operator situation.
//
// The install manager reports a TYPED reason (pinstall.Reason) that names only
// the distinction — "installing", "unavailable" — because the wording a consumer
// shows its own users is the consumer's. These four literals ARE that wording,
// and they are a published contract in two directions: an operator reads them
// from `docker inspect` / a monitor, and static-src/runtime-health.ts
// PREFIX-MATCHES them (on "kiro-cli") to pick its banner copy, keyed on each
// literal in full. Renaming one here silently degrades that banner to its
// terminal fallback, so kiroReasonText is the single place they are produced and
// TestKiroReasonTextIsTheClientContract pins the exact strings.
const (
	reasonInstalling = "kiro-cli installing"
	reasonRetrying   = "kiro-cli install retrying"
	// reasonUnavailable is also the fallback for a rescan with no verdict to
	// read, and for a reason a future library version adds: a state we cannot
	// name still blocks chats, and the terminal wording says so.
	reasonUnavailable = "kiro-cli unavailable"
	// reasonSettings is pinstall.ReasonAssertion in vibekit's terms. The only
	// REQUIRED assertion here is the profile's mandatory app.disableAutoupdates
	// (every setting kiroSettings passes is best-effort), so a withheld verdict
	// means exactly that the binary may replace itself and invalidate the
	// verified digest.
	reasonSettings = "kiro-cli required settings not enforced"
)

// kiroReasonText renders the install manager's typed reason as the reason
// /api/health and the repair hook serve. ReasonReady maps to "", which the
// health envelope omits.
func kiroReasonText(why pinstall.Reason) string {
	switch why {
	case pinstall.ReasonReady:
		return ""
	case pinstall.ReasonInstalling:
		return reasonInstalling
	case pinstall.ReasonRetrying:
		return reasonRetrying
	case pinstall.ReasonUnavailable:
		return reasonUnavailable
	case pinstall.ReasonAssertion:
		return reasonSettings
	}
	return reasonUnavailable
}

// handleKiroRescan re-derives the active kiro-cli version from what is on disk
// right now — downloading nothing — and reports the resulting readiness. 200
// when a version is active afterwards, 503 with the manager's own reason when
// none is: the same verdict /api/health will serve from the next probe, so a
// caller gets its answer without polling.
func (s *Server) handleKiroRescan(w http.ResponseWriter, r *http.Request) {
	// A rescan probes the candidate binary and reasserts the required settings,
	// so it is not free; it is also not cacheable under any circumstances.
	w.Header().Set("Cache-Control", "no-store")
	ok, err := s.kiroRescan(r.Context())
	if ok {
		httpreply.WriteJSON(w, healthBody{Status: "ok"})
		return
	}
	// The manager has already logged the specific fault (and every path it took)
	// at Warn or Error, so this reports the VERDICT rather than the error text:
	// err can name a filesystem path, and this response is not the place to
	// widen what a caller learns about the volume.
	reason := reasonUnavailable
	if s.kiroReady != nil {
		if _, why := s.kiroReady(); why != pinstall.ReasonReady {
			reason = kiroReasonText(why)
		}
	}
	slog.Warn("kiro-cli rescan found no usable version", "reason", reason, "error", err)
	httpreply.WriteJSONStatus(w, http.StatusServiceUnavailable, healthBody{
		Status: "unready",
		Reason: reason,
	})
}

// loopbackOnly admits only requests whose SOCKET PEER and Host header are both
// loopback AND which carry no proxy or browser provenance header, via
// webhttp.LoopbackOnly.
//
// The gate matters because a rescan spawns bounded kiro-cli subprocesses (a
// version probe plus the settings reassertion): leaving it reachable from the LAN
// would hand an unauthenticated caller a process-spawn lever.
//
// The provenance deny is NEW here and is why this now uses the middleware rather
// than the bare webhttp.LoopbackRequest predicate. The two legs alone are not
// sufficient: a reverse proxy sharing this server's loopback interface (host
// networking, a shared network namespace) rewrites Host to its upstream address
// by default in both nginx and Apache, which satisfies the Host leg while the
// proxy itself satisfies the peer leg — so a remote request passed both checks
// and reached the process-spawn lever. web-terminal-kiro had closed that with its
// own seven-header deny while this copy had not; the decision now lives once, in
// webhttp.
//
// The refusal stays local: the repo's canonical error envelope, not the health
// envelope the success and unready paths use, because this is a rejected request
// rather than a readiness verdict and a caller keying on {"status":...} must not
// read a 403 as one.
//
// surface NAMES the endpoint that refused, and it is a parameter rather than a
// constant because there are two mounts behind this gate now (the repair hook and
// /debug/pprof/) and web-terminal-kiro's copy of this wrapper already takes one.
// A refused profile request telling the caller the repair hook declined it is a
// wrong answer with the right status: the operator retries the wrong path.
func loopbackOnly(surface string, next http.Handler) http.Handler {
	refuse := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The request is USED, not discarded: neither endpoint spawns a process
		// on this path but both gate one, so a refusal is worth a correlatable
		// record. Without this the 403 relied entirely on the access log, unlike
		// the sibling app's loopback refusal, which passes the request into its
		// writer.
		slog.Warn("loopback-only endpoint refused: not a loopback caller",
			"surface", surface, "remote", r.RemoteAddr, "host", r.Host)
		httpreply.WriteJSONStatus(w, http.StatusForbidden,
			httpreply.ErrorJSON(surface+" is loopback-only; call it from inside the container"))
	})
	return webhttp.LoopbackOnly(refuse)(next)
}
