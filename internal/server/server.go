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
	// kiroDocs memoizes the document-oriented .kiro inventory behind a directory-mtime
	// signature. A pointer so the zero Server the method-guard tests use needs no init.
	kiroDocs *docsCache
	// tabs is the open-tab SET. Nil in a bare test server and in a build with no config
	// dir; the handler then answers an empty collection at version 0 rather than 404. A
	// one-method ROLE rather than *tabs.Store, because the endpoint's contract is that the
	// set and its version come from ONE call and only a double that MOVES between two
	// calls holds a handler to that.
	tabs      tabReader
	cliRunner CLIRunner
	tools     *toolbelt.Engine
	// kiroReady is the install manager's readiness verdict plus its TYPED reason,
	// consulted per /api/health probe so a recovery is visible without a restart. Nil =
	// this server does not own the install, and readiness stays pure-listener.
	kiroReady func() (bool, pinstall.Reason)
	// kiroRescan re-derives the active kiro-cli version from disk, downloading nothing.
	// Nil when there is no manager, and then the route is not mounted at all.
	kiroRescan func(context.Context) (bool, error)
	// authUnavailable reports whether the last attempt to vend a KAS access token failed.
	// A LATCH read, never a probe: /api/health consults it per request, and probing would
	// spawn kiro-cli on a monitor's poll and could block on an SSO-OIDC refresh.
	authUnavailable func() bool
	configDir       string
	workDir         string
	// trustedProxies is passed to webhttp.WithClientIP. Nil = log the unspoofable socket
	// peer rather than resolving X-Forwarded-For.
	trustedProxies []*net.IPNet
	// hostPolicy is the ALLOWED_HOSTS exact-match allowlist the security middleware
	// applies before the CSRF check. Nil or inactive = any Host accepted.
	hostPolicy *webhttp.HostPolicy
	// onListen fires once per successful bind, before serving: it is what tells the rest
	// of the app this process is the one serving its config dir.
	onListen    func()
	acctUsage   acctUsageCache
	cliTimeouts cliTimeouts
	settingsMu  sync.Mutex
	// ready flips true once the listener binds, and false on the shutdown signal so
	// /api/health reports unready during drain. Same semantic fleet-wide.
	ready atomic.Bool
}

// Option configures a Server at construction time.
type Option func(*Server)

// WithSteering sets the steering generator used to produce environment.md for kiro-cli.
func WithSteering(g SteeringGenerator) Option { return func(s *Server) { s.steering = g } }

// WithAgent sets the agent runtime that manages bridge processes and SSE
// broadcasts. It was WithHub over a chatEngine; the dependency is *agent.Runtime
// now and the name says which collaborator it is rather than what topology it
// used to be.
func WithAgent(a chatEngine) Option { return func(s *Server) { s.agent = a } }

// WithChats sets the chat store, whose own router owns the chat HTTP surface.
//
// The parameter is routeHandler because mounting those routes is the ONLY thing
// this package does with the store: 1 of its 11 methods. The chat reads
// (GET /api/chats, /api/chats/{id}, its search and turns endpoints) are
// registered and served by internal/chat's own router, so the server neither
// reads nor writes a chat itself. It used to hold all of them to call one.
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

// WithUtilityPrompt sets the utility prompter used for AI-assisted tasks
// (error explanations, commit messages, PR descriptions, conflict resolution).
func WithUtilityPrompt(p utilityPrompter) Option {
	return func(s *Server) { s.utilityPrompt = p }
}

// WithAccountUsage sets the provider for account/subscription usage,
// served at GET /api/account/usage (sidebar footer).
func WithAccountUsage(p AccountUsageProvider) Option {
	return func(s *Server) { s.accountUsage = p }
}

// WithPolicy sets the native Cedar policy provider, backing the read-only
// policy view at GET /api/permissions and the pre-flight simulation at
// POST /api/permissions/explain. The rule WRITER at POST /api/permissions/rules
// needs no provider (it is a file write KAS hot-reloads).
func WithPolicy(p policyProvider) Option {
	return func(s *Server) { s.policy = p }
}

