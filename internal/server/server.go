package server

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/fileutil"
	"github.com/cplieger/vibekit/internal/permissions"
	"golang.org/x/sync/singleflight"
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
	modelsSF      singleflight.Group
	mcpStatus     api.RouteHandler
	utilityPrompt api.UtilityPrompter
	hub           api.Hub
	steering      api.SteeringGenerator
	mcpRegistry   api.RouteHandler
	staticFS      fs.FS
	cliRunner     CLIRunner
	rules         *permissions.CommandRules
	cliPath       string
	configDir     string
	workDir       string
	cliTimeouts   cliTimeouts
	settingsMu    sync.Mutex
	installing    atomic.Bool
	// ready flips to true once the listener binds and srv.Serve is
	// running; flips back to false on shutdown signal so /api/health
	// reports unready during drain. Same semantic across the homelab's
	// custom Go apps (subflux, plex-exporter, vibecli).
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

// WithRules sets the command rules store for per-command allow/deny evaluation.
func WithRules(r *permissions.CommandRules) Option { return func(s *Server) { s.rules = r } }

// WithUtilityPrompt sets the utility prompter used for AI-assisted tasks (rename, commit message, etc.).
func WithUtilityPrompt(p api.UtilityPrompter) Option {
	return func(s *Server) { s.utilityPrompt = p }
}

// WithStaticFS sets the embedded filesystem serving the compiled web UI.
func WithStaticFS(staticFS fs.FS) Option {
	return func(s *Server) { s.staticFS = staticFS }
}

// WithCLIPath sets the path to the kiro-cli binary used for CLI sub-operations.
func WithCLIPath(p string) Option {
	return func(s *Server) {
		s.cliPath = p
		s.cliRunner = &execCLIRunner{cliPath: p}
	}
}

// WithConfigDir sets the configuration directory path used for chat files and settings.
func WithConfigDir(d string) Option { return func(s *Server) { s.configDir = d } }

// WithWorkDir sets the workspace directory served by the file handler and git endpoints.
func WithWorkDir(d string) Option { return func(s *Server) { s.workDir = d } }

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
	s.hub.RegisterSlashRoutes(mux)
	mux.HandleFunc("/api/models", s.handleModels)
	mux.HandleFunc("/api/version", s.handleVersion)
	mux.HandleFunc("/api/diagnostics", s.handleDiagnostics)
	mux.HandleFunc("/api/kiro-settings", s.handleKiroSettings)
	s.chats.RegisterRoutes(mux)
	mux.HandleFunc("/api/health", s.handleHealth)
	s.auth.RegisterRoutes(mux)
	mux.HandleFunc("/api/steering", s.handleSteering)
	mux.HandleFunc("/api/tools/install", s.handleToolsInstall)
	mux.HandleFunc("GET /api/tools/status", s.handleToolStatus)
	mux.HandleFunc("POST /api/tools/{section}/{name}/enable", s.handleToolEnable)
	mux.HandleFunc("DELETE /api/tools/{section}/{name}", s.handleToolDelete)
	mux.HandleFunc("PATCH /api/tools/{section}/{name}", s.handleToolPatch)
	s.git.RegisterRoutes(mux)
	if s.gitAI != nil {
		s.gitAI.RegisterRoutes(mux)
	}
	s.files.RegisterRoutes(mux)
	fileutil.ServeJSONFile(mux, filepath.Join(s.configDir, "tools.json"), "{}", 0o644)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/workspace/kiro-config", s.handleKiroConfig)
	s.mcpConfig.RegisterRoutes(mux)
	s.mcpStatus.RegisterRoutes(mux)
	s.mcpRegistry.RegisterRoutes(mux)
	if s.forges != nil {
		s.forges.RegisterRoutes(mux)
	}
	mux.HandleFunc("/api/permissions/commands", s.handleCommandRules)
	mux.HandleFunc("/api/utility/explain-error", s.handleUtilityExplainError)
	mux.HandleFunc("/api/utility/resolve-conflict", s.handleUtilityResolveConflict)
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
	// requests are deduped) and inside requestLogger (so replays are
	// still access-logged). Its janitor goroutine is stopped via defer
	// when ListenAndServe returns — both the errCh and signal paths
	// below return, so the cache lives exactly as long as the server.
	idem := newIdempotencyCache(idempotencyTTL)
	defer idem.stop()

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           securityMiddleware(cspPolicy, requestLogger(idem.middleware(mux))),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MiB
	}

	// Bind listener up front so port-in-use surfaces synchronously
	// (rather than appearing in errCh after the goroutine launches).
	// Bind-then-flip-ready mirrors subflux/plex-exporter so /api/health
	// reports unready until the listener is genuinely ready to accept.
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", srv.Addr)
	if err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	s.ready.Store(true)
	slog.Info("Kiro Web UI listening", "port", port)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case err := <-errCh:
		s.ready.Store(false)
		return err
	case sig := <-sigCh:
		slog.Info("received signal, shutting down", "signal", sig)
		s.ready.Store(false)
		s.hub.Shutdown()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	}
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

// handleHealth returns the liveness+readiness status. Emits the
// canonical JSON envelope shared across the homelab's custom Go apps
// (vibekit, vibecli, subflux, registry-stats, plex-exporter): 200 with
// {"status":"ok"} when the listener is bound and serving; 503 with
// {"status":"unready",...} during startup or graceful shutdown drain.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	if !s.ready.Load() {
		api.WriteJSONStatus(w, http.StatusServiceUnavailable, map[string]string{
			"status": "unready",
			"reason": "starting up or shutting down",
		})
		return
	}
	api.WriteJSON(w, map[string]string{"status": "ok"})
}
