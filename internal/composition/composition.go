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

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/toolbelt/v3"
	"github.com/cplieger/vibekit/internal/agent"
	"github.com/cplieger/vibekit/internal/auth"
	"github.com/cplieger/vibekit/internal/bridge"
	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/chat/archive"
	"github.com/cplieger/vibekit/internal/filebrowse"
	"github.com/cplieger/vibekit/internal/forges"
	"github.com/cplieger/vibekit/internal/git"
	"github.com/cplieger/vibekit/internal/kirosession"
	"github.com/cplieger/vibekit/internal/logctl"
	"github.com/cplieger/vibekit/internal/mcp"
	"github.com/cplieger/vibekit/internal/mcp/prewarm"
	"github.com/cplieger/vibekit/internal/push"
	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/schedule"
	"github.com/cplieger/vibekit/internal/server"
	"github.com/cplieger/vibekit/internal/settings"
	"github.com/cplieger/vibekit/internal/steering"
	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/vibekit/internal/workspace"
)

// App holds all wired-up services for the vibekit server.
type App struct {
	Runtime        *agent.Runtime
	Server         *server.Server
	purgeScheduler *archive.PurgeScheduler
	mcpPrewarm     *prewarm.Runner
	tools          *toolbelt.Engine
	// stopKiro cancels the background kiro-cli install, so a shutdown during a
	// first-boot download or a retry backoff does not wait it out.
	stopKiro func()
	// stopOrphanSweep stops the boot orphan sweep and WAITS: a sweep in flight issues
	// one `inspect` per lease over the utility bridge the teardown below is about to close.
	stopOrphanSweep func()
	// stopPRPoller stops the PR-status poller and waits; nothing else stops it.
	stopPRPoller func()
	// stopApp ends the app's LIFETIME: the context every component that must die with
	// the process is parented on, and the one agent.New requires.
	stopApp func()
}

