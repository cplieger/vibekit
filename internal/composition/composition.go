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
	"reflect"
	"sync"
	"time"

	"github.com/cplieger/atomicfile/v2"
	"github.com/cplieger/toolbelt/v2"
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
	tools          *toolbelt.Engine
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
	warnIfCLIMissing(cfg)

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

	// Tools engine: the cplieger/toolbelt reconciler owns tools.json v2
	// + the install tree + the job queue; job lifecycle/output stream
	// over the hub's SSE via the Config callbacks. The seed plants the
	// disabled LSP + gh templates on fresh volumes (toggled on in
	// Settings -> Tools). Boot reconciles async — installed tools
	// persist on the volume, so nothing blocks server start.
	toolsEngine, err := buildToolsEngine(cfg, h)
	if err != nil {
		return nil, err
	}
	warnIfNoLSPEnabled(toolsEngine)
	// Installed-tool changes matter to the agent (kiro-cli scans PATH
	// for language servers); regenerate the steering env on each list
	// read is overkill, but forge logins need synchronous installs:
	forgesPkg.EnsureTool = toolsEngine.EnsureInstalled

	forgesManager := forgesPkg.NewManager()
	if refreshErr := forgesManager.Refresh(ctx); refreshErr != nil {
		// Non-fatal: refreshing CLI configs may fail if no CLIs are
		// installed yet. The manager starts with an empty list.
		_ = refreshErr
	}

	gitHandler := git.NewHandler(cfg.WorkDir)
	gitAIHandler := git.NewAIHandler(cfg.WorkDir, h)
	fileHandler, err := filehandler.New(cfg.BrowseRoots...)
	if err != nil {
		return nil, err
	}
	// Built-in editor saves fold into the owning chat's checkpoint
	// timeline (pre-write capture; see hub.CaptureEditorSave). Only
	// the editor's PUT /api/file path is captured — uploads, copies,
	// and shell writes stay outside the checkpoint contract.
	fileHandler.SetWriteObserver(h.CaptureEditorSave)
	authHandler := auth.NewHandler(cfg.CLIPath,
		auth.WithConfig(cfg.AuthConfig),
		auth.WithTrustedProxies(cfg.TrustedProxies))
	forgesHTTP := forgesPkg.NewHTTPHandler(forgesManager, h)

	// The forge snapshot shells out to the forge CLIs (gh repo list is a
	// network round trip, 5s cap per forge), and steering.Generate runs
	// synchronously on the pre-bridge-spawn critical path — so the
	// snapshot the generator reads is a never-blocking cache. It is
	// primed off the boot path and refreshed on forge changes and on a
	// TTL, each refresh regenerating environment.md when data changed.
	forgeCache := newForgeSnapshotCache(ctx, steer, func(bctx context.Context) steering.ForgeSnapshot {
		return forgeSnapshot(bctx, forgesManager)
	})
	steer.SetForgeSnapshot(forgeCache.snapshot)
	go forgeCache.refresh()
	forgesHTTP.SetOnChange(func() { go forgeCache.refresh() })

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
		server.WithTools(toolsEngine),
		server.WithUtilityPrompt(h),
		server.WithAccountUsage(h),
		server.WithPolicy(h),
		server.WithStaticFS(static),
		server.WithCLIPath(cfg.CLIPath),
		server.WithConfigDir(cfg.ConfigDir),
		server.WithWorkDir(cfg.WorkDir),
		server.WithTrustedProxies(cfg.TrustedProxies),
		server.WithHostPolicy(cfg.HostPolicy),
	)

	return &App{
		Hub:            h,
		Server:         srv,
		purgeScheduler: purgeScheduler,
		mcpPrewarm:     mcpPrewarm,
		tools:          toolsEngine,
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
	a.tools.Close()
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

// forgeSnapshotTTL bounds how stale the cached forge snapshot may get
// before a read kicks a background revalidation. Forge connections and
// repo lists change rarely (login, disconnect, repo created upstream);
// five minutes keeps the environment.md forge section honest without
// putting CLI network calls anywhere near the session-start path.
const forgeSnapshotTTL = 5 * time.Minute

// forgeSnapshotCache is a stale-while-revalidate cache around
// forgeSnapshot. snapshot() NEVER blocks on the forge CLIs: it returns
// the current cache immediately (zero-value before the boot prime
// lands, which omits the forge section for that one render) and kicks
// an async refresh when stale. refresh() rebuilds synchronously in the
// calling goroutine and regenerates the steering file when the
// snapshot actually changed, so logins/disconnects propagate to
// environment.md within one CLI round trip.
type forgeSnapshotCache struct {
	appCtx context.Context
	build  func(context.Context) steering.ForgeSnapshot
	steer  *steering.Generator
	at     time.Time
	snap   steering.ForgeSnapshot
	mu     sync.Mutex
	busy   bool
	dirty  bool // a refresh request arrived mid-rebuild; go again
}

// newForgeSnapshotCache wires a cache around build (the CLI-backed
// forgeSnapshot in production; injectable for tests) that regenerates
// steer on data changes.
func newForgeSnapshotCache(ctx context.Context, steer *steering.Generator,
	build func(context.Context) steering.ForgeSnapshot,
) *forgeSnapshotCache {
	return &forgeSnapshotCache{appCtx: ctx, build: build, steer: steer}
}

// snapshot returns the cached forge snapshot immediately; stale data
// triggers a background refresh (single-flight via busy). Safe on the
// pre-bridge-spawn critical path.
func (c *forgeSnapshotCache) snapshot() steering.ForgeSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) >= forgeSnapshotTTL && !c.busy {
		c.busy = true
		go c.rebuild()
	}
	return c.snap
}

