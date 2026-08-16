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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/vibekit/internal/api"
	"golang.org/x/sync/singleflight"
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
		api.MethodNotAllowed(w, http.MethodGet)
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
	// Clamp into [1, maxSearchLimit] with the min/max builtins (Go 1.21+)
	// instead of hand-rolled if-guards: fewer branches, same result.
	limit = min(max(limit, 1), maxSearchLimit)

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
// The leader derives its timeout from the request-scoped context so
// upstream fetches respect client disconnection and server shutdown.
// Follower goroutines still respect their own context cancellation.
func (p *RegistryProxy) fetchSearch(ctx context.Context, q string, limit int) (body []byte, cached bool, err error) {
	key := searchCacheKey(q, limit)

	return p.cache.GetOrFetch(ctx, key, func() ([]byte, error) {
		fetchCtx, fetchCancel := context.WithTimeout(ctx, registryTimeout)
		defer fetchCancel()
		return p.doFetch(fetchCtx, q, limit)
	})
}

// searchCacheKey composes the upstream-response cache key over the search
// query and the clamped result limit.
//
// `q` is the ONLY field that can carry a separator: it is untrusted HTTP query
// input, filtered for control characters and length (maxSearchQueryLen) but
// not for ':' or '\'. `limit` is a clamped decimal in [1, maxSearchLimit] and
// can never contain either. The pre-keyenc "%s|%d" form was in fact still
// injective for exactly that reason — the one separator-bearing field sat
// FIRST and the trailing field was a digit run, so the final '|' always marked
// the boundary. That is an accident of field order, not a property of the key:
// appending a third component, or moving `limit` ahead of `q`, would have made
// two distinct searches share one cache entry, and neither edit looks like it
// touches key encoding. keyenc.Join escapes each component, so the encoding
// stays injective under any such edit.
//
// Consequence of a collision, concretely: one browser's search for a crafted
// query would be served the cached upstream body of a DIFFERENT query for up
// to registryCacheTTL, so the MCP add-server list offers servers that do not
// match what the user typed — and, since the entry is shared, poisons that
// result for every other client until it expires.
//
// The key's bytes changed (':' rather than '|', plus escaping when the query
// contains ':' or '\'). Nothing persists it: registryCache is an in-memory map
// with a 60s TTL, rebuilt from scratch on every process start.
func searchCacheKey(q string, limit int) string {
	return keyenc.Join(q, strconv.Itoa(limit))
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
	req.Header.Set("Accept", api.MIMETypeJSON)
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

// --- Cache ---

// registryCache is a TTL-bounded, singleflight-protected byte cache
// for upstream registry responses. It encapsulates the map, TTL,
// max-entries cap, and request coalescing that were previously inline
// on RegistryProxy.
type registryCache struct {
	sf      singleflight.Group
	entries map[string]registryCacheEntry
	ttl     time.Duration
	maxSize int
	mu      sync.Mutex
}

func newRegistryCache(maxSize int) *registryCache {
	return &registryCache{
		entries: make(map[string]registryCacheEntry),
		ttl:     registryCacheTTL,
		maxSize: maxSize,
	}
}

// GetOrFetch returns cached data for key if fresh, otherwise calls
// fetchFn (coalesced via singleflight) and caches the result.
// Followers bail early on ctx cancellation.
func (c *registryCache) GetOrFetch(ctx context.Context, key string, fetchFn func() ([]byte, error)) (body []byte, cached bool, err error) {
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && time.Since(entry.insertedAt) < c.ttl {
		body = entry.body
		c.mu.Unlock()
		return body, true, nil
	}
	c.mu.Unlock()

	// DoChan coalesces concurrent misses without a wrapper goroutine.
	ch := c.sf.DoChan(key, func() (any, error) {
		b, doErr := fetchFn()
		if doErr != nil {
			return nil, doErr
		}
		c.mu.Lock()
		if len(c.entries) >= c.maxSize {
			c.evictLocked()
		}
		c.entries[key] = registryCacheEntry{
			insertedAt: time.Now(),
			body:       b,
		}
		c.mu.Unlock()
		return b, nil
	})

	select {
	case res := <-ch:
		if res.Err != nil {
			return nil, false, res.Err
		}
		b, ok := res.Val.([]byte)
		if !ok {
			return nil, false, fmt.Errorf("registry_cache: fetcher returned %T, want []byte", res.Val)
		}
		return b, res.Shared, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

// evictLocked removes expired entries and, if still at capacity,
// evicts the oldest entry. Caller must hold c.mu.
func (c *registryCache) evictLocked() {
	evicted := 0
	for k, entry := range c.entries {
		if time.Since(entry.insertedAt) >= c.ttl {
			delete(c.entries, k)
			evicted++
		}
	}
	if len(c.entries) >= c.maxSize {
		var oldestKey string
		var oldestTime time.Time
		first := true
		for k, entry := range c.entries {
			if first || entry.insertedAt.Before(oldestTime) {
				oldestKey = k
				oldestTime = entry.insertedAt
				first = false
			}
		}
		delete(c.entries, oldestKey)
		evicted++
	}
	if evicted > 0 {
		slog.Debug("mcp: registry cache evicted",
			"count", evicted, "remaining", len(c.entries))
	}
}

// --- Normalisation ---
//
// The upstream response nests every record in `{server: {...}, _meta: {...}}`
// and carries fields we don't surface (schema URLs, timestamps, OIDC
// metadata). We flatten to one compact shape so the UI doesn't repeat
// the same field-plumbing logic.

// RegistryEntry is the browser-facing shape of one search result.
//
// Status carries the upstream lifecycle verdict, and only when it is NOT
// "active": the badge is its one consumer and an absent status reads as active
// anyway, so emitting the common case would put a constant on every row. See
// registryStatusActive.
type RegistryEntry struct {
	Name          string            `json:"name"`
	Title         string            `json:"title,omitempty"`
	Description   string            `json:"description,omitempty"`
	Version       string            `json:"version,omitempty"`
	Repository    string            `json:"repository,omitempty"`
	Status        string            `json:"status,omitempty"`
	StatusMessage string            `json:"status_message,omitempty"`
	Packages      []RegistryPackage `json:"packages,omitempty"`
	Remotes       []RegistryRemote  `json:"remotes,omitempty"`
}

// RegistryPackage is one install option from a stdio-speaking server.
// Only npm and oci are surfaced; everything else is hidden so the UI
// doesn't offer install paths we can't fulfil on the container.
type RegistryPackage struct {
	RegistryType string           `json:"registry_type"`
	Identifier   string           `json:"identifier"`
	Version      string           `json:"version,omitempty"`
	EnvVars      []RegistryEnvVar `json:"env_vars,omitempty"`
}

// RegistryRemote is one remote transport (http/sse) option.
type RegistryRemote struct {
	Type    string           `json:"type"`
	URL     string           `json:"url"`
	Headers []RegistryHeader `json:"headers,omitempty"`
}

// RegistryEnvVar / RegistryHeader describe a configurable field the user
// must fill in before the server will run.
type RegistryEnvVar struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Format      string `json:"format,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
}

// RegistryHeader describes an HTTP header the user must configure before the remote server will run.
type RegistryHeader struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Value       string `json:"value,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
}

// supportedPackageRegistries defines which package registry types
// vibekit can install. Only npm is supported (via npx -y).
// This is the single source of truth for install-capability gating:
// both normaliseRegistryResponse (which filters registry search results
// for the UI) and extractNpxPackage in prewarm.go (which decides what
// to pre-install) reference this map.
var supportedPackageRegistries = map[string]bool{"npm": true}

// supportedPackageTransports defines which transport types are valid
// for npm packages. Empty string means "default stdio".
// Shared with prewarm.go's extractNpxPackage: a server is only
// prewarm-eligible if its transport is in this set, ensuring the
// invariant "prewarm only targets packages the registry would surface".
var supportedPackageTransports = map[string]bool{"stdio": true, "": true}

// supportedRemoteTypes maps upstream remote type strings to the local
// Transport enum. Only these remote types are surfaced to the UI.
var supportedRemoteTypes = map[string]Transport{
	"streamable-http": TransportHTTP,
	"http":            TransportHTTP,
	"sse":             TransportSSE,
}

// registryWireResponse mirrors the upstream registry v0.1 search
// response. Only the fields vibekit surfaces are decoded; everything
// else (schema URLs, timestamps, OIDC metadata) is ignored. Named
// (vs an inline anonymous struct) so the per-package / per-remote
// mapping can be factored into convertRegistryPackage / convertRegistryRemote.
type registryWireResponse struct {
	Servers []registryWireEntry `json:"servers"`
}

// registryWireEntry is one row of the upstream list: the server document, plus
// the `_meta` sibling that carries the registry's own bookkeeping.
//
// The lifecycle status lives in `_meta`, NOT on the server object — verified
// against the live registry and its OpenAPI (RegistryExtensions: status is
// required, enum active|deprecated|deleted, with an optional statusMessage
// explaining a deprecation). A deprecated entry is still returned by search
// (only `deleted` is filtered, behind include_deleted), which is exactly why it
// looked live: vibekit dropped `_meta` entirely and had nothing to badge with.
type registryWireEntry struct {
	Meta   registryWireMeta   `json:"_meta"`
	Server registryWireServer `json:"server"`
}

// registryWireMeta mirrors the upstream ResponseMeta. The key is a namespaced
// literal, so it cannot be a Go identifier — hence the tag.
type registryWireMeta struct {
	Official registryWireOfficial `json:"io.modelcontextprotocol.registry/official"`
}

// registryWireOfficial is the subset of RegistryExtensions vibekit surfaces.
// The timestamps and isLatest are decoded by nobody: a row shows a version, and
// "published 8 months ago" is not a fact that changes an install decision.
type registryWireOfficial struct {
	Status        string `json:"status"`
	StatusMessage string `json:"statusMessage"`
}

// registryStatusActive is the upstream status a row does not need to announce.
const registryStatusActive = "active"

type registryWireServer struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Repository  struct {
		URL string `json:"url"`
	} `json:"repository"`
	Packages []registryWirePackage `json:"packages"`
	Remotes  []registryWireRemote  `json:"remotes"`
}

type registryWirePackage struct {
	RegistryType string `json:"registryType"`
	Identifier   string `json:"identifier"`
	Version      string `json:"version"`
	Transport    struct {
		Type string `json:"type"`
	} `json:"transport"`
	EnvironmentVariables []registryWireEnvVar `json:"environmentVariables"`
}

type registryWireEnvVar struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Format      string `json:"format"`
	IsRequired  bool   `json:"isRequired"`
	IsSecret    bool   `json:"isSecret"`
}

