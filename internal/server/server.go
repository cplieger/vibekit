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

	"github.com/cplieger/pinstall/v3"
	"github.com/cplieger/toolbelt/v3"
	"github.com/cplieger/toolbelt/v3/httpapi"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/webhttp/v2"
)

const port = "9847"

// Server holds shared state and registers all HTTP handlers.
type Server struct {
	forges        routeHandler
	mcpConfig     routeHandler
	chats         routeHandler
	git           routeHandler
	gitAI         routeHandler
	files         routeHandler
	auth          routeHandler
	push          pushService
	mcpStatus     routeHandler
	utilityPrompt utilityPrompter
	accountUsage  AccountUsageProvider
	policy        policyProvider
	policyReload  policyReloader
	agent         chatEngine
	steering      SteeringGenerator
	mcpRegistry   routeHandler
	staticFS      fs.FS
	// kiroDocs memoizes the .kiro inventory; a pointer so the zero Server needs no init.
	kiroDocs *docsCache
	// tabs is the open-tab set; nil (no config dir) answers an empty collection at version 0.
	tabs      tabReader
	cliRunner CLIRunner
	tools     *toolbelt.Engine
	// kiroReady is the install manager's readiness verdict, re-read per /api/health.
	kiroReady func() (bool, pinstall.Reason)
	// kiroRescan re-derives the active version from disk; nil leaves the repair route unmounted.
	kiroRescan func(context.Context) (bool, error)
	// authUnavailable reads a latch, never a probe (see WithAuthUnavailable).
	authUnavailable func() bool
	configDir       string
	workDir         string
	// trustedProxies feeds webhttp.WithClientIP; nil logs the unspoofable socket peer.
	trustedProxies []*net.IPNet
	// hostPolicy is the ALLOWED_HOSTS allowlist; nil or inactive accepts any Host.
	hostPolicy  *webhttp.HostPolicy
	acctUsage   acctUsageCache
	cliTimeouts cliTimeouts
	settingsMu  sync.Mutex
	// ready is true between listener bind and the shutdown signal.
	ready atomic.Bool
}

// Option configures a Server at construction time.
type Option func(*Server)

// WithSteering sets the steering generator used to produce environment.md for kiro-cli.
func WithSteering(g SteeringGenerator) Option { return func(s *Server) { s.steering = g } }

// WithAgent sets the agent runtime that manages bridge processes and SSE broadcasts.
func WithAgent(a chatEngine) Option { return func(s *Server) { s.agent = a } }

// WithChats sets the chat store, whose own router owns the chat HTTP surface.
func WithChats(c routeHandler) Option { return func(s *Server) { s.chats = c } }

// WithGit sets the git handler for non-AI git HTTP endpoints.
func WithGit(g routeHandler) Option { return func(s *Server) { s.git = g } }

// WithGitAI sets the route handler for AI-assisted git operations.
func WithGitAI(r routeHandler) Option { return func(s *Server) { s.gitAI = r } }

// WithFiles sets the file handler for workspace file read/write endpoints.
func WithFiles(f routeHandler) Option { return func(s *Server) { s.files = f } }

// WithAuth sets the auth handler for login, logout, and whoami endpoints.
func WithAuth(a routeHandler) Option { return func(s *Server) { s.auth = a } }

// WithPush sets the push service used for Web Push notification delivery.
func WithPush(p pushService) Option { return func(s *Server) { s.push = p } }

// WithMCPConfig sets the route handler for MCP server configuration endpoints.
func WithMCPConfig(r routeHandler) Option { return func(s *Server) { s.mcpConfig = r } }

// WithMCPStatus sets the route handler for the MCP runtime status endpoint.
func WithMCPStatus(r routeHandler) Option { return func(s *Server) { s.mcpStatus = r } }

// WithMCPRegistry sets the route handler for the MCP registry proxy endpoint.
func WithMCPRegistry(r routeHandler) Option { return func(s *Server) { s.mcpRegistry = r } }

// WithForges sets the route handler for forge (GitHub/GitLab/Gitea) HTTP endpoints.
func WithForges(r routeHandler) Option { return func(s *Server) { s.forges = r } }

// WithTools sets the tools engine backing the /api/tools surface.
func WithTools(e *toolbelt.Engine) Option { return func(s *Server) { s.tools = e } }

// WithUtilityPrompt sets the utility prompter used for AI-assisted tasks.
func WithUtilityPrompt(p utilityPrompter) Option {
	return func(s *Server) { s.utilityPrompt = p }
}

