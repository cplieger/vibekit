// Official registry proxy.
//
// Queries registry.modelcontextprotocol.io on behalf of the browser,
// normalising the response to a compact shape the UI can render without
// knowing the full upstream schema. Primary reason for proxying (vs
// direct fetch from the browser) is CORS, secondarily we cache results
// briefly to shield the preview-stability registry from a burst of
// identical queries when the user types.
//
// The upstream v0.1 API shape:
//
//	GET /v0.1/servers?search=<q>&limit=<n>
//	{ "servers": [{ "server": {...}, "_meta": {...} }], "metadata": {...} }
//
// Each server has one "version" and carries either `packages[]` (stdio
// via npm/docker/etc) or `remotes[]` (http/sse), with declared
// `environmentVariables` / `headers` telling the user what secrets are
// needed.

package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"vibekit/internal/api"
)

// Compile-time interface assertion.
var _ api.RouteHandler = (*RegistryProxy)(nil)

const (
	registryBaseURL  = "https://registry.modelcontextprotocol.io/v0.1"
	registryHost     = "registry.modelcontextprotocol.io"
	registryTimeout  = 10 * time.Second
	registryCacheTTL = 60 * time.Second
	// Hard cap on the server-side proxy so a browser can't DOS upstream.
	maxSearchLimit = 25
	// Upper bound on the `q` string. Real search queries are a handful
	// of words; anything longer is a cache-fill DoS attempt.
	maxSearchQueryLen = 128
	// Upper bound on distinct cached entries. Each entry is the raw
	// upstream body (up to 2 MiB). With this cap the cache holds at
	// most maxCacheEntries × 2 MiB = 128 MiB in the worst case. Oldest
	// entries get evicted on insert when full.
	maxCacheEntries = 64
	// Max upstream response body we'll read + cache. 2 MiB comfortably
	// covers the full registry at maxSearchLimit=25; the +1 sentinel
	// in fetchSearch turns an at-cap read into an explicit error
	// instead of a silently-truncated JSON that would parse as empty.
	maxRegistryBody = 2 * 1024 * 1024
	// drainLimit caps how many bytes of an error-response body we
	// read for connection reuse. Large enough for typical upstream
	// error envelopes, small enough that a hostile upstream can't tie
	// us up on the drain.
	drainLimit = 4 * 1024
)

// RegistryProxy fetches + caches + shapes upstream registry responses.
type RegistryProxy struct {
	client *http.Client
	cache  *registryCache
}

type registryCacheEntry struct {
	insertedAt time.Time
	body       []byte
}

// NewRegistryProxy returns a ready-to-use proxy with sensible timeouts
// and a redirect allowlist. The default http.Client follows up to 10
// redirects, which would let a compromised or moved upstream bounce
// the proxy to 169.254.169.254, 127.0.0.1, or a LAN service. We
// restrict redirects to the registry host itself and cap at 3 hops.
func NewRegistryProxy() *RegistryProxy {
	return &RegistryProxy{
		client: &http.Client{
			Timeout: registryTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if req.URL.Host != registryHost {
					return fmt.Errorf("refusing redirect to non-registry host %q",
						req.URL.Host)
				}
				if len(via) >= 3 {
					return errors.New("too many redirects")
				}
				return nil
			},
		},
		cache: newRegistryCache(maxCacheEntries),
	}
}

// RegisterRoutes wires /api/mcp/registry/search.
//
//	GET /api/mcp/registry/search?q=<query>&limit=<n>
func (p *RegistryProxy) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/mcp/registry/search", p.handleSearch)
}

