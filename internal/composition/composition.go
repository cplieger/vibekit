// Package composition wires all vibekit services together and manages application lifecycle.
package composition

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/cplieger/atomicfile/v2"
	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/auth"
	"github.com/cplieger/vibekit/internal/bridge"
	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/chat/archive"
	"github.com/cplieger/vibekit/internal/filehandler"
	forgesPkg "github.com/cplieger/vibekit/internal/forges"
	"github.com/cplieger/vibekit/internal/git"
	"github.com/cplieger/vibekit/internal/hub"
	"github.com/cplieger/vibekit/internal/kirosession"
	"github.com/cplieger/vibekit/internal/logctl"
	mcpPkg "github.com/cplieger/vibekit/internal/mcp"
	"github.com/cplieger/vibekit/internal/mcp/prewarm"
	pushPkg "github.com/cplieger/vibekit/internal/push"
	"github.com/cplieger/vibekit/internal/server"
	"github.com/cplieger/vibekit/internal/settings"
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
// passed by pointer to avoid copying the Config struct at every
// invocation (it's only ever built once from the environment, then
// mutated is forbidden — callers must treat it as read-only).
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
		return bridge.New(cfg.CLIPath, cfg.WorkDir)
	}

	mcpStore, err := mcpPkg.New(ctx, cfg.ConfigDir, nil)
	if err != nil {
		return nil, err
	}

	pushSvc := pushPkg.New(ctx, cfg.ConfigDir, cfg.VapidSub)

	// vibekit owns kiro-cli/KAS session cleanup end to end (cleanup.periodDays
	// pinned to 0/never): reap a chat's session state on delete, and orphans
	// via a periodic sweep that spares every active/archived chat's session.
	sessionReaper := kirosession.New(filepath.Join(workspace.KiroHome(), "sessions"))
	h := hub.New(cfg.WorkDir, bridgeFactory, chatStore,
		hub.WithConfigDir(cfg.ConfigDir), hub.WithMCPConfig(mcpStore), hub.WithPush(pushSvc),
		hub.WithSessionReaper(sessionReaper, chatStore.ReferencedSessionIDs))
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
		// v3 (KAS) has no live set_config_option for mcpServers; a bridge
		// picks up the new MCP set when it next (re)starts a session
		// (session/new and session/load both forward the current set).
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
	authHandler := auth.NewHandler(cfg.CLIPath,
		auth.WithConfig(cfg.AuthConfig),
		auth.WithTrustedProxies(cfg.TrustedProxies))
	forgesHTTP := forgesPkg.NewHTTPHandler(forgesManager, h)

	steer.SetForgeSnapshot(func() steering.ForgeSnapshot {
		return forgeSnapshot(ctx, forgesManager)
	})

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}

	retention := func() time.Duration {
		// vibekit owns retention (see settings.KeyChatRetentionDays). <= 0 is
		// "no purge": 0 = off (chats deleted on close, nothing to purge) and
		// -1 = forever (archived, never purged). N > 0 = purge after N days.
		days, ok := settings.Field[int](ctx, cfg.ConfigDir, settings.KeyChatRetentionDays, "chat_retention_days")
		if !ok {
			days = settings.DefaultChatRetentionDays
		}
		if days <= 0 {
			return 0
		}
		return time.Duration(days) * 24 * time.Hour
	}
	purgeScheduler := chat.NewPurgeScheduler(ctx, chatStore, retention)
	// Pre-archive teardown MUST run before the chat file moves so archiving
	// routes through the same bridge/in-memory teardown a delete performs
	// (minus the file removal + checkpoint reap) — no orphaned live bridge,
	// no stranded in-flight turn, no ghost .partial. See hub.OnChatArchiving.
	chat.WithPreArchive(func(id api.ChatID) {
		h.OnChatArchiving(id)
	})(chatStore)
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
		server.WithAccountUsage(h),
		server.WithPolicy(h),
		server.WithStaticFS(static),
		server.WithCLIPath(cfg.CLIPath),
		server.WithConfigDir(cfg.ConfigDir),
		server.WithWorkDir(cfg.WorkDir),
		server.WithTrustedProxies(cfg.TrustedProxies),
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
		if _, err := atomicfile.CleanupStaleTemps(dir, tempMaxAge); err != nil {
			slog.Debug("stale temp cleanup failed", "dir", dir, "error", err)
		}
	}
}

// forgeSnapshot builds the steering forge snapshot: one provider per
// configured forge, each enriched (best-effort) with its repo list.
// It is the body of the steer.SetForgeSnapshot callback, extracted so
// Build stays within the cognitive-complexity ceiling.
func forgeSnapshot(ctx context.Context, forgesManager *forgesPkg.Manager) steering.ForgeSnapshot {
	fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	configured := forgesManager.List(fctx)
	providers := make([]steering.ForgeProvider, 0, len(configured))
	for i := range configured {
		f := &configured[i]
		providers = append(providers, steering.ForgeProvider{
			Kind:  string(f.Kind),
			Host:  f.Host,
			User:  f.Username,
			Email: f.Email,
			Repos: repoNamesFor(ctx, f.Kind, f.Host),
		})
	}
	return steering.ForgeSnapshot{Providers: providers}
}

// repoNamesFor returns the full names of every repo reachable for the
// given forge, or nil if the provider can't be constructed or the
// listing fails. Enrichment is best-effort: a forge whose repos can't
// be listed is still surfaced, just without a repo list. A 5s timeout
// bounds the CLI call.
func repoNamesFor(ctx context.Context, kind forgesPkg.Kind, host string) []string {
	ops, err := forgesPkg.New(kind, host)
	if err != nil {
		return nil
	}
	repoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	repos, err := ops.ListRepos(repoCtx)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(repos))
	for i := range repos {
		names = append(names, repos[i].FullName)
	}
	return names
}