// WithAccountUsage sets the provider backing GET /api/account/usage.
func WithAccountUsage(p AccountUsageProvider) Option {
	return func(s *Server) { s.accountUsage = p }
}

// WithPolicy sets the Cedar policy provider backing GET /api/permissions and
// POST /api/permissions/explain. The rule writer needs no provider.
func WithPolicy(p policyProvider) Option {
	return func(s *Server) { s.policy = p }
}

// WithPolicyReload wires the recycle a security-profile change needs. Optional:
// unwired, a saved profile still reaches every session started afterwards and
// only the policy view lags until the utility session is next recycled.
func WithPolicyReload(p policyReloader) Option {
	return func(s *Server) { s.policyReload = p }
}

// WithStaticFS sets the embedded filesystem serving the compiled web UI.
func WithStaticFS(staticFS fs.FS) Option {
	return func(s *Server) { s.staticFS = staticFS }
}

// WithKiroCLI sets the resolvers for the kiro-cli binary and its environment.
// Resolvers rather than values: the install manager selects the active version
// AFTER the listener binds and can switch it later. The environment matters as
// much as the path, because `settings` re-execs a sibling binary through PATH
// and the overlay is what makes that search land inside the verified install.
func WithKiroCLI(resolvePath func() string, resolveEnv func() []string) Option {
	return func(s *Server) {
		s.cliRunner = &execCLIRunner{cliPath: resolvePath, env: resolveEnv}
	}
}

// WithKiroReady sets the kiro-cli readiness verdict /api/health reports. Unset
// leaves the probe reflecting only that the listener is up.
func WithKiroReady(ready func() (bool, pinstall.Reason)) Option {
	return func(s *Server) { s.kiroReady = ready }
}

// WithAuthUnavailable sets the sign-in leg /api/health reports. It must be a
// LATCH read, not a probe: the handler runs per request, so vending a token here
// would spawn kiro-cli on every monitor poll and could block on an SSO-OIDC
// refresh. Unset leaves readiness with no auth leg.
func WithAuthUnavailable(unavailable func() bool) Option {
	return func(s *Server) { s.authUnavailable = unavailable }
}

// WithKiroRescan sets the disk-rescan hook the loopback repair route exposes.
// Unset leaves the route unmounted.
func WithKiroRescan(rescan func(context.Context) (bool, error)) Option {
	return func(s *Server) { s.kiroRescan = rescan }
}

// WithConfigDir sets the configuration directory path used for chat files and settings.
func WithConfigDir(d string) Option { return func(s *Server) { s.configDir = d } }

// WithTabs wires the open-tab set that GET /api/tabs reads. A nil store stays a
// nil INTERFACE rather than an interface holding a nil pointer, or the handler's
// unwired branch would never be taken and the endpoint would nil-deref instead
// of answering the empty collection.
func WithTabs(st *tabs.Store) Option {
	return func(s *Server) {
		if st == nil {
			return
		}
		s.tabs = st
	}
}

// WithWorkDir sets the workspace directory served by the file handler and git endpoints.
func WithWorkDir(d string) Option { return func(s *Server) { s.workDir = d } }

// WithTrustedProxies sets the reverse-proxy networks trusted when resolving the
// access-log client_ip. Empty trusts nothing, so the socket peer is logged.
func WithTrustedProxies(trusted []*net.IPNet) Option {
	return func(s *Server) { s.trustedProxies = trusted }
}

// WithHostPolicy sets the exact-match Host allowlist the security middleware
// applies before the CSRF check — the anti-DNS-rebinding gate. A nil or inactive
// policy accepts any Host.
func WithHostPolicy(p *webhttp.HostPolicy) Option {
	return func(s *Server) { s.hostPolicy = p }
}

