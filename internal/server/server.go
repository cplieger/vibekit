package server

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/cplieger/toolbelt/v2"
	"github.com/cplieger/toolbelt/v2/httpapi"
	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/webhttp"
)

const port = "9847"

// Server holds shared state and registers all HTTP handlers.
type Server struct {
	forges        api.RouteHandler
	mcpConfig     api.RouteHandler
	chats         api.ChatStore
	git           api.GitHandler
	gitAI         api.RouteHandler
	files         api.FileHandler
	auth          api.AuthHandler
	push          api.PushService
	mcpStatus     api.RouteHandler
	utilityPrompt api.UtilityPrompter
	accountUsage  api.AccountUsageProvider
	policy        api.PolicyProvider
	hub           api.Hub
	steering      api.SteeringGenerator
	mcpRegistry   api.RouteHandler
	staticFS      fs.FS
	cliRunner     CLIRunner
	tools         *toolbelt.Engine
	// kiroReady is the kiro-cli install manager's readiness verdict plus its
	// reason, consulted per /api/health probe so a recovery becomes visible
	// without a restart. Nil = this server does not own the install, which
	// happens only outside the container (a bare `go run` with no pins), and
	// readiness stays pure-listener. Every container boot wires it.
	kiroReady func() (bool, string)
	// kiroRescan re-derives the active kiro-cli version from what is on disk,
	// downloading nothing. It backs the loopback repair hook; nil when there is
	// no manager, and then the route is not mounted at all.
	kiroRescan func(context.Context) (bool, error)
	configDir  string
	workDir    string
	// trustedProxies is the reverse-proxy network set passed to
	// webhttp.WithClientIP so the access log's client_ip resolves the
	// real client from a trusted X-Forwarded-For. Nil (unconfigured) =
	// log the unspoofable socket peer.
	trustedProxies []*net.IPNet
	// hostPolicy is the ALLOWED_HOSTS exact-match Host allowlist the
	// security middleware applies before the CSRF check (anti-DNS-rebinding;
	// see internal/composition parseAllowedHosts). Nil/inactive = any Host
	// accepted.
	hostPolicy  *webhttp.HostPolicy
	acctUsage   acctUsageCache
	cliTimeouts cliTimeouts
	settingsMu  sync.Mutex
	// ready flips to true once the listener binds and srv.Serve is
	// running; flips back to false on shutdown signal so /api/health
	// reports unready during drain. Same semantic across the cplieger
	// Go apps (subflux, plex-exporter, web-terminal-kiro).
	ready atomic.Bool
}

// Option configures a Server at construction time.
type Option func(*Server)

// WithSteering sets the steering generator used to produce environment.md for kiro-cli.
func WithSteering(g api.SteeringGenerator) Option { return func(s *Server) { s.steering = g } }

// WithHub sets the hub that manages bridge processes and SSE broadcasts.
func WithHub(h api.Hub) Option { return func(s *Server) { s.hub = h } }

// WithChats sets the chat store used for reading and writing chat files.
func WithChats(c api.ChatStore) Option { return func(s *Server) { s.chats = c } }

// WithGit sets the git handler for non-AI git HTTP endpoints.
func WithGit(g api.GitHandler) Option { return func(s *Server) { s.git = g } }

// WithGitAI sets the route handler for AI-assisted git operations.
func WithGitAI(r api.RouteHandler) Option { return func(s *Server) { s.gitAI = r } }

// WithFiles sets the file handler for workspace file read/write endpoints.
func WithFiles(f api.FileHandler) Option { return func(s *Server) { s.files = f } }

// WithAuth sets the auth handler for login, logout, and whoami endpoints.
func WithAuth(a api.AuthHandler) Option { return func(s *Server) { s.auth = a } }

// WithPush sets the push service used for Web Push notification delivery.
func WithPush(p api.PushService) Option { return func(s *Server) { s.push = p } }

// WithMCPConfig sets the route handler for MCP server configuration endpoints.
func WithMCPConfig(r api.RouteHandler) Option { return func(s *Server) { s.mcpConfig = r } }

// WithMCPStatus sets the route handler for the MCP runtime status endpoint.
func WithMCPStatus(r api.RouteHandler) Option { return func(s *Server) { s.mcpStatus = r } }

// WithMCPRegistry sets the route handler for the MCP registry proxy endpoint.
func WithMCPRegistry(r api.RouteHandler) Option { return func(s *Server) { s.mcpRegistry = r } }

// WithForges sets the route handler for forge (GitHub/GitLab/Gitea) HTTP endpoints.
func WithForges(r api.RouteHandler) Option { return func(s *Server) { s.forges = r } }

// WithTools sets the tools engine backing the /api/tools surface.
func WithTools(e *toolbelt.Engine) Option { return func(s *Server) { s.tools = e } }