type registryWireRemote struct {
	Type    string               `json:"type"`
	URL     string               `json:"url"`
	Headers []registryWireHeader `json:"headers"`
}

type registryWireHeader struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Value       string `json:"value"`
	IsRequired  bool   `json:"isRequired"`
	IsSecret    bool   `json:"isSecret"`
}

func normaliseRegistryResponse(body []byte) []RegistryEntry {
	var raw registryWireResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return []RegistryEntry{}
	}
	out := make([]RegistryEntry, 0, len(raw.Servers))
	for i := range raw.Servers {
		if entry, ok := buildRegistryEntry(&raw.Servers[i]); ok {
			out = append(out, entry)
		}
	}
	return out
}

// buildRegistryEntry maps one upstream server record to a browser-facing
// RegistryEntry. The bool is false when the server exposes zero usable
// install paths (no supported package and no supported remote), in which
// case the caller skips it — common for schema-only publications or
// packages using registries vibekit doesn't support.
func buildRegistryEntry(row *registryWireEntry) (RegistryEntry, bool) {
	srv := &row.Server
	entry := RegistryEntry{
		Name:        srv.Name,
		Title:       srv.Title,
		Description: srv.Description,
		Version:     srv.Version,
		Repository:  srv.Repository.URL,
	}
	if st := row.Meta.Official.Status; st != "" && st != registryStatusActive {
		entry.Status = st
		entry.StatusMessage = row.Meta.Official.StatusMessage
	}
	for j := range srv.Packages {
		if pe, ok := convertRegistryPackage(&srv.Packages[j]); ok {
			entry.Packages = append(entry.Packages, pe)
		}
	}
	for j := range srv.Remotes {
		if re, ok := convertRegistryRemote(&srv.Remotes[j]); ok {
			entry.Remotes = append(entry.Remotes, re)
		}
	}
	if len(entry.Packages) == 0 && len(entry.Remotes) == 0 {
		return RegistryEntry{}, false
	}
	return entry, true
}