// Build constructs all services and wires them together. cfg is READ-ONLY: it is built
// once from the environment and must never be mutated after.
func Build(ctx context.Context, cfg *Config, staticFS fs.FS) (*App, error) {
	// Two processes on one configDir would corrupt chat files. flock, so the lock
	// auto-releases on crash or SIGKILL with no cleanup.
	if err := acquireInstanceLock(cfg.ConfigDir); err != nil {
		return nil, fmt.Errorf("another vibekit instance is running on %s: %w", cfg.ConfigDir, err)
	}

	if err := validateConfig(ctx, cfg); err != nil {
		return nil, fmt.Errorf("config validation failed:\n  %w", err)
	}

	// The app's lifetime. Build's ctx is context.Background() in production, so it can
	// never end; appCtx is the cancellable child every component whose work must not
	// outlive the process is parented on, derived HERE so the lifetime flows outward.
	appCtx, stopApp := context.WithCancel(ctx)
	// A boot that returns no App is the one case nothing can call App.Shutdown,
	// including the (nil, nil) degraded verdict below and not just the error returns.
	built := false
	defer cancelUnless(&built, stopApp)

	logctl.Install(ctx, cfg.ConfigDir)

	// The three paths a boot's blast radius derives from, on one line; otherwise a boot
	// pointed at the wrong one is diagnosable only by reading which envs vibekit consults.
	// KIRO_HOME decides whose KAS session trees a sweep may delete and does NOT follow the
	// config dir. AFTER Install, or it bypasses logfmt.
	slog.Info("boot paths resolved",
		"config_dir", cfg.ConfigDir, "work_dir", cfg.WorkDir, "kiro_home", workspace.KiroHome())

	// Backgrounded on purpose: the listener binds first and only readiness waits, so a
	// first-boot download is an unready verdict rather than a missing server. BELOW
	// Install because its two early returns log synchronously and would bypass logfmt.
	kiro := startKiroCLI(ctx, cfg)

	steer := steering.New(cfg.WorkDir, cfg.ConfigDir)
	steer.Generate(ctx)

	// Wipe legacy shadow-git checkpoint directories.
	legacyCheckpoints := filepath.Join(cfg.ConfigDir, "checkpoints")
	if err := os.RemoveAll(legacyCheckpoints); err != nil {
		slog.Warn("legacy checkpoint wipe failed",
			"error", err, "path", legacyCheckpoints)
	}

	sweepStaleTemps(ctx, cfg.ConfigDir, cfg.WorkDir)

	chatStore, err := chat.NewStore(filepath.Join(cfg.ConfigDir, "chats"))
	if err != nil {
		return nil, err
	}

	// Resolved per SPAWN: resolving once per process would pin every chat to whatever
	// version was installed first.
	bridgeFactory := func() agent.ACPBridge {
		return bridge.New(kiro.cliPath(), cfg.WorkDir,
			bridge.WithEnv(kiro.env()), bridge.WithEnvAllow(cfg.BridgeEnvAllow))
	}

	mcpStore, err := mcp.New(appCtx, cfg.ConfigDir, nil)
	if err != nil {
		return nil, err
	}

	scheduleStore := openScheduleStore(cfg.ConfigDir)
	leaseStore := openRunLeaseStore(cfg.ConfigDir)

	pushSvc := push.New(appCtx, cfg.ConfigDir, cfg.VapidSub)

	// The second argument is WHO this reaper answers for, and only the workspace root
	// is correct — see vibekit-runtime.md, "What the reaper may delete".
	sessionReaper := kirosession.New(filepath.Join(workspace.KiroHome(), "sessions"), cfg.WorkDir)
	// Closed by the server once its listener has bound; the destructive session sweep
	// waits on it. Created here because the runtime is built before the server.
	listenerBound := make(chan struct{})
	tabStore := openTabStore(cfg.ConfigDir)
	h := agent.New(appCtx, cfg.WorkDir, bridgeFactory, chatStore,
		agent.WithConfigDir(cfg.ConfigDir), agent.WithMCPConfig(mcpStore), agent.WithPush(pushSvc),
		agent.WithACPArgs(cfg.ACPArgs),
		agent.WithKiroCLIPath(kiro.cliPath, kiro.env),
		agent.WithSessionReaper(sessionReaper, chatStore.ReferencedSessionIDs),
		agent.WithSessionSweepGate(listenerBound),
		agent.WithSchedules(scheduleStore),
		agent.WithRunLeases(leaseStore),
		agent.WithTabs(tabStore))
	chat.WithBroadcaster(h)(chatStore)
	pruneTabs(ctx, tabStore, chatStore)

	// BEFORE anything can launch: relying on the scheduler's first tick would make
	// correctness a property of the tick interval. On the APP's lifetime, never Build's,
	// or the sweep outlives App.Shutdown; backgrounded because a boot must not wait.
	stopOrphanSweep := startOrphanSweep(appCtx, h.Runs().SweepOrphaned, kiro.installed)

	startScheduleRunner(appCtx, scheduleStore, h.Runs())

	mcpRegistry := mcp.NewRegistryProxy()
	mcpPrewarm := prewarm.NewRunner(appCtx, mcpStore)
	mcpPrewarm.OnStatus = func(pkg string, state prewarm.State) {
		h.Broadcast(ctx, vibekit.NewEvent(vibekit.EventMCPPrewarm, "", vibekit.MCPPrewarmPayload{
			Package: pkg,
			State:   string(state),
		}))
	}
	mcpStore.SetOnChange(func(ctx context.Context) {
		h.Broadcast(ctx, vibekit.NewEvent(vibekit.EventMCPConfigChanged, "", vibekit.MCPConfigChangedPayload{}))
		mcpPrewarm.Run(ctx)
		// No bridge restart and nothing to forward: the persist renders KAS's own config
		// file, whose watcher reconnects in place, so a change reaches every LIVE session.
	})
	mcpPrewarm.Run(ctx)

	steer.SetMCPSnapshot(func() steering.MCPSnapshot {
		return steering.MCPSnapshot{Servers: h.MCPSnapshot()}
	})
	h.SetMCPOnChange(func() { steer.Generate(appCtx) })
	h.SetPreBridgeSpawn(func(ctx context.Context) { steer.Generate(ctx) })

	// The engine owns the manifest, the install tree and the queue; this root owns wiring.
	toolsEngine, err := wireToolsEngine(appCtx, cfg, h)
	if err != nil {
		return nil, err
	}

	forgesManager := forges.NewManager()
	if refreshErr := forgesManager.Refresh(ctx); refreshErr != nil {
		// Non-fatal with no CLIs installed yet; the manager starts with an empty list.
		_ = refreshErr
	}

	gitHandler := git.NewHandler(cfg.WorkDir)
	gitAIHandler := git.NewAIHandler(cfg.WorkDir, h)
	fileHandler, err := filebrowse.New(cfg.BrowseRoots...)
	if err != nil {
		return nil, err
	}
	authHandler := auth.NewHandler(kiro.cliPath,
		auth.WithConfig(cfg.AuthConfig),
		auth.WithTrustedProxies(cfg.TrustedProxies))
	forgesHTTP := forges.NewHTTPHandler(forgesManager, h)

	// A cache, because steering.Generate runs synchronously on the pre-bridge-spawn path
	// and forgeSnapshot shells out to the forge CLIs.
	forgeCache := newForgeSnapshotCache(appCtx, steer, func(bctx context.Context) steering.ForgeSnapshot {
		return forgeSnapshot(bctx, forgesManager)
	})
	steer.SetForgeSnapshot(forgeCache.snapshot)
	go forgeCache.refresh()
	forgesHTTP.SetOnChange(func() { go forgeCache.refresh() })

	stopPRPoller := startPRStatusPoller(ctx, forgesManager, gitHandler, pushSvc)

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}

	retention := func() time.Duration { return chatRetention(ctx, cfg.ConfigDir) }
	purgeScheduler := chat.NewPurgeScheduler(chatStore, retention)
	// The purge scans the SAME directory live chats live in, so this predicate is what
	// keeps an old-but-open conversation out of it.
	chat.WithLive(h.HasLiveBridge)(chatStore)
	// A chat someone has OPEN is not abandoned work, bridge or draft or neither. That
	// makes retention opt-out for a chat left open forever, which is accepted.
	chat.WithOpenTab(h.Membership().HasOpenTab)(chatStore)
	// Not a retention predicate: the chat store's HTTP surface reads it, and without it
	// that surface's silence about a buffered turn reads as "nothing closed this turn".
	// Injected post-construction for WithLive's reason — the store cannot import the agent.
	chat.WithTurnOpen(h.HasOpenTurn)(chatStore)
	chat.WithOnPurge(func(id vibekit.ChatID, sessionChain []string) {
		for _, sid := range sessionChain {
			sessionReaper.Reap(sid)
		}
		// After the per-chat record lock is released: it keeps the lock order acyclic.
		h.Membership().RetentionClose(appCtx, id)
	})(chatStore)
	purgeScheduler.Start(appCtx)

	srv := server.New(
		server.WithSteering(steer),
		server.WithAgent(h),
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
		server.WithPolicy(h.Config()),
		// The recycle a security-profile change needs, or the policy view describes the
		// profile that was in force before it.
		server.WithPolicyReload(h),
		server.WithStaticFS(static),
		server.WithCLIPath(kiro.cliPath),
		server.WithKiroReady(kiro.ready),
		server.WithKiroRescan(kiro.rescan),
		server.WithAuthUnavailable(h.AuthTokenUnavailable),
		server.WithConfigDir(cfg.ConfigDir),
		server.WithTabs(tabStore),
		server.WithWorkDir(cfg.WorkDir),
		server.WithTrustedProxies(cfg.TrustedProxies),
		server.WithHostPolicy(cfg.HostPolicy),
		// ListenAndServe is callable twice and a second close panics.
		server.WithOnListen(sync.OnceFunc(func() { close(listenerBound) })),
	)

	built = true
	return &App{
		Runtime:         h,
		Server:          srv,
		purgeScheduler:  purgeScheduler,
		mcpPrewarm:      mcpPrewarm,
		tools:           toolsEngine,
		stopKiro:        kiro.stop,
		stopOrphanSweep: stopOrphanSweep,
		stopPRPoller:    stopPRPoller,
		stopApp:         stopApp,
	}, nil
}