// WithUtilityPrompt sets the utility prompter used for AI-assisted tasks (rename, commit message, etc.).
func WithUtilityPrompt(p api.UtilityPrompter) Option {
	return func(s *Server) { s.utilityPrompt = p }
}

// WithAccountUsage sets the provider for account/subscription usage,
// served at GET /api/account/usage (sidebar footer).
func WithAccountUsage(p api.AccountUsageProvider) Option {
	return func(s *Server) { s.accountUsage = p }
}

// WithPolicy sets the native Cedar policy provider, backing the read-only
// policy view at GET /api/permissions and the pre-flight simulation at
// POST /api/permissions/explain. The rule WRITER at POST /api/permissions/rules
// needs no provider (it is a file write KAS hot-reloads).
func WithPolicy(p api.PolicyProvider) Option {
	return func(s *Server) { s.policy = p }
}

// WithStaticFS sets the embedded filesystem serving the compiled web UI.
func WithStaticFS(staticFS fs.FS) Option {
	return func(s *Server) { s.staticFS = staticFS }
}

// WithCLIPath sets the RESOLVER for the kiro-cli binary used by the CLI
// sub-operations (/api/version, /api/diagnostics, /api/kiro-settings).
//
// A resolver rather than a string because the install manager selects the
// active version AFTER the listener binds and can switch it later: a path
// captured at construction would pin every shell-out to whatever was installed
// first, which on a first boot is nothing at all.
func WithCLIPath(resolve func() string) Option {
	return func(s *Server) { s.cliRunner = &execCLIRunner{cliPath: resolve} }
}

// WithKiroReady sets the kiro-cli readiness verdict /api/health reports. Unset
// leaves the health probe reflecting only that the listener is up.
func WithKiroReady(ready func() (bool, string)) Option {
	return func(s *Server) { s.kiroReady = ready }
}

// WithKiroRescan sets the disk-rescan hook the loopback repair route exposes.
// Unset leaves the route unmounted.
func WithKiroRescan(rescan func(context.Context) (bool, error)) Option {
	return func(s *Server) { s.kiroRescan = rescan }
}

// WithConfigDir sets the configuration directory path used for chat files and settings.
func WithConfigDir(d string) Option { return func(s *Server) { s.configDir = d } }

// WithWorkDir sets the workspace directory served by the file handler and git endpoints.
func WithWorkDir(d string) Option { return func(s *Server) { s.workDir = d } }

// WithTrustedProxies sets the reverse-proxy networks trusted when
// resolving the access-log client_ip via webhttp.WithClientIP. Empty/nil
// trusts nothing, so the unspoofable socket-peer host is logged (the
// spoof-safe default for a directly-exposed deployment).
func WithTrustedProxies(trusted []*net.IPNet) Option {
	return func(s *Server) { s.trustedProxies = trusted }
}

// WithHostPolicy sets the exact-match Host allowlist (parsed from
// ALLOWED_HOSTS) that the security middleware applies before the CSRF
// check — the anti-DNS-rebinding gate. A nil or inactive policy is a
// pass-through (any Host accepted, the backward-compatible default).
func WithHostPolicy(p *webhttp.HostPolicy) Option {
	return func(s *Server) { s.hostPolicy = p }
}

