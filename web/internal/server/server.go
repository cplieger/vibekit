package server

import (
	"context"
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

	"vibekit/internal/api"
	"vibekit/internal/fileutil"
	"vibekit/internal/metrics"
	"vibekit/internal/permissions"

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

func WithSteering(g api.SteeringGenerator) Option  { return func(s *Server) { s.steering = g } }
func WithHub(h api.Hub) Option                     { return func(s *Server) { s.hub = h } }
func WithChats(c api.ChatStore) Option             { return func(s *Server) { s.chats = c } }
func WithGit(g api.GitHandler) Option              { return func(s *Server) { s.git = g } }
func WithGitAI(r api.RouteHandler) Option          { return func(s *Server) { s.gitAI = r } }
func WithFiles(f api.FileHandler) Option           { return func(s *Server) { s.files = f } }
func WithAuth(a api.AuthHandler) Option            { return func(s *Server) { s.auth = a } }
func WithPush(p api.PushService) Option            { return func(s *Server) { s.push = p } }
func WithMCPConfig(r api.RouteHandler) Option      { return func(s *Server) { s.mcpConfig = r } }
func WithMCPStatus(r api.RouteHandler) Option      { return func(s *Server) { s.mcpStatus = r } }
func WithMCPRegistry(r api.RouteHandler) Option    { return func(s *Server) { s.mcpRegistry = r } }
func WithForges(r api.RouteHandler) Option         { return func(s *Server) { s.forges = r } }
func WithRules(r *permissions.CommandRules) Option { return func(s *Server) { s.rules = r } }
func WithUtilityPrompt(p api.UtilityPrompter) Option {
	return func(s *Server) { s.utilityPrompt = p }
}
func WithStaticFS(staticFS fs.FS) Option {
	return func(s *Server) { s.staticFS = staticFS }
}
func WithCLIPath(p string) Option {
	return func(s *Server) {
		s.cliPath = p
		s.cliRunner = &execCLIRunner{cliPath: p}
	}
}
func WithConfigDir(d string) Option { return func(s *Server) { s.configDir = d } }
func WithWorkDir(d string) Option   { return func(s *Server) { s.workDir = d } }

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
	mux.HandleFunc("/metrics", metrics.Handler())

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           securityMiddleware(requestLogger(mux)),
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
