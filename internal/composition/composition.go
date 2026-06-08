package composition

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/atomicfile"
	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/auth"
	"github.com/cplieger/vibekit/internal/bridge"
	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/chat/archive"
	"github.com/cplieger/vibekit/internal/filehandler"
	forgesPkg "github.com/cplieger/vibekit/internal/forges"
	"github.com/cplieger/vibekit/internal/git"
	"github.com/cplieger/vibekit/internal/hub"
	"github.com/cplieger/vibekit/internal/logctl"
	mcpPkg "github.com/cplieger/vibekit/internal/mcp"
	"github.com/cplieger/vibekit/internal/mcp/prewarm"
	"github.com/cplieger/vibekit/internal/permissions"
	pushPkg "github.com/cplieger/vibekit/internal/push"
	"github.com/cplieger/vibekit/internal/server"
	"github.com/cplieger/vibekit/internal/sessions"
	"github.com/cplieger/vibekit/internal/steering"
	"github.com/cplieger/vibekit/internal/workspace"
)

// App holds all wired-up services for the vibekit server.
type App struct {
	Hub            *hub.Hub
	Server         *server.Server
	purgeScheduler *archive.PurgeScheduler
	mcpPrewarm     *prewarm.Runner
}

// Build constructs all services and wires them together. staticFS is
// the embedded filesystem containing the compiled web UI. cfg is
// passed by pointer to avoid copying the 112-byte Config struct at
// every invocation (it's only ever built once from the environment,
// then mutated is forbidden — callers must treat it as read-only).
func Build(ctx context.Context, cfg *Config, staticFS fs.FS) (*App, error) {
	// Instance guard: prevent two vibekit processes from running against
	// the same configDir (which would corrupt chat files). Uses flock so
	// the lock auto-releases on crash/SIGKILL without cleanup.
	if err := acquireInstanceLock(cfg.ConfigDir); err != nil {
		return nil, fmt.Errorf("another vibekit instance is running on %s: %w", cfg.ConfigDir, err)
	}

	if err := validateConfig(cfg); err != nil {
		return nil, fmt.Errorf("config validation failed:\n  %w", err)
	}

	logctl.Install(ctx, cfg.ConfigDir)

	steer := steering.New(cfg.WorkDir, cfg.ConfigDir)
	steer.Generate(ctx)

	lockMgr := sessions.New(workspace.KiroSessionsCLIDir())
	lockMgr.CleanupStale(ctx)

	// Wipe legacy shadow-git checkpoint directories.
	legacyCheckpoints := filepath.Join(cfg.ConfigDir, "checkpoints")
	if err := os.RemoveAll(legacyCheckpoints); err != nil {
		slog.Warn("legacy checkpoint wipe failed",
			"error", err, "path", legacyCheckpoints)
	}

	sweepStaleTemps(cfg.ConfigDir, cfg.WorkDir)

	chatStore, err := chat.NewStore(filepath.Join(cfg.ConfigDir, "chats"))
	if err != nil {
		return nil, err
	}

	bridgeFactory := func() api.ACPBridge {
		return bridge.New(cfg.CLIPath, cfg.WorkDir, bridge.WithSessionManager(lockMgr))
	}

	mcpStore, err := mcpPkg.New(ctx, cfg.ConfigDir, nil)
	if err != nil {
		return nil, err
	}

	pushSvc := pushPkg.New(ctx, cfg.ConfigDir, cfg.VapidSub)

	h := hub.New(cfg.WorkDir, bridgeFactory, chatStore, func() []string {
		return permissions.Args(ctx, cfg.ConfigDir)
	}, hub.WithConfigDir(cfg.ConfigDir), hub.WithMCPConfig(mcpStore), hub.WithPush(pushSvc))
	chat.WithBroadcaster(h)(chatStore)
	h.RecoverPartials()
	h.StartCheckpointBackgroundTasks()

	mcpRegistry := mcpPkg.NewRegistryProxy()
	mcpPrewarm := prewarm.NewRunner(ctx, mcpStore)
	mcpPrewarm.OnStatus = func(pkg string, state prewarm.State) {
		h.Broadcast(ctx, api.NewEvent(api.EventMCPPrewarm, "", api.MCPPrewarmPayload{
			Package: pkg,
			State:   string(state),
		}))
	}
	mcpStore.SetOnChange(func(ctx context.Context) {
		h.Broadcast(ctx, api.NewEvent(api.EventMCPConfigChanged, "", api.MCPConfigChangedPayload{}))
		mcpPrewarm.Run(ctx)
		h.PushMCPConfig()
	})
	mcpPrewarm.Run(ctx)

	steer.SetMCPSnapshot(func() steering.MCPSnapshot {
		return steering.MCPSnapshot{Servers: h.MCPSnapshot()}
	})
	h.SetMCPOnChange(func() { steer.Generate(h.ShutdownCtx()) })
	h.SetPreBridgeSpawn(func(ctx context.Context) { steer.Generate(ctx) })

	forgesManager := forgesPkg.NewManager()
	if refreshErr := forgesManager.Refresh(ctx); refreshErr != nil {
		// Non-fatal: refreshing CLI configs may fail if no CLIs are
		// installed yet. The manager starts with an empty list.
		_ = refreshErr
	}

	gitHandler := git.NewHandler(cfg.WorkDir)
	gitAIHandler := git.NewAIHandler(cfg.WorkDir, h)
	fileHandler, err := filehandler.New("/")
	if err != nil {
		return nil, err
	}
	authHandler := auth.NewHandler(cfg.CLIPath, auth.WithConfig(cfg.AuthConfig))
	forgesHTTP := forgesPkg.NewHTTPHandler(forgesManager, h)

	steer.SetForgeSnapshot(func() steering.ForgeSnapshot {
		fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		configured := forgesManager.List(fctx)
		providers := make([]steering.ForgeProvider, 0, len(configured))
		for i := range configured {
			f := &configured[i]
			p := steering.ForgeProvider{
				Kind:  string(f.Kind),
				Host:  f.Host,
				User:  f.Username,
				Email: f.Email,
			}
			// Best-effort: enumerate repos for this provider via the CLI.
			if ops, opsErr := forgesPkg.New(f.Kind, f.Host); opsErr == nil {
				repoCtx, repoCancel := context.WithTimeout(ctx, 5*time.Second)
				if repos, listErr := ops.ListRepos(repoCtx); listErr == nil {
					full := make([]string, 0, len(repos))
					for j := range repos {
						full = append(full, repos[j].FullName)
					}
					p.Repos = full
				}
				repoCancel()
			}
			providers = append(providers, p)
		}
		return steering.ForgeSnapshot{Providers: providers}
	})

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}

	retention := func() time.Duration {
		days := readCleanupPeriodDays(ctx, cfg.CLIPath)
		if days <= 0 {
			return 0
		}
		return time.Duration(days) * 24 * time.Hour
	}
	purgeScheduler := chat.NewPurgeScheduler(ctx, chatStore, retention)
	chat.WithOnArchive(func(id api.ChatID) {
		h.OnChatArchived(id)
		purgeScheduler.Trigger()
	})(chatStore)
	chat.WithOnPurge(func(chatID api.ChatID) {
		h.CleanupCheckpoints(ctx, chatID)
	})(chatStore)
	chat.WithOldestCheckpointFn(h.CheckpointOldestTag)(chatStore)
	purgeScheduler.Start()

	srv := server.New(
		server.WithSteering(steer),
		server.WithHub(h),
		server.WithChats(chatStore),
		server.WithGit(gitHandler),
		server.WithGitAI(gitAIHandler),
		server.WithFiles(fileHandler),
		server.WithAuth(authHandler),
		server.WithPush(pushSvc),
		server.WithMCPConfig(mcpStore),
		server.WithMCPStatus(h.MCPRegistry()),
		server.WithMCPRegistry(mcpRegistry),
		server.WithForges(forgesHTTP),
		server.WithRules(h.Rules()),
		server.WithUtilityPrompt(h),
		server.WithStaticFS(static),
		server.WithCLIPath(cfg.CLIPath),
		server.WithConfigDir(cfg.ConfigDir),
		server.WithWorkDir(cfg.WorkDir),
	)

	return &App{
		Hub:            h,
		Server:         srv,
		purgeScheduler: purgeScheduler,
		mcpPrewarm:     mcpPrewarm,
	}, nil
}

