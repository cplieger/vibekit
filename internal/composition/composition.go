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
	// stopKiro cancels the background kiro-cli install, so shutdown need not wait it out.
	stopKiro func()
	// stopPRPoller stops the PR-status poller and waits for its goroutine.
	stopPRPoller func()
	// stopApp ends the app's LIFETIME: what every process-bound component is parented on.
	stopApp func()
}

// Build constructs all services and wires them together. staticFS is the embedded
// web UI; cfg must be treated as read-only from here on.
func Build(ctx context.Context, cfg *Config, staticFS fs.FS) (*App, error) {
	// flock, so the lock auto-releases on SIGKILL: two processes on one configDir
	// corrupt chat files.
	if err := acquireInstanceLock(cfg.ConfigDir); err != nil {
		return nil, fmt.Errorf("another vibekit instance is running on %s: %w", cfg.ConfigDir, err)
	}

	if err := validateConfig(ctx, cfg); err != nil {
		return nil, fmt.Errorf("config validation failed:\n  %w", err)
	}

	// The app's lifetime: Build's ctx is context.Background() in production, so every
	// component whose work must not outlive the process is parented on appCtx.
	appCtx, stopApp := context.WithCancel(ctx)
	// A boot that returns no App has no Shutdown to call, so the lifetime ends here.
	built := false
	defer cancelUnless(&built, stopApp)

	// Consumers resolve the path per use, so a version switch reaches the next bridge.
	// The install runs in the background: the listener binds first, only readiness waits.
	kiro := startKiroCLI(ctx, cfg)

	logctl.Install(ctx, cfg.ConfigDir)

	steer := steering.New(cfg.WorkDir, cfg.ConfigDir)
	steer.Generate(ctx)

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

	// A factory: resolving once per process would pin every chat to the first install.
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

	// cfg.WorkDir bounds what the reaper may reap: it globs every workspace-hash bucket
	// under one Kiro home, so the root is what each candidate's session.json is matched on.
	sessionReaper := kirosession.New(filepath.Join(workspace.KiroHome(), "sessions"), cfg.WorkDir)
	tabStore := openTabStore(cfg.ConfigDir)
	h := agent.New(appCtx, cfg.WorkDir, bridgeFactory, chatStore,
		agent.WithConfigDir(cfg.ConfigDir), agent.WithMCPConfig(mcpStore), agent.WithPush(pushSvc),
		agent.WithACPArgs(cfg.ACPArgs),
		agent.WithKiroCLIPath(kiro.cliPath, kiro.env),
		agent.WithSessionReaper(sessionReaper, chatStore.ReferencedSessionIDs),
		agent.WithSchedules(scheduleStore),
		agent.WithRunLeases(leaseStore),
		agent.WithTabs(tabStore))
	chat.WithBroadcaster(h)(chatStore)
	pruneTabs(ctx, tabStore, chatStore)

	// Before startScheduleRunner, the only other thing at boot that can launch a run.
	// Background: the sweep issues one RPC per lease over a still-installing kiro-cli.
	go h.Runs().SweepOrphaned(appCtx)

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
		// No bridge restart: persisting renders KAS's config file, whose watcher
		// reconnects in place, so a change reaches every LIVE session.
	})
	mcpPrewarm.Run(ctx)

	steer.SetMCPSnapshot(func() steering.MCPSnapshot {
		return steering.MCPSnapshot{Servers: h.MCPSnapshot()}
	})
	h.SetMCPOnChange(func() { steer.Generate(appCtx) })
	h.SetPreBridgeSpawn(func(ctx context.Context) { steer.Generate(ctx) })

	toolsEngine, err := wireToolsEngine(appCtx, cfg, h)
	if err != nil {
		return nil, err
	}

	forgesManager := forges.NewManager()
	if refreshErr := forgesManager.Refresh(ctx); refreshErr != nil {
		// Non-fatal: no forge CLI installed yet is normal, and the manager starts empty.
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
	// Off the boot path: Run primes and refreshes the identity /api/whoami answers from.
	go authHandler.Run(appCtx)
	forgesHTTP := forges.NewHTTPHandler(forgesManager, h)

	// steering.Generate runs synchronously on the pre-bridge-spawn path, so the snapshot
	// it reads must never block on a forge CLI.
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
	// The purge scans the directory live chats live in, so a live bridge is an exemption.
	chat.WithLive(h.HasLiveBridge)(chatStore)
	// Second exemption: an open tab means the chat is not abandoned work. Accepted
	// consequence — a chat left open forever is opt-out of retention.
	chat.WithOpenTab(h.Membership().HasOpenTab)(chatStore)
	chat.WithOnPurge(func(id vibekit.ChatID, sessionChain []string) {
		for _, sid := range sessionChain {
			sessionReaper.Reap(sid)
		}
		// Covers a tab opened between the exemption check and the remove. Runs after
		// the per-chat record lock is released, which keeps lock order acyclic.
		h.Membership().RetentionClose(appCtx, id)
	})(chatStore)
	// An exempt chat contributes no wake-up deadline, so closing its tab must trigger
	// a pass; without this the purge noticed up to an hour later.
	h.Membership().SetRetentionWake(purgeScheduler.Trigger)
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
		// A security-profile change must recycle the session or the policy view goes stale.
		server.WithPolicyReload(h),
		server.WithStaticFS(static),
		server.WithKiroCLI(kiro.cliPath, kiro.env),
		server.WithKiroReady(kiro.ready),
		server.WithKiroRescan(kiro.rescan),
		server.WithAuthUnavailable(h.AuthTokenUnavailable),
		server.WithConfigDir(cfg.ConfigDir),
		server.WithTabs(tabStore),
		server.WithWorkDir(cfg.WorkDir),
		server.WithTrustedProxies(cfg.TrustedProxies),
		server.WithHostPolicy(cfg.HostPolicy),
	)

	built = true
	return &App{
		Runtime:        h,
		Server:         srv,
		purgeScheduler: purgeScheduler,
		mcpPrewarm:     mcpPrewarm,
		tools:          toolsEngine,
		stopKiro:       kiro.stop,
		stopPRPoller:   stopPRPoller,
		stopApp:        stopApp,
	}, nil
}

