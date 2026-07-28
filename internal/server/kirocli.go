package server

import (
	"log/slog"
	"net"
	"net/http"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/kirocli"
	"github.com/cplieger/webhttp"
)

// kiroRescanPath is the loopback kiro-cli repair hook: it makes an install
// repaired INSIDE the container observable without recreating the container,
// which is the half of the healing posture bounded retries cannot cover (a
// restored version directory arrives after the retries are exhausted).
const kiroRescanPath = "/api/kiro-cli/rescan"

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
	reason := kirocli.ReasonUnavailable
	if s.kiroReady != nil {
		if _, why := s.kiroReady(); why != "" {
			reason = why
		}
	}
	slog.Warn("kiro-cli rescan found no usable version", "reason", reason, "error", err)
	api.WriteJSONStatus(w, http.StatusServiceUnavailable, healthBody{
		Status: "unready",
		Reason: reason,
	})
}

// loopbackOnly admits only requests whose SOCKET PEER *and* Host header are both
// loopback. Forwarded headers are deliberately ignored — they are
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
		if !loopbackPeer(r.RemoteAddr) || !loopbackHost(r.Host) {
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

// loopbackPeer reports whether an http.Request.RemoteAddr belongs to a loopback
// socket peer. Forwarded headers play no part — RemoteAddr is set by the server
// from the accepted connection. Malformed values fail closed.
func loopbackPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// loopbackHost reports whether a request's Host header names the local host:
// "localhost" or a loopback IP literal, canonicalized by webhttp.CanonicalHost
// (so 127.0.0.1:9847, [::1]:9847 and localhost all match, and a malformed Host
// canonicalizes to "" and fails closed). Paired with loopbackPeer this is the
// both-ends test webhttp.WithLoopbackExempt applies to the ALLOWED_HOSTS
// carve-out: a DNS-rebound page carries the ATTACKER's name in Host even when
// its socket peer is loopback, so the peer check alone does not close CWE-346
// wherever the browser and the server share a loopback interface.
func loopbackHost(host string) bool {
	canon := webhttp.CanonicalHost(host)
	if canon == "localhost" {
		return true
	}
	ip := net.ParseIP(canon)
	return ip != nil && ip.IsLoopback()
}
