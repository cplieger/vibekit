package server

import (
	"log/slog"
	"net/http"

	"github.com/cplieger/pinstall/v2"
	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/webhttp"
)

// kiroRescanPath is the loopback kiro-cli repair hook: it makes an install
// repaired INSIDE the container observable without recreating the container,
// which is the half of the healing posture bounded retries cannot cover (a
// restored version directory arrives after the retries are exhausted).
const kiroRescanPath = "/api/kiro-cli/rescan"

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
		api.WriteJSON(w, healthBody{Status: "ok"})
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
	api.WriteJSONStatus(w, http.StatusServiceUnavailable, healthBody{
		Status: "unready",
		Reason: reason,
	})
}

// loopbackOnly admits only requests whose SOCKET PEER *and* Host header are both
// loopback, via webhttp.LoopbackRequest — the shared two-legged conjunction,
// which reads only RemoteAddr and Host and fails closed when either is
// unparseable. Forwarded headers are deliberately ignored — they are
// client-controlled, and this gate is the repair hook's only boundary on an
// otherwise-unauthenticated port. The intended caller is an operator or an agent
// INSIDE the container (`curl -X POST localhost:9847/api/kiro-cli/rescan`);
// everything routed in from outside is refused, as is a DNS-rebound page whose
// loopback socket peer carries an attacker Host.
//
// The gate matters because a rescan spawns bounded kiro-cli subprocesses (a
// version probe plus the settings reassertion): leaving it reachable from the
// LAN would hand an unauthenticated caller a process-spawn lever.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !webhttp.LoopbackRequest(r) {
			// The repo's canonical error envelope, not the health envelope the
			// success and unready paths use: this is a rejected request, not a
			// readiness verdict, and a caller keying on {"status":...} must not
			// read a 403 as one.
			api.WriteJSONStatus(w, http.StatusForbidden,
				api.ErrorJSON("the kiro-cli repair hook is loopback-only; call it from inside the container"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