// Run starts the HTTP server and blocks until shutdown; signal handling is internal.
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

// Shutdown stops background services in reverse order. Every member is optional — tools is
// nil on a degraded boot — and a panic here would strand the rest of the teardown.
func (a *App) Shutdown() {
	// First, or a sweep reaches into the push service the Runtime is about to close.
	callIfSet(a.stopPRPoller)
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
	// Here, not earlier: ending the lifetime before stopPRPoller above would signal the
	// push service's Done under a poller still sweeping it.
	callIfSet(a.stopApp)
	a.shutdownHub()
}

// hubStopGrace bounds the runtime teardown App.Shutdown owns. It is the PTY teardown's own
// ceiling, the largest single step in the sequence.
const hubStopGrace = 10 * time.Second

// shutdownHub tears the runtime down on a fresh context — the signal context is already
// cancelled on both paths here — and logs an expiry rather than returning it.
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

// callIfSet runs fn when it is set.
func callIfSet(fn func()) {
	if fn != nil {
		fn()
	}
}

// cancelUnless calls cancel unless *built is true, read at call time through the pointer.
func cancelUnless(built *bool, cancel context.CancelFunc) {
	if !*built {
		cancel()
	}
}

// chatRetention resolves the purge window: <= 0 is no purge (0 off, -1 forever), N > 0
// purges after N days. A PRESENT but unreadable config.json returns 0, so a malformed file
// cannot override a stored -1 and delete every chat the user kept; an ABSENT key or file
// keeps the default, because a fresh install has no config.json.
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

// sweepStaleTemps removes orphan temp files left by SIGKILL between CreateTemp and Rename;
// files younger than an hour are spared and ctx bounds the walk. Only atomicfile's own
// ".atomicfile-<digits>.tmp" shape is a candidate, so the recursive configDir leg cannot
// touch a caller-owned file. workDir is swept FLAT: its temps only land at the top level.
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

// forgeSnapshotTTL bounds how stale the cached forge snapshot may get before a read
// kicks a background revalidation. Forge logins and repo lists change rarely.
const forgeSnapshotTTL = 5 * time.Minute

// forgeSnapshotCache is a stale-while-revalidate cache around forgeSnapshot: snapshot
// never blocks on the forge CLIs, refresh rebuilds in the calling goroutine.
type forgeSnapshotCache struct {
	// appCtx is the app's lifetime: neither entry point carries a context, and both
	// reach a rebuild that must not outlive the process.
	appCtx context.Context
	build  func(context.Context) steering.ForgeSnapshot
	steer  *steering.Generator
	at     time.Time
	snap   steering.ForgeSnapshot
	mu     sync.Mutex
	busy   bool
	dirty  bool // a refresh request arrived mid-rebuild; go again
}