// WithPolicyReload wires the recycle a security-profile change needs. Optional:
// unwired, a profile still persists and still reaches every session started
// afterwards, and only the policy VIEW keeps describing the previous profile until
// the utility session is next recycled on its own.
func WithPolicyReload(p policyReloader) Option {
	return func(s *Server) { s.policyReload = p }
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
// leaves the health probe reflecting only that the listener is up. The reason is
// the install manager's typed one; this package owns the wording it serves.
func WithKiroReady(ready func() (bool, pinstall.Reason)) Option {
	return func(s *Server) { s.kiroReady = ready }
}

// WithAuthUnavailable sets the sign-in leg /api/health reports after the
// kiro-cli leg. It must be a LATCH read, not a probe: the readiness handler runs
// per request and stays a lock and two field reads, so vending a token here to
// find out would spawn kiro-cli on every monitor poll and could block on an
// SSO-OIDC refresh. Unset leaves readiness with no auth leg.
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

// WithTabs wires the open-tab set that GET /api/tabs reads.
//
// A nil store stays a nil INTERFACE rather than becoming a non-nil interface
// holding a nil pointer, or the handler's unwired branch would never be taken and
// the endpoint would nil-deref instead of answering the empty collection.
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

// WithOnListen registers a callback fired once the listener has SUCCESSFULLY
// bound, which is this app's evidence that this process — rather than another one
// already on the port — is serving its config dir.
//
// A bind failure returns before it, so a boot that could not bind never fires it.
// It runs on the bind goroutine ahead of serving, so it must not block; the one
// caller closes a channel (see composition, the session-sweep gate).
func WithOnListen(fn func()) Option {
	return func(s *Server) { s.onListen = fn }
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

// ListenAndServe registers all routes and starts the HTTP server. Blocks until
// SIGTERM/SIGINT, then shuts down gracefully.
//
// EVERY ROUTE HERE IS A PLAIN PATH AND THE METHOD IS GATED IN THE HANDLER. ServeMux
// synthesises its 405 + Allow only when NO pattern matched at all, and the "/" SPA mount
// matches every path and method, so a method-mismatched request lands there and is answered
// 200 with index.html. That fallback is not optional, so httpreply.RequireMethod is the
// whole of vibekit's 405 surface: do not put a method back on a pattern here.
func (s *Server) ListenAndServe() error {
	mux := http.NewServeMux()
	mux.Handle("/", spaHandler(s.staticFS))
	s.agent.RegisterRoutes(mux)
	mux.HandleFunc("/api/version", s.handleVersion)
	mux.HandleFunc("/api/diagnostics", s.handleDiagnostics)
	mux.HandleFunc("/api/kiro-settings", s.handleKiroSettings)
	s.chats.RegisterRoutes(mux)
	mux.HandleFunc("/api/health", s.handleHealth)
	// Only when this server owns the install: with no pins there is nothing to rescan, so
	// the route is absent rather than answering a misleading 503.
	if s.kiroRescan != nil {
		mux.Handle(kiroRescanPath, loopbackOnly(kiroRescanSurface, http.HandlerFunc(s.handleKiroRescan)))
	}
	// Runtime profiles, same loopback gate, but unconditional: a goroutine dump is a
	// property of the process and is always answerable.
	mux.Handle(pprofPath, pprofHandler())
	s.auth.RegisterRoutes(mux)
	mux.HandleFunc("/api/steering", s.handleSteering)
	// The toolbelt httpapi projection, at the exact prefix and the subtree.
	// /api/tools/status stays app-owned, and its exact pattern winning over the subtree
	// for EVERY method is what keeps it out of toolbelt's {name} handlers as name="status".
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

	// Computed from the embedded index.html so the inline importmap's sha256 stays in sync
	// with what the browser sees, with no literal to hand-update per importmap edit.
	cspPolicy, err := buildCSPPolicy(s.staticFS)
	if err != nil {
		return fmt.Errorf("build CSP: %w", err)
	}

	// Inside securityMiddleware, so only same-origin CSRF-checked requests are deduped,
	// and inside the access logger, so a replay is still logged.
	idem := newIdempotencyCache(idempotencyTTL)
	defer idem.stop()

	// webhttp.NewServer's defaults are what this app wants: ReadHeaderTimeout 10s,
	// IdleTimeout 120s, MaxHeaderBytes 1 MiB, and Read/WriteTimeout unset for the SSE,
	// WebSocket and streaming-zip responses. Server.MaxHeaderValueCount is deliberately
	// left at its default, and this is the one place that could set it: a 431 is answered
	// BELOW the middleware chain, so a cap tuned under a future proxy's header count
	// refuses requests with no access-log line, no request id and no client_ip.
	handler := webhttp.Chain(mux, s.middlewareStack(cspPolicy, idem)...)
	srv := webhttp.NewServer(handler)
	srv.Addr = ":" + port

	// Bound up front so port-in-use surfaces synchronously, and so /api/health reports
	// unready until the listener can genuinely accept.
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", srv.Addr)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	s.ready.Store(true)
	// AFTER the bind, so nothing downstream can read a failed bind as ownership.
	if s.onListen != nil {
		s.onListen()
	}
	// The bound ADDRESS, not the port constant: a misdirected boot's own output then says
	// which listener it got.
	slog.Info("Kiro Web UI listening", "addr", ln.Addr().String())
	// DNS rebinding rides the victim's BROWSER, so it reaches even a loopback bind, and
	// this HTTP surface carries no auth of its own: the Host allowlist is the whole gate.
	if !s.hostPolicy.Active() {
		slog.Warn("ALLOWED_HOSTS is unset or blank; any Host header is accepted, leaving DNS rebinding open even on loopback/private binds",
			"hint", "set ALLOWED_HOSTS to the exact hostnames/IPs you browse to (e.g. localhost,192.168.1.5,vibekit.example.com)")
	}

	// webhttp.Run owns the serve/shutdown sequence. The pre-drain hook is what keeps the
	// agent-before-server ordering: readiness flips, the runtime stops bridges and cancels
	// the SSE/WebSocket streams, and only then does the HTTP drain run.
	runErr := webhttp.Run(ctx, srv, ln, nil, webhttp.WithPreDrain(func(drainCtx context.Context) {
		slog.Info("received signal, shutting down", "cause", context.Cause(ctx))
		s.ready.Store(false)
		// Run calls this hook SYNCHRONOUSLY before srv.Shutdown, so an unbounded teardown
		// here would consume the whole grace: drainCtx is passed on, never discarded.
		if err := s.agent.Shutdown(drainCtx); err != nil {
			slog.Error("agent runtime shutdown did not finish within the grace period", "error", err)
		}
	}))
	s.ready.Store(false) // no-op on the signal path; covers a serve failure
	return runErr
}

// middlewareStack returns the middleware wrapping the route mux, OUTERMOST FIRST
// (webhttp.Chain's order): access logging, panic recovery, the security layer (dynamic CSP
// + ALLOWED_HOSTS + stdlib CSRF), the canonical-path gate, the REST idempotency dedup.
//
// A method rather than an inline literal so the ORDER is assertable without binding a
// port: that order is a security property, and a test hand-assembling the same list would
// only assert agreement with itself.
func (s *Server) middlewareStack(cspPolicy string, idem *idempotencyCache) []webhttp.Middleware {
	return []webhttp.Middleware{
		webhttp.Logging(
			// Skipped by path, or the long-lived stream logs one open-forever line.
			webhttp.WithSkipPaths("/api/events"),
			// The shell PTY is silenced by RESPONSE rather than by path: the record is
			// dropped only once the response left HTTP framing, so every handshake
			// REFUSAL keeps its line — which is what a reader needs when a browser
			// cannot attach a shell, and what the path skip deleted along with the noise.
			webhttp.WithSkipUpgrades(true),
			// /api/health is probed every 30s, so a healthy probe logs at Debug and only
			// a failing one is surfaced.
			webhttp.ProbeLogLevel("/api/health"),
			webhttp.WithClientIP(s.trustedProxies...),
		),
		webhttp.Recoverer(),
		func(next http.Handler) http.Handler { return securityMiddleware(cspPolicy, s.hostPolicy, next) },
		// INSIDE the host allowlist and the CSRF check, so their 403 is never shadowed by
		// a 400 about spelling; OUTSIDE the mux, because ServeMux canonicalizes before it
		// selects a pattern and no handler can be reached to refuse for itself; outside
		// the dedup cache, so a refused spelling mints no entry a retry would replay.
		canonicalAPIPath,
		idem.middleware,
	}
}

// requirePOST returns true if r.Method is POST, and otherwise writes 405 and returns false.
// Every command endpoint here is POST-only, so this saves an identical argument per site.
func requirePOST(w http.ResponseWriter, r *http.Request) bool {
	return httpreply.RequireMethod(w, r, http.MethodPost)
}

// decodeBody applies LimitBody, decodes JSON into v, and returns true on success. On
// failure it writes a 400 and returns false.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	return httpreply.DecodeBody(w, r, v, "bad request")
}

// healthBody is the readiness envelope handleHealth and handleKiroRescan both answer with,
// so an operator reads one shape from either surface. A struct, not a map, so the key order
// matches webhttp.ReadinessHandler's byte for byte — encoding/json sorts map keys, which is
// what made the shared-envelope claim false. This handler cannot BE ReadinessHandler: its
// verdict is composite where the library's ReadinessChecker is Ready() bool.
type healthBody struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// handleHealth returns the liveness+readiness status in the envelope shared across the
// fleet: 200 {"status":"ok"} when the listener is bound and serving, 503
// {"status":"unready",...} during startup, drain, or an unavailable kiro-cli.
//
// The kiro-cli verdict is the install manager's and is VERSION-AWARE, so a binary drifted
// from the pin reads unready rather than healthy. Reading it is a lock and two field reads,
// never a spawn, and it re-evaluates per request, so a completing install heals the signal
// with no restart. A READINESS signal: nothing restarts on the unhealthy state.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	// Never cached: under RFC 9111 a 200 with no explicit freshness is heuristically
	// cacheable, and a cached "ok" outliving the readiness it reported keeps traffic
	// arriving at an instance that has begun draining. (503 is not, so unready is safe.)
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
	// The kiro-cli leg stays FIRST: with no binary there is nothing to vend a token, so it
	// is the superset failure and the envelope carries one reason.
	if s.authUnavailable != nil && s.authUnavailable() {
		unready(reasonSignIn)
		return
	}
	webhttp.WriteJSON(w, healthBody{Status: "ok"})
}