// Run starts the HTTP server and blocks until shutdown, which the server handles itself.
func (a *App) Run() error {
	err := a.Server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		slog.Info("HTTP server shut down cleanly")
		return nil
	}
	if err != nil {
		slog.Error("HTTP server", "error", err)
		a.shutdownHub()
	}
	return err
}

// Shutdown stops background services in reverse order. Every member is treated as
// optional — one genuinely is (tools is nil on the degraded boot) — because a panic on a
// service that was never started takes the teardown of the ones that WERE down with it.
func (a *App) Shutdown() {
	// First: the poller consults the push service the Runtime owns.
	callIfSet(a.stopPRPoller)
	// Before stopKiro because this stop WAITS: a sweep reaches KAS over the utility bridge
	// the kiro teardown is about to close, so the reverse order leaves one mid-inspect.
	callIfSet(a.stopOrphanSweep)
	callIfSet(a.stopKiro)
	if a.purgeScheduler != nil {
		a.purgeScheduler.Stop()
	}
	if a.mcpPrewarm != nil {
		a.mcpPrewarm.Stop()
	}
	if a.tools != nil {
		a.tools.Close()
	}
	// Immediately BEFORE the runtime's teardown and no earlier: sooner would signal the
	// push service's Done while the poller that consults it is still running.
	callIfSet(a.stopApp)
	a.shutdownHub()
}