func (p *RegistryProxy) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > maxSearchQueryLen {
		slog.Debug("mcp: registry search query too long",
			"len", len(q), "cap", maxSearchQueryLen)
		api.BadRequest(w, "query too long")
		return
	}
	// Control characters can't appear in real search queries; they
	// trigger upstream 400s (masked as 502 "registry unavailable")
	// and muddy the cache key. Reject up-front so the user sees a
	// concrete 400 instead of a generic proxy failure.
	for _, c := range q {
		if c < 0x20 || c == 0x7f {
			slog.Debug("mcp: registry search query has control chars",
				"len", len(q))
			api.BadRequest(w, "query contains control characters")
			return
		}
	}
	limit := 20
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			slog.Debug("mcp: registry search bad limit",
				"limit", s, "error", err)
			api.BadRequest(w, "invalid limit")
			return
		}
		limit = n
	}
	if limit < 1 {
		limit = 1
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	body, cached, err := p.fetchSearch(r.Context(), q, limit)
	if err != nil {
		// Client walked away mid-fetch (tab close, navigation); no
		// need to log a 502 or write a response. Keeping the Debug
		// line so Loki still has a breadcrumb for "query was in
		// flight when the client disconnected".
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			slog.Debug("mcp: registry search cancelled",
				"q", q, "limit", limit, "error", err)
			return
		}
		slog.Warn("mcp: registry search failed",
			"q", q, "limit", limit, "error", err)
		// Return a generic sentinel to the browser; full detail is in
		// the slog.Warn above. The error text can leak upstream
		// operational signals ("refusing redirect to non-registry host
		// 169.254.169.254") that the browser has no need to see.
		api.WriteJSONStatus(w, http.StatusBadGateway, map[string]string{
			"error": "registry unavailable",
		})
		return
	}
	normalised := normaliseRegistryResponse(body)
	slog.Debug("mcp: registry search",
		"q", q, "limit", limit, "results", len(normalised), "cached", cached)
	api.WriteJSON(w, map[string]any{"servers": normalised})
}

// fetchSearch returns raw upstream response bytes (from cache when
// fresh). The second return value reports whether the result came from
// the cache. Concurrent identical misses coalesce to a single upstream
// request via singleflight, so N browser tabs typing the same query
// no longer hold N outbound goroutines waiting on the same 10-second
// HTTP call.
//
// The leader runs its upstream fetch under a detached background
// context so the shared result isn't starved when the originating
// caller walks away. Follower goroutines still respect their own
// context cancellation.
func (p *RegistryProxy) fetchSearch(ctx context.Context, q string, limit int) (body []byte, cached bool, err error) {
	key := fmt.Sprintf("%s|%d", q, limit)

	return p.cache.GetOrFetch(ctx, key, func() ([]byte, error) {
		// Detach: the upstream fetch is cheap (10s cap) and shared,
		// so losing the originating caller's ctx shouldn't kill the
		// shared work.
		fetchCtx, fetchCancel := context.WithTimeout(context.Background(), registryTimeout)
		defer fetchCancel()
		return p.doFetch(fetchCtx, q, limit)
	})
}

// doFetch issues the upstream GET. Factored out of fetchSearch so the
// in-flight barrier code stays focused on coordination.
func (p *RegistryProxy) doFetch(ctx context.Context, q string, limit int) ([]byte, error) {
	u, err := url.Parse(registryBaseURL + "/servers")
	if err != nil {
		return nil, err
	}
	v := u.Query()
	if q != "" {
		v.Set("search", q)
	}
	v.Set("limit", strconv.Itoa(limit))
	u.RawQuery = v.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "vibekit/1.0")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Drain a bounded tail of the body so net/http can reuse the
		// keep-alive connection for the next fetch. Without this,
		// every non-200 forces a fresh TLS handshake on the follow-up
		// query — under an upstream incident with retries, the cost
		// compounds.
		drainRegistryBody(resp.Body)
		return nil, fmt.Errorf("upstream returned %d", resp.StatusCode)
	}
	// Pre-check upstream-declared size so we fail fast on oversize
	// responses instead of reading up to the cap and then silently
	// truncating (which would parse as empty and surface as "no
	// results matching your query" — a lie indistinguishable from a
	// real empty result).
	if resp.ContentLength > maxRegistryBody {
		drainRegistryBody(resp.Body)
		return nil, fmt.Errorf("upstream body too large: %d > %d",
			resp.ContentLength, maxRegistryBody)
	}
	// Read up to maxRegistryBody+1 so an undeclared-length response
	// that runs past the cap reads one extra byte we can detect.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRegistryBody+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxRegistryBody {
		return nil, fmt.Errorf("upstream body exceeded %d byte cap",
			maxRegistryBody)
	}
	return body, nil
}

// drainRegistryBody discards a bounded tail of r so net/http can
// reuse the keep-alive TLS connection. Errors are logged at Debug
// (expected when the remote closes immediately) so errcheck is
// satisfied without polluting ops logs.
func drainRegistryBody(r io.Reader) {
	if _, err := io.Copy(io.Discard, io.LimitReader(r, drainLimit)); err != nil {
		slog.Debug("mcp: registry drain stopped", "error", err)
	}
}