// newForgeSnapshotCache wires a cache around build (production's CLI-backed forgeSnapshot;
// injectable for tests) that regenerates steer on data changes. ctx is the app's lifetime
// and is required; the appCtx field says why it cannot arrive at a method instead.
func newForgeSnapshotCache(ctx context.Context, steer *steering.Generator,
	build func(context.Context) steering.ForgeSnapshot,
) *forgeSnapshotCache {
	return &forgeSnapshotCache{appCtx: ctx, build: build, steer: steer}
}

// snapshot returns the cached forge snapshot immediately, stale data triggering a
// single-flight background refresh. Safe on the pre-bridge-spawn critical path.
func (c *forgeSnapshotCache) snapshot() steering.ForgeSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) >= forgeSnapshotTTL && !c.busy {
		c.busy = true
		go c.rebuild()
	}
	return c.snap
}

// refresh rebuilds the snapshot now (network: forge CLI repo listings), then regenerates
// the steering file if the data changed. Callers run it in their own goroutine. A request
// arriving mid-rebuild is coalesced rather than dropped, because that rebuild may already
// have read pre-change CLI config and a fresh login would be stranded until the TTL.
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

// rebuild rebuilds the snapshot and regenerates on change, looping while coalesced
// requests are pending. Entered only with busy already claimed; clears it when done.
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
		// Generate skips byte-identical writes anyway; skipping the render too
		// spares a workspace scan on the common no-change TTL refresh.
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

// repoNamesFor returns the full names of every repo reachable for the given forge, or nil
// if the provider cannot be constructed or the 5s-bounded listing fails. A forge whose
// repos cannot be listed is still surfaced, just without a repo list.
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
// consumers. A nil engine is buildToolsEngine's root-integrity degraded verdict, not an
// error, and the whole wiring is skipped for one rather than nil-guarding downstream:
// no toolbelt method is nil-receiver safe. appCtx is the app's lifetime, forwarded to
// buildToolsEngine, which parents the two code-intelligence activations on it.
func wireToolsEngine(appCtx context.Context, cfg *Config, h *agent.Runtime) (*toolbelt.Engine, error) {
	toolsEngine, err := buildToolsEngine(appCtx, cfg, h)
	if err != nil {
		return nil, err
	}
	if toolsEngine != nil {
		warnIfNoLSPEnabled(toolsEngine)
		// A forge login needs a synchronous install, so it gets the engine's own.
		forges.EnsureTool = toolsEngine.EnsureInstalled
	}
	return toolsEngine, nil
}