// hubStopGrace bounds the runtime teardown App.Shutdown owns. Invented rather than
// inherited because the signal context is already cancelled on both paths that reach
// here, so a derived budget would be zero. 10s is the runtime's PTY teardown ceiling.
const hubStopGrace = 10 * time.Second

// shutdownHub tears the runtime down on that budget and LOGS an expiry: both callers are
// terminal paths with nobody above them to return an error to.
func (a *App) shutdownHub() {
	if a.Runtime == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), hubStopGrace)
	defer cancel()
	if err := a.Runtime.Shutdown(ctx); err != nil {
		slog.Error("agent runtime shutdown did not finish within the grace period",
			"grace", hubStopGrace, "error", err)
	}
}

// callIfSet runs fn when it is set; App's function members have no nil-safe receiver.
func callIfSet(fn func()) {
	if fn != nil {
		fn()
	}
}

// cancelUnless calls cancel unless *built is true, read at call time through the pointer.
// Named rather than a closure so it does not count against Build's complexity ceiling.
func cancelUnless(built *bool, cancel context.CancelFunc) {
	if !*built {
		cancel()
	}
}

// chatRetention resolves the purge window, read on every pass. <= 0 is "no purge": 0 =
// off, -1 = forever, N > 0 = purge after N days. FieldStrict, so a config.json that is
// PRESENT and unreadable answers 0 rather than the default: folding the two would let a
// malformed file override a stored -1. An ABSENT key or file keeps the default, which must
// stay lenient — a fresh install has no config.json.
func chatRetention(ctx context.Context, configDir string) time.Duration {
	days, ok, err := settings.FieldStrict[int](ctx, configDir, settings.KeyChatRetentionDays)
	if err != nil {
		slog.Error("chat retention: config.json is present but unreadable; purged nothing this pass rather than applying the default window",
			"key", settings.KeyChatRetentionDays,
			"default_days", settings.DefaultChatRetentionDays, "error", err)
		return 0
	}
	if !ok {
		days = settings.DefaultChatRetentionDays
	}
	if days <= 0 {
		return 0
	}
	return time.Duration(days) * 24 * time.Hour
}