// New constructs a Server with the given options applied.
func New(opts ...Option) *Server {
	s := &Server{
		cliTimeouts: defaultCLITimeouts(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ListenAndServe registers all routes and starts the HTTP server.
// Blocks until SIGTERM/SIGINT, then shuts down gracefully.
func (s *Server) ListenAndServe() error {
	mux := http.NewServeMux()
	mux.Handle("/", spaHandler(s.staticFS))
	s.hub.RegisterRoutes(mux)
	mux.HandleFunc("/api/version", s.handleVersion)
	mux.HandleFunc("/api/diagnostics", s.handleDiagnostics)
	mux.HandleFunc("/api/kiro-settings", s.handleKiroSettings)
	s.chats.RegisterRoutes(mux)
	mux.HandleFunc("/api/health", s.handleHealth)
	// The kiro-cli repair hook exists only when this server owns the install
	// (see WithKiroRescan): with no pins in the environment there is nothing to
	// rescan, so the route is absent rather than answering a misleading 503.
	if s.kiroRescan != nil {
		mux.Handle("POST "+kiroRescanPath, loopbackOnly(http.HandlerFunc(s.handleKiroRescan)))
	}
	s.auth.RegisterRoutes(mux)
	mux.HandleFunc("/api/steering", s.handleSteering)
	// Tools REST surface: the toolbelt httpapi projection, mounted at
	// the exact prefix and the subtree. /api/tools/status stays
	// app-owned (vibekit's feature-gating PATH probes); its exact
	// GET pattern wins over the subtree mount.
	if s.tools != nil {
		toolsAPI := httpapi.Handler(s.tools, "/api/tools")
		mux.Handle("/api/tools", toolsAPI)
		mux.Handle("/api/tools/", toolsAPI)
	}
	mux.HandleFunc("GET /api/tools/status", s.handleToolStatus)
	s.git.RegisterRoutes(mux)
	if s.gitAI != nil {
		s.gitAI.RegisterRoutes(mux)
	}
	s.files.RegisterRoutes(mux)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/workspace/kiro-config", s.handleKiroConfig)
	s.mcpConfig.RegisterRoutes(mux)
	s.mcpStatus.RegisterRoutes(mux)
	s.mcpRegistry.RegisterRoutes(mux)
	if s.forges != nil {
		s.forges.RegisterRoutes(mux)
	}
	mux.HandleFunc("GET /api/permissions", s.handlePolicyView)
	mux.HandleFunc("/api/permissions/explain", s.handlePolicyExplain)
	mux.HandleFunc("/api/permissions/rules", s.handlePolicyRules)
	mux.HandleFunc("/api/utility/explain-error", s.handleUtilityExplainError)
	mux.HandleFunc("/api/utility/resolve-conflict", s.handleUtilityResolveConflict)
	mux.HandleFunc("GET /api/account/usage", s.handleAccountUsage)
	s.push.RegisterRoutes(mux)

	// Compute the CSP once from the embedded index.html so the inline
	// importmap's sha256 hash is always in sync with what the browser
	// actually sees — no hardcoded literal to hand-update on every
	// importmap edit. Startup fails fast if the embed is malformed.
	cspPolicy, err := buildCSPPolicy(s.staticFS)
	if err != nil {
		return fmt.Errorf("build CSP: %w", err)
	}

	// REST Idempotency-Key dedup wraps the mux from the inside: it sits
	// inside securityMiddleware (so only same-origin, CSRF-checked
	// requests are deduped) and inside the access logger (so replays are
	// still access-logged). Its janitor goroutine is stopped via defer
	// when ListenAndServe returns — both the errCh and signal paths
	// below return, so the cache lives exactly as long as the server.
	idem := newIdempotencyCache(idempotencyTTL)
	defer idem.stop()

	// Middleware, outermost first: request-id access logging (webhttp.Logging,
	// skipping the long-lived SSE/WebSocket streams so they don't log a single
	// open-forever line), then panic recovery, then the security layer (dynamic
	// CSP + stdlib CSRF), then the REST idempotency-dedup, then the mux.
	// webhttp.NewServer's defaults match the former hand-rolled server exactly
	// (ReadHeaderTimeout 10s, IdleTimeout 120s, MaxHeaderBytes 1 MiB, and
	// Read/WriteTimeout left unset for the SSE, WebSocket, and streaming-zip
	// responses).
	//
	// webhttp.WithClientIP adds a spoof-safe "client_ip" to every access line:
	// with no trusted proxies it is the unspoofable socket peer; when
	// TRUSTED_PROXIES lists the reverse proxy's CIDRs it is the real client
	// resolved from a trusted X-Forwarded-For. It costs nothing on the skipped
	// streaming paths.
	handler := webhttp.Chain(mux,
		webhttp.Logging(
			webhttp.WithSkipPaths("/api/events", "/api/shell/ws"),
			// /api/health is probed every 30s (Docker HEALTHCHECK curl +
			// Gatus). The fleet-standard ProbeLogLevel keeps healthy probes
			// at Debug (out of the shipped stream) and surfaces a failing
			// probe at Warn/Error — previously every probe logged at Info
			// (~5,760 noise lines/day) while carrying no failure emphasis.
			webhttp.ProbeLogLevel("/api/health"),
			webhttp.WithClientIP(s.trustedProxies...),
		),
		webhttp.Recoverer(),
		func(next http.Handler) http.Handler { return securityMiddleware(cspPolicy, s.hostPolicy, next) },
		idem.middleware,
	)
	srv := webhttp.NewServer(handler)
	srv.Addr = ":" + port

	// Bind listener up front so port-in-use surfaces synchronously
	// (rather than appearing after the serve goroutine launches).
	// Bind-then-flip-ready mirrors subflux/plex-exporter so /api/health
	// reports unready until the listener is genuinely ready to accept.
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", srv.Addr)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	s.ready.Store(true)
	slog.Info("Kiro Web UI listening", "port", port)
	// DNS rebinding rides the victim's BROWSER, so it reaches even a
	// loopback/private bind, and vibekit's HTTP surface (a PTY shell
	// included) carries no auth of its own — the exact-Host allowlist is
	// the gate that closes it (see internal/composition parseAllowedHosts).
	if !s.hostPolicy.Active() {
		slog.Warn("ALLOWED_HOSTS is unset or blank; any Host header is accepted, leaving DNS rebinding open even on loopback/private binds",
			"hint", "set ALLOWED_HOSTS to the exact hostnames/IPs you browse to (e.g. localhost,192.168.1.5,vibekit.example.com)")
	}

	// webhttp.Run owns the serve/shutdown sequence (default 5s grace, matching
	// the previous hand-rolled shutdown timeout). The pre-drain hook preserves
	// vibekit's hub-before-server ordering: readiness flips, then the hub stops
	// bridges and cancels the SSE/WebSocket streams, and only then does the
	// HTTP drain run.
	runErr := webhttp.Run(ctx, srv, ln, nil, webhttp.WithPreDrain(func(context.Context) {
		slog.Info("received signal, shutting down", "cause", context.Cause(ctx))
		s.ready.Store(false)
		s.hub.Shutdown()
	}))
	s.ready.Store(false) // no-op on the signal path; covers a serve failure
	return runErr
}

// requirePOST returns true if r.Method is POST; otherwise it writes a
// 405 response and returns false. All vibekit command endpoints accept
// only POST, so this specialised wrapper avoids an always-identical
// method argument at call sites.
func requirePOST(w http.ResponseWriter, r *http.Request) bool {
	return api.RequireMethod(w, r, http.MethodPost)
}

// decodeBody applies LimitBody, decodes JSON into v, and returns true
// on success. On failure it writes a 400 response and returns false.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	return api.DecodeBody(w, r, v, "bad request")
}

// healthBody is the readiness envelope handleHealth and the kiro-cli repair
// hook (handleKiroRescan) both answer with, so an operator reads the same shape
// from either surface. A struct, not a map, and that is the point:
// webhttp.ReadinessHandler fixes its key order with a struct, while
// this handler built a map and encoding/json sorts map keys — so the "canonical
// envelope shared across the cplieger Go apps" claimed below was emitting
// {"reason":…,"status":…} here and {"status":…,"reason":…} from the library that
// owns it. Matching the library byte-for-byte is what makes the claim true.
//
// This handler cannot simply BE webhttp.ReadinessHandler: its verdict is
// composite (a second reason for an unavailable kiro-cli) while the library's
// ReadinessChecker is Ready() bool. Extending the library to absorb a composite
// verdict was considered and rejected as a wide public surface for a six-line
// envelope; matching the wire shape is the cheap half that removes the drift.
type healthBody struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// handleHealth returns the liveness+readiness status. Emits the
// canonical JSON envelope shared across the cplieger Go apps
// (vibekit, web-terminal-kiro, subflux, registry-stats, plex-exporter): 200 with
// {"status":"ok"} when the listener is bound and serving; 503 with
// {"status":"unready",...} during startup or graceful shutdown drain —
// or when kiro-cli is unavailable, which is how a failed or still-running
// install surfaces to `docker ps`, monitoring and the client's degraded banner.
//
// The kiro-cli verdict is the install manager's (internal/kirocli): it is
// VERSION-AWARE, where the check this replaced only asked whether SOMETHING
// named kiro-cli was on PATH — so a binary drifted from the pin, or one whose
// auto-update could not be switched off, now reads unready instead of healthy.
// Reading it is a lock-and-two-field-read, never a subprocess spawn, and it
// re-evaluates per request, so an install completing (or an in-container repair
// plus a rescan) heals the signal with no restart.
//
// This is a READINESS signal: under `restart: unless-stopped` nothing restarts
// on the unhealthy state, so there is no restart loop; if ever run
// under Swarm/k8s, wire it to a readinessProbe, not a livenessProbe.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	// A readiness verdict must never be cached. Under RFC 9111 a 200 carrying no
	// explicit freshness is heuristically cacheable, and this answer is never
	// valid a moment later: a cached "ok" outliving the readiness it reported
	// keeps traffic arriving at an instance that has begun draining, defeating the
	// gate exactly when it matters. The unready direction is safe by accident (503
	// is not heuristically cacheable). webhttp.ReadinessHandler now sets the same
	// header, so every app in the fleet answers this the same way.
	w.Header().Set("Cache-Control", "no-store")
	unready := func(reason string) {
		api.WriteJSONStatus(w, http.StatusServiceUnavailable, healthBody{
			Status: "unready",
			Reason: reason,
		})
	}
	if !s.ready.Load() {
		unready("starting up or shutting down")
		return
	}
	if s.kiroReady != nil {
		if ok, reason := s.kiroReady(); !ok {
			unready(reason)
			return
		}
	}
	api.WriteJSON(w, healthBody{Status: "ok"})
}