// convertRegistryPackage maps one upstream package to the browser-facing
// RegistryPackage. The bool is false when the package uses a registry or
// transport vibekit can't install (npm-only, stdio/default-only), in
// which case the caller skips it.
func convertRegistryPackage(pkg *registryWirePackage) (RegistryPackage, bool) {
	if !supportedPackageRegistries[pkg.RegistryType] {
		return RegistryPackage{}, false
	}
	if !supportedPackageTransports[pkg.Transport.Type] {
		return RegistryPackage{}, false
	}
	pe := RegistryPackage{
		RegistryType: pkg.RegistryType,
		Identifier:   pkg.Identifier,
		Version:      pkg.Version,
	}
	for k := range pkg.EnvironmentVariables {
		env := &pkg.EnvironmentVariables[k]
		pe.EnvVars = append(pe.EnvVars, RegistryEnvVar{
			Name:        env.Name,
			Description: env.Description,
			Format:      env.Format,
			Required:    env.IsRequired,
			Secret:      env.IsSecret,
		})
	}
	return pe, true
}

// convertRegistryRemote maps one upstream remote to the browser-facing
// RegistryRemote, normalising the transport type via supportedRemoteTypes
// (e.g. streamable-http to http). The bool is false when the remote type
// isn't one vibekit surfaces, in which case the caller skips it.
func convertRegistryRemote(rem *registryWireRemote) (RegistryRemote, bool) {
	transport, ok := supportedRemoteTypes[rem.Type]
	if !ok {
		return RegistryRemote{}, false
	}
	re := RegistryRemote{Type: string(transport), URL: rem.URL}
	for k := range rem.Headers {
		h := &rem.Headers[k]
		re.Headers = append(re.Headers, RegistryHeader{
			Name:        h.Name,
			Description: h.Description,
			Value:       h.Value,
			Required:    h.IsRequired,
			Secret:      h.IsSecret,
		})
	}
	return re, true
}