// sweepStaleTemps removes orphan temps left by SIGKILL between CreateTemp and Rename,
// sparing anything under an hour old. configDir is swept RECURSIVELY, so a new atomic
// writer needs no entry on a hand-kept list; workDir is FLAT, since temps only land at its
// top level. Failed and Unreadable are reported APART: they are different problems.
func sweepStaleTemps(ctx context.Context, configDir, workDir string) {
	const tempMaxAge = time.Hour
	for _, sweep := range []struct {
		dir  string
		opts []atomicfile.Option
	}{
		{configDir, []atomicfile.Option{atomicfile.WithRecursive(true)}},
		{workDir, nil},
	} {
		res, err := atomicfile.CleanupStaleTemps(ctx, sweep.dir, tempMaxAge, sweep.opts...)
		if err != nil {
			slog.Debug("stale temp cleanup failed", "dir", sweep.dir, "error", err)
		}
		if res.Failed > 0 {
			slog.Warn("stale temp cleanup could not reclaim every orphan; they are accumulating on the volume",
				"dir", sweep.dir, "failed", res.Failed,
				"hint", "check ownership and mode on the paths logged at debug level")
		}
		if res.Unreadable > 0 {
			slog.Warn("stale temp cleanup could not enter every subdirectory; it may be hiding uncounted orphans",
				"dir", sweep.dir, "unreadable", res.Unreadable)
		}
		slog.Debug("stale temp cleanup done", "dir", sweep.dir, "removed", res.Removed)
	}
}

// forgeSnapshotTTL bounds how stale the cached forge snapshot may get before a read kicks
// a background revalidation. Connections and repo lists change rarely, so five minutes
// keeps the forge section honest with no CLI network call near the session-start path.
const forgeSnapshotTTL = 5 * time.Minute

// forgeSnapshotCache is a stale-while-revalidate cache around forgeSnapshot. snapshot()
// NEVER blocks on the forge CLIs: it returns the current cache (zero-value before the boot
// prime lands) and kicks an async refresh when stale. refresh() rebuilds in the calling
// goroutine and regenerates the steering file only on a change.
type forgeSnapshotCache struct {
	// appCtx is the app's lifetime, required at construction: both entry points are
	// context-free callbacks, and both reach rebuild, which must not outlive the process.
	appCtx context.Context
	build  func(context.Context) steering.ForgeSnapshot
	steer  *steering.Generator
	at     time.Time
	snap   steering.ForgeSnapshot
	mu     sync.Mutex
	busy   bool
	dirty  bool // a refresh request arrived mid-rebuild; go again
}

// newForgeSnapshotCache wires a cache around build that regenerates steer on a change.
// ctx is the app's lifetime and is required; see the appCtx field.
func newForgeSnapshotCache(ctx context.Context, steer *steering.Generator,
	build func(context.Context) steering.ForgeSnapshot,
) *forgeSnapshotCache {
	return &forgeSnapshotCache{appCtx: ctx, build: build, steer: steer}
}

// snapshot returns the cached forge snapshot immediately, kicking a single-flight
// background refresh when stale. Safe on the pre-bridge-spawn critical path.
func (c *forgeSnapshotCache) snapshot() steering.ForgeSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) >= forgeSnapshotTTL && !c.busy {
		c.busy = true
		go c.rebuild()
	}
	return c.snap
}

// refresh rebuilds the snapshot now, then regenerates the steering file if the data
// changed. A request arriving mid-rebuild is COALESCED rather than dropped: that rebuild
// may have read pre-change CLI config, so dropping it would strand a fresh login until the
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