// Run starts the HTTP server and blocks until shutdown. The server
// handles signal-based graceful shutdown internally.
func (a *App) Run() error {
	err := a.Server.ListenAndServe()
	// Distinguish graceful shutdown from real failures.
	if errors.Is(err, http.ErrServerClosed) {
		slog.Info("HTTP server shut down cleanly")
		return nil
	}
	if err != nil {
		slog.Error("HTTP server", "error", err)
		a.Hub.Shutdown()
	}
	return err
}

// Shutdown stops background services in reverse order.
func (a *App) Shutdown() {
	a.purgeScheduler.Stop()
	a.mcpPrewarm.Stop()
	a.Hub.Shutdown()
}

// readCleanupPeriodDays shells out to `kiro-cli settings
// cleanup.periodDays` and returns the integer value. Any failure
// returns 0 so the scheduler falls back to "disabled".
func readCleanupPeriodDays(ctx context.Context, cliPath string) int {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, cliPath, "settings", "cleanup.periodDays").Output()
	if err != nil {
		slog.Warn("cleanup.periodDays: kiro-cli exec failed, retention disabled",
			"error", err, "cli_path", cliPath)
		return 0
	}
	raw := strings.TrimSpace(string(out))
	if i := strings.LastIndexByte(raw, '('); i > 0 {
		raw = strings.TrimSpace(raw[:i])
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		slog.Warn("cleanup.periodDays: parse failed, retention disabled",
			"error", err, "output", raw)
		return 0
	}
	if n < 0 {
		slog.Warn("cleanup.periodDays: negative value, retention disabled", "days", n)
		return 0
	}
	return n
}

// sweepStaleTemps removes orphan temp files left by SIGKILL between
// CreateTemp and Rename. Files younger than 1 hour are spared.
func sweepStaleTemps(configDir, workDir string) {
	const tempMaxAge = time.Hour
	for _, dir := range []string{
		configDir,
		filepath.Join(configDir, "chats"),
		filepath.Join(configDir, "chats", archive.Subdir),
		workDir,
	} {
		atomicfile.CleanupStaleTemps(dir, tempMaxAge)
	}
}