// buildToolsEngine constructs the shared toolbelt engine with vibekit's SSE adapters and
// enqueues the boot jobs, reconcile before catalog fetch: the engine's schedule has no
// fire-on-start, and an enqueue inside New would land ahead of boot-critical work on the
// single-flight queue. An enqueue failure is logged, not fatal — installed tools persist
// on the volume. (nil, nil) is the DEGRADED verdict for a root-integrity refusal; every
// other New failure returns an error and stops the boot.
func buildToolsEngine(appCtx context.Context, cfg *Config, h *agent.Runtime) (*toolbelt.Engine, error) {
	catalogRefresh := &toolbelt.CatalogRefresh{
		URL:      cfg.ToolCatalogURL,
		Require:  cfg.ToolCatalogRequire,
		Interval: cfg.ToolCatalogRefresh,
	}
	toolsEngine, err := toolbelt.New(&toolbelt.Config{
		ConfigDir: cfg.ConfigDir,
		ToolsDir:  cfg.ToolsDir,
		// The engine EXECUTES what it finds in ToolsDir — an install probe runs
		// <ToolsDir>/bin/<tool>, and that dir leads PATH for every package-manager
		// run — as root over /config, a volume the operator reshapes by hand. So
		// inspect the roots first; the refusal is degraded, not fatal, below.
		VerifyRootIntegrity: true,
		CatalogPath:         cfg.ToolCatalogPath,
		Refresh:             catalogRefresh,
		CatalogOverlays:     cfg.ToolCatalogOverlays,
		Seed:                toolbelt.DefaultSeed(),
		System:              []string{"git", "jq", "curl", "unzip", "xz", "ssh", "tar", "bash"},
		OnJobChanged: func(j *toolbelt.Job) {
			h.Broadcast(context.Background(), vibekit.NewEvent(vibekit.EventToolJobChanged, "",
				vibekit.ToolJobChangedPayload{Job: j}))
			// A finished install may have produced the first enabled language
			// server. Async because job callbacks fire under the queue lock.
			if j != nil && j.State == toolbelt.JobDone {
				// The app's lifetime, not Background: this goroutine writes
				// lsp.json, and SIGTERM must unwind it rather than abandon it.
				go h.EnsureCodeIntelligence(appCtx)
			}
		},
		OnJobOutput: func(jobID string, lines []string) {
			h.Broadcast(context.Background(), vibekit.NewEvent(vibekit.EventToolJobOutput, "",
				vibekit.ToolJobOutputPayload{JobID: jobID, Lines: lines}))
		},
	})
	if err != nil {
		// Both answers travel in the error: a nil from the classifier is the
		// DEGRADED verdict, so this return yields (nil, nil) for a refusal.
		return nil, toolsEngineFailure(err)
	}
	if _, _, rerr := toolsEngine.Reconcile(toolbelt.ReconcileFull); rerr != nil {
		slog.Warn("tools: boot reconcile not enqueued", "error", rerr)
	}
	if _, rerr := toolsEngine.RefreshCatalog(); rerr != nil {
		slog.Warn("tools: boot catalog refresh not enqueued", "error", rerr)
	}
	// The gate scans the live inventory for an enabled+installed language server. This
	// boot fire covers a volume that has servers but no lsp.json; later fires ride the
	// job callback above.
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

// toolsEngineFailure decides what a toolbelt.New failure costs vibekit: a nil return is
// the DEGRADED verdict and Build carries on tool-less, every other failure stays fatal.
// Only the root-integrity refusal degrades, because an unfit root is persistent-volume
// state this process cannot repair and refusing to boot removes the only way in to fix
// it (invariant 6).
func toolsEngineFailure(err error) error {
	if !errors.Is(err, toolbelt.ErrRootIntegrity) {
		return fmt.Errorf("tools engine: %w", err)
	}
	logRootIntegrityRefusal(err)
	return nil
}

// logRootIntegrityRefusal reports a refusal one line per offending path, then the
// consequence: toolbelt joins the findings into one field, and per-path lines are what
// an operator can grep. Deliberately does NOT touch /api/health — that verdict is the
// kiro-cli install manager's, and this condition never self-heals, so reporting it there
// would leave the container unready forever with no repair path.
func logRootIntegrityRefusal(err error) {
	refusal, ok := errors.AsType[*toolbelt.RootIntegrityError](err)
	if !ok {
		// Classified by the sentinel but not carrying the concrete type
		// (a future wrapper): report the whole error rather than nothing.
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

// warnIfNoLSPEnabled nudges when no language-server entry is enabled: kiro-cli scans
// PATH for language servers at session start, so a box without one silently lacks code
// intelligence. The catalog-derived Lsp marker means any enabled LSP silences it.
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

// openScheduleStore opens the workflow-schedule store, or returns nil to leave scheduling
// off. A malformed schedules.json warns rather than aborting boot (invariant 6: a bad file
// on the persistent volume must still leave a way IN to fix it).
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

// pruneTabs is the tab set's LOAD-TIME crash recovery, running exactly ONCE: the
// membership coordinator is the live mechanism, and this covers only a crash landing
// between its two writes. The resolver answers per KIND — a CHAT tab is checked against
// the chat store, while a missing file, a finished run or a fixed page are not reasons to
// close an EDITOR, RUN or SINGLETON tab.
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

// startScheduleRunner starts the schedule sweep when scheduling is available. The runner
// reuses Runtime.Launch, which launches a PARENTLESS run on its own bridge, so a
// scheduled run needs no host chat. ctx must be the APP lifetime, not Build's own:
// Runner.Run's only exit is its ctx.Done arm, and Build's context is Background in
// production, so the ticker would outlive App.Shutdown.
func startScheduleRunner(ctx context.Context, st *schedule.Store, l schedule.Launcher) {
	if st == nil {
		return
	}
	go schedule.NewRunner(st, l).Run(ctx)
}

// startPRStatusPoller starts the CI-flip notifier and returns its stop function. The repo
// resolver is the join this root owns — git answers which repos are checked out, forges
// which hosts have an account, and neither package reaches into the other. It runs per
// SWEEP, so a repo cloned or a forge logged into after boot is watched without a restart.
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

// backgroundStopGrace bounds how long a stop function waits for its goroutine. The wait
// is the point: an unwaited cancel lets a sweep already inside a forge subprocess run on
// past Shutdown. The bound is its counterweight — cancellation reaches the subprocess
// through its context, so anything slower is a bug to log rather than a hang to take.
const backgroundStopGrace = 5 * time.Second

// runBackground starts fn on a cancellable child of ctx and returns the stop function.
// Someone has to hold the cancel: production passes context.Background() into Build, so a
// loop handed that context directly cannot be stopped short of process exit.
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
