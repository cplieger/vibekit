package server

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp"
)

// accountUsageTTL bounds how often the footer fetch hits KAS. Account
// usage changes slowly and the upstream GetUsageLimits call may be
// rate-limited, so we serve a cached snapshot for this window and only
// refetch (lazily, on footer open) once it expires.
const accountUsageTTL = 60 * time.Second

// acctUsageCache is the server-side short-TTL cache + last-known snapshot
// for GET /api/account/usage. The last-known snapshot is served stale when
// a refresh fails (no live bridge, rate limit) so the footer degrades
// gracefully instead of blanking.
type acctUsageCache struct {
	data    *vibekit.AccountUsage
	atNanos int64 // wall-clock UnixNano of the last successful fetch
	mu      sync.Mutex
}

// handleAccountUsage serves account/subscription usage for the sidebar
// footer. Cached for accountUsageTTL; on a fetch failure it serves the
// last-known snapshot (marked stale) if any, else 503.
func (s *Server) handleAccountUsage(w http.ResponseWriter, r *http.Request) {
	// Gated here, not on the ServeMux pattern: a method-pattern mismatch falls
	// through to the SPA mount and answers 200 with index.html. See
	// server.go's ListenAndServe.
	if !httpreply.RequireMethod(w, r, http.MethodGet) {
		return
	}
	if s.accountUsage == nil {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON("account usage unavailable"))
		return
	}
	c := &s.acctUsage

	c.mu.Lock()
	if c.data != nil && time.Since(time.Unix(0, c.atNanos)) < accountUsageTTL {
		fresh := *c.data
		c.mu.Unlock()
		fresh.Stale = false
		webhttp.WriteJSON(w, fresh)
		return
	}
	c.mu.Unlock()

	usage, err := s.accountUsage.AccountUsage(r.Context())
	if err != nil {
		c.mu.Lock()
		last := c.data
		c.mu.Unlock()
		if last != nil {
			stale := *last
			stale.Stale = true
			webhttp.WriteJSON(w, stale)
			return
		}
		slog.Warn("account usage fetch failed", "error", err)
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON("account usage unavailable"))
		return
	}

	c.mu.Lock()
	c.data = usage
	c.atNanos = time.Now().UnixNano()
	c.mu.Unlock()
	webhttp.WriteJSON(w, usage)
}