// rebuild does the rebuild and conditional regen, looping while coalesced requests are
// pending. Entered only with busy already claimed by the caller; clears it when done.
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
		// Generate skips byte-identical writes anyway; skipping the RENDER for the common
		// no-change TTL refresh is what avoids a pointless workspace scan.
		if changed {
			c.steer.Generate(c.appCtx)
		}
		if !again {
			return
		}
	}
}

// forgeSnapshot builds the steering forge snapshot: one provider per configured forge,
// each enriched best-effort with its repo list.
func forgeSnapshot(ctx context.Context, forgesManager *forges.Manager) steering.ForgeSnapshot {
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

// repoNamesFor returns every repo reachable for the given forge, or nil. Best-effort: a
// forge whose repos cannot be listed is still surfaced, just without a repo list.
func repoNamesFor(ctx context.Context, kind forges.Kind, host string) []string {
	ops, err := forges.New(kind, host)
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

// wireToolsEngine builds the tools engine and, when the root is intact, wires its
// consumers. A nil engine is the degraded verdict rather than an error, and the dependent
// wiring is SKIPPED whole rather than nil-guarded: no toolbelt method is nil-safe.
func wireToolsEngine(appCtx context.Context, cfg *Config, h *agent.Runtime) (*toolbelt.Engine, error) {
	toolsEngine, err := buildToolsEngine(appCtx, cfg, h)
	if err != nil {
		return nil, err
	}
	if toolsEngine != nil {
		warnIfNoLSPEnabled(toolsEngine)
		// A forge login needs its CLI installed synchronously.
		forges.EnsureTool = toolsEngine.EnsureInstalled
	}
	return toolsEngine, nil
}

// buildToolsEngine constructs the shared toolbelt engine with vibekit's SSE adapters and
// enqueues the boot jobs, reconcile first; a failed enqueue is logged rather than fatal
// because installed tools persist on the volume. (nil, nil) is the root-integrity DEGRADED
// verdict, not an omission — every other New failure still stops the boot.
func buildToolsEngine(appCtx context.Context, cfg *Config, h *agent.Runtime) (*toolbelt.Engine, error) {
	catalogRefresh := &toolbelt.CatalogRefresh{
		URL:      cfg.ToolCatalogURL,
		Require:  cfg.ToolCatalogRequire,
		Interval: cfg.ToolCatalogRefresh,
	}
	toolsEngine, err := toolbelt.New(&toolbelt.Config{
		ConfigDir: cfg.ConfigDir,
		ToolsDir:  cfg.ToolsDir,
		// The engine EXECUTES what it finds here and this dir leads PATH, over a volume
		// the operator reshapes by hand. The refusal must NOT be fatal — see below.
		VerifyRootIntegrity: true,
		CatalogPath:         cfg.ToolCatalogPath,
		Refresh:             catalogRefresh,
		CatalogOverlays:     cfg.ToolCatalogOverlays,
		Seed:                toolbelt.DefaultSeed(),
		System:              []string{"git", "jq", "curl", "unzip", "xz", "ssh", "tar", "bash"},
		OnJobChanged: func(j *toolbelt.Job) {
			h.Broadcast(context.Background(), vibekit.NewEvent(vibekit.EventToolJobChanged, "",
				vibekit.ToolJobChangedPayload{Job: j}))
			// Async because a job callback fires under the queue lock and must not
			// block; the call itself is idempotent.
			if j != nil && j.State == toolbelt.JobDone {
				// The app's lifetime, not Background: this goroutine writes lsp.json,
				// and a Background parent let SIGTERM abandon it mid-write.
				go h.EnsureCodeIntelligence(appCtx)
			}
		},
		OnJobOutput: func(jobID string, lines []string) {
			h.Broadcast(context.Background(), vibekit.NewEvent(vibekit.EventToolJobOutput, "",
				vibekit.ToolJobOutputPayload{JobID: jobID, Lines: lines}))
		},
	})
	if err != nil {
		// Both answers travel in the error: nil from the classifier IS the degraded
		// verdict, so this one return yields (nil, nil) or (nil, wrapped).
		return nil, toolsEngineFailure(err)
	}
	if _, _, rerr := toolsEngine.Reconcile(toolbelt.ReconcileFull); rerr != nil {
		slog.Warn("tools: boot reconcile not enqueued", "error", rerr)
	}
	if _, rerr := toolsEngine.RefreshCatalog(); rerr != nil {
		slog.Warn("tools: boot catalog refresh not enqueued", "error", rerr)
	}
	// The gate agent/code_intel.go consults; the boot fire below covers a volume that
	// already has servers but no lsp.json, later fires ride the job callback above.
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
	// The app's lifetime, not Background — see the OnJobChanged spawn above.
	go h.EnsureCodeIntelligence(appCtx)
	return toolsEngine, nil
}

// toolsEngineFailure decides what a toolbelt.New failure costs vibekit. A nil return is
// the DEGRADED verdict and only the root-integrity refusal earns it; every other failure
// stays fatal. Degraded rather than fatal because an unfit root is persistent-volume state
// this process cannot repair, and refusing to boot removes the only way in (invariant 6).
func toolsEngineFailure(err error) error {
	if !errors.Is(err, toolbelt.ErrRootIntegrity) {
		return fmt.Errorf("tools engine: %w", err)
	}
	logRootIntegrityRefusal(err)
	return nil
}

// logRootIntegrityRefusal reports a refusal one line per offending path, then states the
// consequence; a path is what an operator can grep and act on. Deliberately does NOT touch
// /api/health: that verdict is the install manager's, and this condition never self-heals,
// so wiring it in would report the container unready forever with no repair path.
func logRootIntegrityRefusal(err error) {
	refusal, ok := errors.AsType[*toolbelt.RootIntegrityError](err)
	if !ok {
		// Sentinel-classified but not carrying the type: report it all rather than nothing.
		slog.Error("tools engine disabled: a managed root failed the integrity check", "error", err)
		return
	}
	for _, f := range refusal.Findings {
		slog.Error("tools: managed root is not fit to execute from",
			"path", f.Path, "reason", f.Reason)
	}
	slog.Warn("tools engine disabled: vibekit is running without the tools subsystem; "+
		"Settings -> Tools is unavailable and forge CLIs will not auto-install",
		"finding_count", len(refusal.Findings),
		"hint", "the check reports only and never repairs: fix the paths above from inside the container "+
			"(chmod g-w,o-w on a writable dir; replace a symlinked root with a real directory), then restart it")
}

// warnIfNoLSPEnabled nudges when no language server is enabled: kiro-cli scans PATH at
// session start, so a box without one silently lacks code intelligence.
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

// openScheduleStore opens the workflow-schedule store, or nil to leave scheduling off. A
// malformed schedules.json warns rather than aborting boot (invariant 6: a bad file on the
// volume must still leave a way IN to fix it).
func openScheduleStore(dir string) *schedule.Store {
	st, err := schedule.NewStore(dir)
	if err != nil {
		slog.Warn("workflow scheduling disabled", "error", err)
		return nil
	}
	return st
}

// openTabStore opens the open-tab set, ALWAYS returning a store: an arrangement is
// re-derivable by opening the tabs again (invariant 6), and no store would take the four
// tab commands down with it, so nothing could reopen anything.
func openTabStore(dir string) *tabs.Store {
	st, err := tabs.NewStore(dir)
	if err != nil {
		slog.Warn("tab arrangement starting empty", "error", err)
	}
	return st
}

// pruneTabs is the tab set's LOAD-TIME crash recovery, running exactly ONCE: the membership
// coordinator is the live mechanism, so this covers only a crash between its two writes.
// Per KIND — a CHAT tab is checked against the chat store, while EDITOR, RUN and SINGLETON
// tabs are left alone, since a missing file is no reason to close the tab naming it.
func pruneTabs(ctx context.Context, st *tabs.Store, chats *chat.Store) {
	if st == nil {
		return
	}
	dropped, _, err := st.Prune(ctx, func(t vibekit.TabSubject) bool {
		if t.Kind != vibekit.TabKindChat {
			return true
		}
		_, ok := chats.Get(ctx, vibekit.ChatID(t.Ref))
		return ok
	})
	if err != nil {
		slog.Warn("tab prune failed; the arrangement may name a chat that is gone", "error", err)
		return
	}
	if len(dropped) > 0 {
		slog.Info("tab prune dropped tabs whose subject is gone", "count", len(dropped))
	}
}

// openRunLeaseStore opens the durable run-lease store, ALWAYS returning one: a lease
// carries a run's wall clock and its unattended mark, so refusing to open the store would
// leave every run unbounded — the opposite of what the record is for.
func openRunLeaseStore(dir string) *runlease.Store {
	st, err := runlease.NewStore(dir)
	if err != nil {
		slog.Warn("run leases starting empty; runs from before this boot will not be swept", "error", err)
	}
	return st
}

// startOrphanSweep runs the boot orphan sweep and RETRIES it once the kiro-cli install
// completes. sweep is a method VALUE rather than the run surface, so the retry POLICY is
// testable with no agent runtime behind it.
func startOrphanSweep(ctx context.Context, sweep func(context.Context) bool,
	installed <-chan struct{},
) (stop func()) {
	return runBackground(ctx, "orphan sweep", func(bctx context.Context) {
		if sweep(bctx) {
			return
		}
		select {
		case <-installed:
			sweep(bctx)
		case <-bctx.Done():
		}
	})
}

// startScheduleRunner starts the schedule sweep when scheduling is available; the runner
// reuses Runtime.Launch, so a scheduled run needs no host chat. ctx must be the APP
// lifetime: Runner.Run's only exit is its ctx.Done arm, and Build's context never ends.
func startScheduleRunner(ctx context.Context, st *schedule.Store, l schedule.Launcher) {
	if st == nil {
		return
	}
	go schedule.NewRunner(st, l).Run(ctx)
}

// startPRStatusPoller starts the CI-flip notifier and returns its stop function. The repo
// resolver is the join this root owns: git answers which repos are checked out, forges
// which hosts have an account, and neither package should reach into the other. The lookup
// runs per SWEEP, so a repo or a login added after boot is watched without a restart.
func startPRStatusPoller(ctx context.Context, mgr *forges.Manager,
	gitHandler *git.Handler, notifier forges.PRNotifier,
) (stop func()) {
	repos := func(rctx context.Context) []forges.PRRepo {
		remotes := gitHandler.RepoRemotes(rctx)
		if len(remotes) == 0 {
			return nil
		}
		origins := make([]forges.RepoOrigin, 0, len(remotes))
		for _, r := range remotes {
			origins = append(origins, forges.RepoOrigin{Host: r.Host, Slug: r.Slug})
		}
		return forges.MatchRepos(mgr.List(rctx), origins)
	}
	poller := forges.NewPRStatusPoller(forges.NewManagerPRSource(mgr, repos), notifier)
	return runBackground(ctx, "pr status poller", poller.Run)
}

// backgroundStopGrace bounds how long a stop function waits for its goroutine. The WAIT is
// the point: an unwaited cancel lets a sweep already inside a forge subprocess keep going
// after Shutdown returned. The BOUND is the counterweight — cancellation reaches the
// subprocess through its context, so anything slower is a bug to log, not a hang to take.
const backgroundStopGrace = 5 * time.Second

// runBackground starts fn on a cancellable child of ctx and returns the stop function.
// Exists because "shutdown is context cancellation" needs someone to HOLD the cancel:
// production passes context.Background() into Build, so a loop given it has no owner.
func runBackground(ctx context.Context, name string, fn func(context.Context)) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(ctx)
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(backgroundStopGrace):
			slog.Warn("background loop did not stop within the shutdown grace",
				"loop", name, "grace", backgroundStopGrace)
		}
	}
}