// New constructs a Server with the given options applied.
func New(opts ...Option) *Server {
	s := &Server{
		cliTimeouts: defaultCLITimeouts(),
		kiroDocs:    &docsCache{},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ListenAndServe registers all routes and starts the HTTP server.
// Blocks until SIGTERM/SIGINT, then shuts down gracefully.
//
// Every route is a plain path with the method gated in the handler
// (httpreply.RequireMethod). Do not put a method back on a pattern here:
// ServeMux only synthesises its 405 when NO pattern matched, and the "/" SPA
// mount matches every path and method, so a method-mismatched request would be
// answered 200 with index.html instead of an RFC 9110 §15.5.6 405.
func (s *Server) ListenAndServe() error {
	mux := http.NewServeMux()
	mux.Handle("/", spaHandler(s.staticFS))
	s.agent.RegisterRoutes(mux)
	mux.HandleFunc("/api/version", s.handleVersion)
	mux.HandleFunc("/api/diagnostics", s.handleDiagnostics)
	mux.HandleFunc("/api/kiro-settings", s.handleKiroSettings)
	s.chats.RegisterRoutes(mux)
	mux.HandleFunc("/api/health", s.handleHealth)
	// Absent rather than answering a misleading 503 when nothing owns the install.
	if s.kiroRescan != nil {
		mux.Handle(kiroRescanPath, loopbackOnly(kiroRescanSurface, http.HandlerFunc(s.handleKiroRescan)))
	}
	// Same loopback gate, but unconditional: a goroutine dump is a property of
	// the process, so it is always answerable.
	mux.Handle(pprofPath, pprofHandler())
	s.auth.RegisterRoutes(mux)
	mux.HandleFunc("/api/steering", s.handleSteering)
	// The exact /api/tools/status pattern below wins over this subtree mount for
	// EVERY method, which keeps it out of toolbelt's /api/tools/{name} handlers
	// with name="status".
	if s.tools != nil {
		toolsAPI := httpapi.Handler(s.tools, "/api/tools")
		mux.Handle("/api/tools", toolsAPI)
		mux.Handle("/api/tools/", toolsAPI)
	}
	mux.HandleFunc("/api/tools/status", handleToolStatus)
	s.git.RegisterRoutes(mux)
	if s.gitAI != nil {
		s.gitAI.RegisterRoutes(mux)
	}
	s.files.RegisterRoutes(mux)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/tabs", s.handleTabs)
	mux.HandleFunc("/api/workspace/kiro-config", s.handleKiroConfig)
	mux.HandleFunc("/api/workspace/kiro-docs", s.handleKiroDocs)
	s.mcpConfig.RegisterRoutes(mux)
	s.mcpStatus.RegisterRoutes(mux)
	s.mcpRegistry.RegisterRoutes(mux)
	if s.forges != nil {
		s.forges.RegisterRoutes(mux)
	}
	mux.HandleFunc("/api/permissions", s.handlePolicyView)
	mux.HandleFunc("/api/permissions/explain", s.handlePolicyExplain)
	mux.HandleFunc("/api/permissions/rules", s.handlePolicyRules)
	mux.HandleFunc("/api/permissions/profile", s.handlePolicyProfile)
	mux.HandleFunc("/api/utility/explain-error", s.handleUtilityExplainError)
	mux.HandleFunc("/api/utility/resolve-conflict", s.handleUtilityResolveConflict)
	mux.HandleFunc("/api/account/usage", s.handleAccountUsage)
	s.push.RegisterRoutes(mux)

	// Derived from the embedded index.html so the inline importmap's sha256 hash
	// stays in sync with what the browser actually sees.
	cspPolicy, err := buildCSPPolicy(s.staticFS)
	if err != nil {
		return fmt.Errorf("build CSP: %w", err)
	}

	// Deduped inside securityMiddleware (so only same-origin, CSRF-checked
	// requests are) and inside the access logger (so replays stay logged).
	idem := newIdempotencyCache(idempotencyTTL)
	defer idem.stop()

	// srv.MaxHeaderValueCount is deliberately left at its default: the 431 it
	// answers is written BELOW the middleware chain, so a cap tuned under a
	// future proxy's header count would refuse requests with no access-log line,
	// no request id and no client_ip.
	handler := webhttp.Chain(mux, s.middlewareStack(cspPolicy, idem)...)
	srv := webhttp.NewServer(handler)
	srv.Addr = ":" + port

	// Bind up front so port-in-use surfaces synchronously, rather than after the
	// serve goroutine launches.
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", srv.Addr)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	s.ready.Store(true)
	slog.Info("Kiro Web UI listening", "port", port)
	// DNS rebinding rides the victim's BROWSER, so it reaches even a loopback
	// bind, and this HTTP surface carries no auth of its own.
	if !s.hostPolicy.Active() {
		slog.Warn("ALLOWED_HOSTS is unset or blank; any Host header is accepted, leaving DNS rebinding open even on loopback/private binds",
			"hint", "set ALLOWED_HOSTS to the exact hostnames/IPs you browse to (e.g. localhost,192.168.1.5,vibekit.example.com)")
	}

	// The pre-drain hook keeps agent-before-server ordering: readiness flips and
	// the runtime cancels its streams before the HTTP drain runs.
	runErr := webhttp.Run(ctx, srv, ln, nil, webhttp.WithPreDrain(func(drainCtx context.Context) {
		slog.Info("received signal, shutting down", "cause", context.Cause(ctx))
		s.ready.Store(false)
		// Run calls this hook SYNCHRONOUSLY before srv.Shutdown, so an unbounded
		// teardown here would consume the whole grace: pass the budget on.
		if err := s.agent.Shutdown(drainCtx); err != nil {
			slog.Error("agent runtime shutdown did not finish within the grace period", "error", err)
		}
	}))
	s.ready.Store(false) // no-op on the signal path; covers a serve failure
	return runErr
}

// middlewareStack returns the middleware wrapping the route mux, OUTERMOST
// FIRST (webhttp.Chain's order): request-id access logging, panic recovery, the
// security layer (dynamic CSP + the ALLOWED_HOSTS allowlist + stdlib CSRF), the
// canonical-path gate, the REST idempotency dedup, then the mux.
//
// A method rather than an inline literal in ListenAndServe so the ORDER is
// assertable without binding a port; the ordering is a security property.
func (s *Server) middlewareStack(cspPolicy string, idem *idempotencyCache) []webhttp.Middleware {
	return []webhttp.Middleware{
		webhttp.Logging(
			// The long-lived SSE stream would log one open-forever line.
			webhttp.WithSkipPaths("/api/events"),
			// The shell PTY is silenced by RESPONSE, not by path: only a recorded
			// 101 or a bare hijack drops the record, so every handshake REFUSAL on
			// the same path keeps its status, request id and client_ip.
			webhttp.WithSkipUpgrades(true),
			// Keeps healthy probes (every 30s) at Debug and a failing one at Warn.
			webhttp.ProbeLogLevel("/api/health"),
			webhttp.WithClientIP(s.trustedProxies...),
		),
		webhttp.Recoverer(),
		func(next http.Handler) http.Handler { return securityMiddleware(cspPolicy, s.hostPolicy, next) },
		// INSIDE the host allowlist and the CSRF check, whose 403 must not be
		// shadowed by a 400 about spelling; OUTSIDE the mux, because ServeMux
		// canonicalizes before selecting a pattern so no handler can refuse for
		// itself; outside the idempotency cache, so a refused spelling mints no
		// entry a later well-formed retry would replay.
		canonicalAPIPath,
		idem.middleware,
		// INNERMOST, so it sees exactly what a handler wrote: the idempotency
		// cache stores identity bytes and a replay re-negotiates against the
		// replaying client's own Accept-Encoding, while the outermost access
		// logger counts on-the-wire bytes.
		compressJSON,
	}
}

// requirePOST reports whether r.Method is POST, writing a 405 when it is not.
// Every command endpoint accepts only POST.
func requirePOST(w http.ResponseWriter, r *http.Request) bool {
	return httpreply.RequireMethod(w, r, http.MethodPost)
}

// decodeBody applies LimitBody, decodes JSON into v, and returns true
// on success. On failure it writes a 400 response and returns false.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	return httpreply.DecodeBody(w, r, v, "bad request")
}

// healthBody is the readiness envelope handleHealth and handleKiroRescan both
// answer with. A struct rather than a map because encoding/json sorts map keys,
// which put the fields in a different order from webhttp.ReadinessHandler's.
// This cannot simply BE that handler: the verdict here is composite (a second
// reason for an unavailable kiro-cli) while ReadinessChecker is Ready() bool.
type healthBody struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// handleHealth answers the readiness envelope: 200 {"status":"ok"} once the
// listener is serving, 503 {"status":"unready",...} during startup, during the
// shutdown drain, or while kiro-cli is unavailable.
//
// The kiro-cli verdict is version-aware and re-read per request — a lock and two
// field reads, never a subprocess — so an install completing heals the signal
// with no restart. READINESS only: wire it to a readinessProbe, never a
// livenessProbe.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	// Under RFC 9111 a 200 with no explicit freshness is heuristically cacheable,
	// and a cached "ok" outliving the readiness it reported keeps traffic arriving
	// at an instance that has begun draining.
	w.Header().Set("Cache-Control", "no-store")
	unready := func(reason string) {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, healthBody{
			Status: "unready",
			Reason: reason,
		})
	}
	if !s.ready.Load() {
		unready("starting up or shutting down")
		return
	}
	if s.kiroReady != nil {
		if ok, why := s.kiroReady(); !ok {
			unready(kiroReasonText(why))
			return
		}
	}
	// The kiro-cli leg stays FIRST: with no binary there is nothing to vend a
	// token, and the envelope carries only one reason.
	if s.authUnavailable != nil && s.authUnavailable() {
		unready(reasonSignIn)
		return
	}
	webhttp.WriteJSON(w, healthBody{Status: "ok"})
}