// refresh rebuilds the snapshot now (network: forge CLI repo listings),
// then regenerates the steering file if the data changed. When a
// rebuild is already in flight, the request is coalesced into it via
// the dirty flag — an in-flight rebuild may have read pre-change CLI
// config, so dropping the request would strand a fresh login until the
// TTL. Callers run refresh in their own goroutine.
func (c *forgeSnapshotCache) refresh() {
	c.mu.Lock()
	if c.busy {
		c.dirty = true
		c.mu.Unlock()
		return
	}
	c.busy = true
	c.mu.Unlock()
	c.rebuild()
}

// rebuild does the actual snapshot rebuild + conditional regen, looping
// while coalesced refresh requests (dirty) are pending. Only entered
// with busy=true already claimed by the caller; clears it when done.
func (c *forgeSnapshotCache) rebuild() {
	for {
		snap := c.build(c.appCtx)
		c.mu.Lock()
		changed := !reflect.DeepEqual(c.snap, snap)
		c.snap = snap
		c.at = time.Now()
		again := c.dirty
		c.dirty = false
		c.busy = again
		c.mu.Unlock()
		// Regenerate only on change: Generate skips byte-identical
		// writes anyway, but skipping the render for the common
		// no-change TTL refresh avoids pointless workspace scans.
		if changed {
			c.steer.Generate(c.appCtx)
		}
		if !again {
			return
		}
	}
}

// forgeSnapshot builds the steering forge snapshot: one provider per
// configured forge, each enriched (best-effort) with its repo list.
// It is the body of the forgeSnapshotCache rebuild, extracted so
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

// warnIfNoLSPEnabled logs the code-intelligence nudge when no
// language-server entry is enabled: kiro-cli scans PATH for language
// servers at session start, so a fresh box without one silently lacks
// code intelligence. Detection uses the inventory's catalog-derived
// Lsp marker, so any enabled LSP (seeded template or hand-added)
// silences it.
// buildToolsEngine constructs the shared toolbelt engine with vibekit's
// SSE adapters and enqueues the boot jobs: the full reconcile first,
// then the boot catalog fetch (the engine's schedule has no
// fire-on-start; an immediate enqueue inside New would land ahead of
// boot-critical work on the single-flight queue). Failures to enqueue
// are logged, not fatal: installed tools persist on the volume and
// keep-last-good absorbs an unreachable publisher.
func buildToolsEngine(cfg *Config, h *hub.Hub) (*toolbelt.Engine, error) {
	catalogRefresh := &toolbelt.CatalogRefresh{
		URL:      cfg.ToolCatalogURL,
		Require:  cfg.ToolCatalogRequire,
		Interval: cfg.ToolCatalogRefresh,
	}
	toolsEngine, err := toolbelt.New(&toolbelt.Config{
		ConfigDir:       cfg.ConfigDir,
		ToolsDir:        cfg.ToolsDir,
		CatalogPath:     cfg.ToolCatalogPath,
		Refresh:         catalogRefresh,
		CatalogOverlays: cfg.ToolCatalogOverlays,
		Seed:            toolbelt.DefaultSeed(),
		System:          []string{"git", "jq", "curl", "unzip", "xz", "ssh", "tar", "bash"},
		OnJobChanged: func(j *toolbelt.Job) {
			h.Broadcast(context.Background(), api.NewEvent(api.EventToolJobChanged, "",
				api.ToolJobChangedPayload{Job: j}))
			// A finished install/reconcile may have just produced the
			// first enabled language server: activate workspace code
			// intelligence (idempotent; no-op while lsp.json exists or
			// no lsp tool is enabled+installed). Async — job callbacks
			// must not block (fired under the queue lock).
			if j != nil && j.State == toolbelt.JobDone {
				go h.EnsureCodeIntelligence(context.Background())
			}
		},
		OnJobOutput: func(jobID string, lines []string) {
			h.Broadcast(context.Background(), api.NewEvent(api.EventToolJobOutput, "",
				api.ToolJobOutputPayload{JobID: jobID, Lines: lines}))
		},
	})
	if err != nil {
		return nil, fmt.Errorf("tools engine: %w", err)
	}
	if _, rerr := toolsEngine.Reconcile(toolbelt.ReconcileFull); rerr != nil {
		slog.Warn("tools: boot reconcile not enqueued", "error", rerr)
	}
	if _, rerr := toolsEngine.RefreshCatalog(); rerr != nil {
		slog.Warn("tools: boot catalog refresh not enqueued", "error", rerr)
	}
	// Code-intelligence activation (hub/code_intel.go): the gate scans
	// the live inventory for an enabled+installed language server, and
	// the boot fire covers the volume that already has servers but no
	// lsp.json (first deploy of this feature, or a deleted config).
	// Later fires ride the job callback above.
	h.SetCodeIntelligence(filepath.Join(cfg.WorkDir, ".kiro", "settings", "lsp.json"), func() bool {
		inv, ierr := toolsEngine.Inventory()
		if ierr != nil {
			return false
		}
		for i := range inv.Tools {
			if inv.Tools[i].Lsp && !inv.Tools[i].Disabled && inv.Tools[i].Installed {
				return true
			}
		}
		return false
	})
	go h.EnsureCodeIntelligence(context.Background())
	return toolsEngine, nil
}

func warnIfNoLSPEnabled(e *toolbelt.Engine) {
	inv, err := e.Inventory()
	if err != nil {
		return
	}
	for i := range inv.Tools {
		if inv.Tools[i].Lsp && !inv.Tools[i].Disabled {
			return
		}
	}
	slog.Warn("no language servers enabled; kiro code intelligence will be limited",
		"hint", "enable gopls (Go), typescript-language-server (TypeScript), or pyright (Python) in Settings -> Tools")
}
