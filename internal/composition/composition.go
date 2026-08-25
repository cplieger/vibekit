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
	"github.com/cplieger/vibekit/internal/uistate"
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
	// stopPRPoller stops the PR-status poller and waits for its goroutine.
	//
	// It exists because Build's ctx is context.Background() in production
	// (main.go), so a loop handed that context is not stopped by anything: the
	// poller kept waking once a minute after App.Shutdown had closed the push
	// service it consults. Holding the cancel here is what makes the component's
	// documented "shutdown is context cancellation" contract true rather than
	// merely masked by runMain exiting the process straight afterwards.
	stopPRPoller func()
	// stopApp ends the app's LIFETIME: the context every component that must die
	// with the process is parented on, and the one agent.New requires.
	//
	// It exists because the app's lifetime used to be invented inside the hub
	// (context.Background() in agent.New) and fetched back out through an exported
	// ShutdownCtx() accessor, which is how four goroutines wired here came to
	// take their context from a component they are not part of. The composition
	// root owns the lifetime now and hands the same one down.
	stopApp func()
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

	if err := validateConfig(ctx, cfg); err != nil {
		return nil, fmt.Errorf("config validation failed:\n  %w", err)
	}

	// The app's lifetime. Build's ctx is context.Background() in production
	// (main.go), so it can never end; appCtx is the cancellable child that CAN,
	// and it is what every component whose work must not outlive the process is
	// parented on. App.Shutdown ends it (see stopApp).
	//
	// Deriving it here rather than inside a component is the point: the runtime used
	// to root its own lifetime at context.Background() and expose it as
	// ShutdownCtx(), so a goroutine wired in this file took its context out of
	// the agent. Now the lifetime flows the other way.
	appCtx, stopApp := context.WithCancel(ctx)
	// A boot that does not return an App is the one case nothing can call
	// App.Shutdown, so the lifetime is ended here instead. That includes the
	// (nil, nil) root-integrity degraded verdict below, not just the error
	// returns.
	built := false
	defer cancelUnless(&built, stopApp)

	// kiro-cli is installed and selected by the manager startKiroCLI builds: the
	// bridge's argv and environment, the auth shell-outs, the CLI runner and the
	// /api/health verdict all come from it, so a version switch reaches the next
	// bridge instead of being frozen at boot. The install runs in the background
	// -- the listener binds first and only readiness waits.
	kiro := startKiroCLI(ctx, cfg)

	logctl.Install(ctx, cfg.ConfigDir)

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

	// One bridge per chat, so the resolution happens per SPAWN: the bridge is
	// this app's long-lived kiro-cli consumer, and resolving once per process
	// would pin every chat to whatever was installed first.
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

	// vibekit owns kiro-cli/KAS session cleanup end to end (cleanup.periodDays
	// pinned to 0/never): reap a chat's session state on delete, and orphans
	// via a periodic sweep that spares every active/archived chat's session.
	sessionReaper := kirosession.New(filepath.Join(workspace.KiroHome(), "sessions"))
	h := agent.New(appCtx, cfg.WorkDir, bridgeFactory, chatStore,
		agent.WithConfigDir(cfg.ConfigDir), agent.WithMCPConfig(mcpStore), agent.WithPush(pushSvc),
		agent.WithACPArgs(cfg.ACPArgs),
		agent.WithKiroCLIPath(kiro.cliPath, kiro.env),
		agent.WithSessionReaper(sessionReaper, chatStore.ReferencedSessionIDs),
		agent.WithSchedules(scheduleStore),
		agent.WithRunLeases(leaseStore))
	chat.WithBroadcaster(h)(chatStore)

	// Clear the runs a previous process left paused, BEFORE anything can launch.
	// The scheduler below is the only thing at build time that can, and its own
	// first tick is a minute out, but relying on that would make correctness a
	// property of the tick interval.
	//
	// On the APP's lifetime, never this function's: Build's ctx is
	// context.Background() in production, so a sweep started on it would outlive
	// App.Shutdown. In the background because the sweep issues one RPC per lease
	// over a utility bridge whose kiro-cli may still be installing; a boot must not
	// wait on that.
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
		// No bridge restart, and nothing to forward: the store's persist renders
		// KAS's own config file, whose watcher re-merges and reconnects in place.
		// A change reaches every LIVE session, not just the next one.
	})
	mcpPrewarm.Run(ctx)

	steer.SetMCPSnapshot(func() steering.MCPSnapshot {
		return steering.MCPSnapshot{Servers: h.MCPSnapshot()}
	})
	h.SetMCPOnChange(func() { steer.Generate(appCtx) })
	h.SetPreBridgeSpawn(func(ctx context.Context) { steer.Generate(ctx) })

	// Tools engine: the cplieger/toolbelt reconciler owns tools.json v2
	// + the install tree + the job queue; job lifecycle/output stream
	// over the runtime's SSE via the Config callbacks. The seed plants the
	// disabled LSP + gh templates on fresh volumes (toggled on in
	// Settings -> Tools). Boot reconciles async — installed tools
	// persist on the volume, so nothing blocks server start.
	toolsEngine, err := wireToolsEngine(appCtx, cfg, h)
	if err != nil {
		return nil, err
	}

	forgesManager := forges.NewManager()
	if refreshErr := forgesManager.Refresh(ctx); refreshErr != nil {
		// Non-fatal: refreshing CLI configs may fail if no CLIs are
		// installed yet. The manager starts with an empty list.
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

	// The forge snapshot shells out to the forge CLIs (gh repo list is a
	// network round trip, 5s cap per forge), and steering.Generate runs
	// synchronously on the pre-bridge-spawn critical path — so the
	// snapshot the generator reads is a never-blocking cache. It is
	// primed off the boot path and refreshed on forge changes and on a
	// TTL, each refresh regenerating environment.md when data changed.
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

	retention := func() time.Duration {
		// vibekit owns retention (see settings.KeyChatRetentionDays). <= 0 is
		// "no purge": 0 = off (chats deleted on close, nothing to purge) and
		// -1 = forever (archived, never purged). N > 0 = purge after N days.
		days, ok := settings.Field[int](ctx, cfg.ConfigDir, settings.KeyChatRetentionDays)
		if !ok {
			days = settings.DefaultChatRetentionDays
		}
		if days <= 0 {
			return 0
		}
		return time.Duration(days) * 24 * time.Hour
	}
	purgeScheduler := chat.NewPurgeScheduler(chatStore, retention)
	// Retention must never delete a chat someone is using. Chats no longer
	// move to an archive directory, so the purge scans the SAME directory live
	// chats live in — this predicate is what keeps an old-but-open conversation
	// out of it.
	chat.WithLive(h.HasLiveBridge)(chatStore)
	chat.WithOnPurge(func(_ vibekit.ChatID, sessionChain []string) {
		for _, id := range sessionChain {
			sessionReaper.Reap(id)
		}
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
		server.WithStaticFS(static),
		server.WithCLIPath(kiro.cliPath),
		server.WithKiroReady(kiro.ready),
		server.WithKiroRescan(kiro.rescan),
		server.WithAuthUnavailable(h.AuthTokenUnavailable),
		server.WithConfigDir(cfg.ConfigDir),
		server.WithUIState(openUIStateStore(cfg.ConfigDir)),
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
		a.shutdownHub()
	}
	return err
}

// Shutdown stops background services in reverse order.
//
// Every member is treated as optional. One already is (tools is nil on the
// root-integrity degraded boot, and Close is not nil-receiver safe), and the rest
// share the reason: a shutdown that panics on a service which was never started
// takes the ordered teardown of the ones that WERE started down with it. It is also
// what lets the lifecycle be asserted through this function rather than through a
// cancel a test made up.
func (a *App) Shutdown() {
	// Before the rest: the poller consults the push service the Runtime owns, so
	// stopping it first is what keeps a sweep from reaching into a closed one.
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
	// The app's lifetime ends immediately BEFORE the runtime's own teardown, not
	// after it: the runtime's shutdown context is a child of this one, so cancelling
	// here is the same instant the app-lifetime goroutines used to lose the
	// context they took out of the runtime via ShutdownCtx(). Doing it earlier would
	// signal the push service's Done before the poller that consults it has been
	// stopped, which is what the ordering above exists to prevent.
	callIfSet(a.stopApp)
	a.shutdownHub()
}

// hubStopGrace bounds the runtime teardown App.Shutdown owns.
//
// Invented here because there is nothing to inherit: this path runs from
// runMain's defer and from a serve failure, and the signal context is already
// cancelled by then, so a derived budget would be zero. 10s is the PTY
// teardown's own ceiling (runtime's shutdownBudget, above the engine's 5s reap) and
// the largest single step in the sequence, so less would report an expiry for
// work that was always going to take that long.
const hubStopGrace = 10 * time.Second

// shutdownHub tears the runtime down on that budget and logs an expiry rather than
// dropping it: both callers are terminal paths with nobody above them to return
// an error to.
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

// callIfSet runs fn when it is set. Two of App's members are functions rather than
// objects, so their nil check cannot be a method call on a nil receiver.
func callIfSet(fn func()) {
	if fn != nil {
		fn()
	}
}

// cancelUnless calls cancel unless *built is true. Deferred by Build, and a named
// function rather than a closure so the guard does not count against Build's
// complexity ceiling: `built` is read at call time through the pointer.
func cancelUnless(built *bool, cancel context.CancelFunc) {
	if !*built {
		cancel()
	}
}

// sweepStaleTemps removes orphan temp files left by SIGKILL between
// CreateTemp and Rename. Files younger than 1 hour are spared.
//
// configDir is swept with WithRecursive, which removes the maintenance hazard in the
// previous shape: it enumerated configDir, configDir/chats and configDir/chats/<archive>
// by hand, so every new location that writes atomically had to be added to that list or
// its orphans were never reaped. Widening the sweep is safe by construction: only
// atomicfile's own ".atomicfile-<digits>.tmp" shape is ever a candidate, so a caller-owned
// file is never touched. (An earlier version of this comment named checkpoints/blobs as
// the motivating unlisted location. It is not one: nothing writes under
// configDir/checkpoints any more -- KAS owns snapshots -- and Build os.RemoveAll's that
// whole directory a few lines above this call, so the sweep never sees it. The maintenance
// argument stands on the locations that are live.)
//
// workDir is swept flat on purpose. It is the user's working tree, which can be an
// arbitrarily large checkout; descending it on every startup would walk the whole repo to
// find temps that only ever land at its top level.
//
// ctx bounds the walk. The recursive configDir sweep is the one leg here that can take
// real time -- it descends every location under configDir that writes atomically -- and it
// runs on the startup path, so a shutdown arriving mid-sweep should abandon it rather than
// hold the boot open.
//
// The two non-zero counts are reported because they are different operator problems and
// only the operator can fix either. Failed means a temp was found and could not be
// unlinked, so orphans are ACCUMULATING on a persistent volume -- a disk-fill precursor
// with no self-healing path. Unreadable means a subdirectory could not be entered at all,
// so it may be hiding orphans nobody has counted, which is the shape a widened ACL or a
// mode drift takes on this fleet's volumes. Removed stays at Debug: a sweep that worked is
// not news. Folding the two together would leave an operator unable to tell which one to
// go fix, which is exactly why atomicfile returns them apart instead of logging a summary.
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
	// appCtx is the app's lifetime, required at construction. It cannot be a
	// method parameter: the cache's entry points are a steering callback of
	// signature func() steering.ForgeSnapshot (snapshot) and a change hook of
	// signature func() (refresh), neither of which carries a context, and both
	// reach rebuild — which shells out to the forge CLIs and regenerates the
	// steering file. The rebuild is the work that must not outlive the process,
	// so the lifetime is held here.
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
//
// ctx is the app's lifetime and is required; see the appCtx field for why it
// cannot arrive at a method instead.
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

// repoNamesFor returns the full names of every repo reachable for the
// given forge, or nil if the provider can't be constructed or the
// listing fails. Enrichment is best-effort: a forge whose repos can't
// be listed is still surfaced, just without a repo list. A 5s timeout
// bounds the CLI call.
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

// warnIfNoLSPEnabled logs the code-intelligence nudge when no
// language-server entry is enabled: kiro-cli scans PATH for language
// servers at session start, so a fresh box without one silently lacks
// code intelligence. Detection uses the inventory's catalog-derived
// Lsp marker, so any enabled LSP (seeded template or hand-added)
// silences it.
// wireToolsEngine builds the tools engine and, when the root is intact,
// wires its consumers. A nil engine is the root-integrity degraded verdict
// (buildToolsEngine), not an error. The tools-dependent wiring is skipped
// rather than nil-guarding each consumer downstream: none of toolbelt's
// methods is nil-receiver safe, and server.WithTools already omits the
// /api/tools mount for a nil engine. forges.EnsureTool left nil makes a
// forge login report "gh is not installed (tools engine unavailable)"
// instead of panicking.
//
// appCtx is the app's lifetime, forwarded to buildToolsEngine, which parents
// the two code-intelligence activations on it.
func wireToolsEngine(appCtx context.Context, cfg *Config, h *agent.Runtime) (*toolbelt.Engine, error) {
	toolsEngine, err := buildToolsEngine(appCtx, cfg, h)
	if err != nil {
		return nil, err
	}
	if toolsEngine != nil {
		warnIfNoLSPEnabled(toolsEngine)
		// Installed-tool changes matter to the agent (kiro-cli scans PATH
		// for language servers); regenerate the steering env on each list
		// read is overkill, but forge logins need synchronous installs:
		forges.EnsureTool = toolsEngine.EnsureInstalled
	}
	return toolsEngine, nil
}

// buildToolsEngine constructs the shared toolbelt engine with vibekit's
// SSE adapters and enqueues the boot jobs: the full reconcile first,
// then the boot catalog fetch (the engine's schedule has no
// fire-on-start; an immediate enqueue inside New would land ahead of
// boot-critical work on the single-flight queue). Failures to enqueue
// are logged, not fatal: installed tools persist on the volume and
// keep-last-good absorbs an unreachable publisher.
//
// (nil, nil) is the DEGRADED verdict, not an omission: the root-integrity
// refusal (Config.VerifyRootIntegrity below) returns no engine and no error,
// and Build carries on tool-less. Every other New failure still returns an
// error and stops the boot, exactly as it did before the check existed.
func buildToolsEngine(appCtx context.Context, cfg *Config, h *agent.Runtime) (*toolbelt.Engine, error) {
	catalogRefresh := &toolbelt.CatalogRefresh{
		URL:      cfg.ToolCatalogURL,
		Require:  cfg.ToolCatalogRequire,
		Interval: cfg.ToolCatalogRefresh,
	}
	toolsEngine, err := toolbelt.New(&toolbelt.Config{
		ConfigDir: cfg.ConfigDir,
		ToolsDir:  cfg.ToolsDir,
		// The engine EXECUTES what it finds in ToolsDir: an install probe
		// runs <ToolsDir>/bin/<tool> directly, and that dir goes first on
		// PATH for every package-manager run. vibekit runs those as root
		// over /config, a volume the operator reshapes by hand, and the
		// entrypoint creates only bin/ and kiro-cli-versions/ — opt/, npm/
		// and python/ are engine-owned and nothing checked them. On:
		// inspect the roots before writing anything.
		//
		// This is why the refusal must NOT be fatal — see the degraded arm
		// below.
		VerifyRootIntegrity: true,
		CatalogPath:         cfg.ToolCatalogPath,
		Refresh:             catalogRefresh,
		CatalogOverlays:     cfg.ToolCatalogOverlays,
		Seed:                toolbelt.DefaultSeed(),
		System:              []string{"git", "jq", "curl", "unzip", "xz", "ssh", "tar", "bash"},
		OnJobChanged: func(j *toolbelt.Job) {
			h.Broadcast(context.Background(), vibekit.NewEvent(vibekit.EventToolJobChanged, "",
				vibekit.ToolJobChangedPayload{Job: j}))
			// A finished install/reconcile may have just produced the
			// first enabled language server: activate workspace code
			// intelligence (idempotent; no-op while lsp.json exists or
			// no lsp tool is enabled+installed). Async — job callbacks
			// must not block (fired under the queue lock).
			if j != nil && j.State == toolbelt.JobDone {
				// The app's lifetime, not Background: this goroutine writes
				// lsp.json, and a Background parent meant SIGTERM abandoned it
				// mid-write instead of unwinding it.
				go h.EnsureCodeIntelligence(appCtx)
			}
		},
		OnJobOutput: func(jobID string, lines []string) {
			h.Broadcast(context.Background(), vibekit.NewEvent(vibekit.EventToolJobOutput, "",
				vibekit.ToolJobOutputPayload{JobID: jobID, Lines: lines}))
		},
	})
	if err != nil {
		// Both answers travel in the error: nil from the classifier is the
		// DEGRADED verdict, so this one return yields (nil, nil) for an
		// integrity refusal and (nil, wrapped) for everything else.
		return nil, toolsEngineFailure(err)
	}
	if _, _, rerr := toolsEngine.Reconcile(toolbelt.ReconcileFull); rerr != nil {
		slog.Warn("tools: boot reconcile not enqueued", "error", rerr)
	}
	if _, rerr := toolsEngine.RefreshCatalog(); rerr != nil {
		slog.Warn("tools: boot catalog refresh not enqueued", "error", rerr)
	}
	// Code-intelligence activation (agent/code_intel.go): the gate scans
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
	// The app's lifetime, not Background — see the OnJobChanged spawn above.
	go h.EnsureCodeIntelligence(appCtx)
	return toolsEngine, nil
}

// toolsEngineFailure decides what a toolbelt.New failure costs vibekit. A nil
// return is the DEGRADED verdict — buildToolsEngine hands back no engine and no
// error, and Build carries on tool-less.
//
// ONLY the root-integrity refusal degrades. A manifest this engine refuses to
// guess at, or an absent required dir, stays a fatal New failure wrapped exactly
// as it was before the check existed — narrowing by sentinel rather than by "any
// New error" is what keeps an unrelated regression from being swallowed into a
// tool-less boot nobody notices.
//
// Degraded, not dead, because an unfit root is persistent-volume state this
// process neither created nor may repair (the check reports only, deliberately),
// and refusing to boot on it would take away the only way IN to fix it: the
// operator reaches /config through this container. The entrypoint aborts on ONE
// condition, a /config that is absent or unwritable, and everything else warns
// and continues.
func toolsEngineFailure(err error) error {
	if !errors.Is(err, toolbelt.ErrRootIntegrity) {
		return fmt.Errorf("tools engine: %w", err)
	}
	logRootIntegrityRefusal(err)
	return nil
}

// logRootIntegrityRefusal reports a root-integrity refusal one line per
// offending path, then states the consequence. toolbelt logs the same
// findings itself, but joined into a single field on its own logger; the
// per-path lines are what an operator can grep and act on individually,
// and "vibekit is running without tools" is this app's verdict to state,
// not the library's.
//
// The refusal does NOT touch readiness: /api/health's verdict is the
// kiro-cli install manager's, and wiring a never-self-healing condition
// into it would report the container unready forever, with no repair path
// that does not go through the host. The log plus an absent /api/tools
// mount is the signal.
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

// openScheduleStore opens the workflow-schedule store, or returns nil to leave
// scheduling off.
//
// A malformed schedules.json warns and disables scheduling rather than aborting
// boot (invariant 6: this is a dev box, so a bad file on the persistent volume
// must still leave a way IN to fix it).
func openScheduleStore(dir string) *schedule.Store {
	st, err := schedule.NewStore(dir)
	if err != nil {
		slog.Warn("workflow scheduling disabled", "error", err)
		return nil
	}
	return st
}

// openUIStateStore opens the synced UI-arrangement store.
//
// It ALWAYS returns a store: uistate.NewStore hands back a usable empty one
// alongside a parse error, and the arrangement is re-derivable by opening the
// tabs again. Disabling the sync instead would put the client back on a local
// copy, which is the exact shape whose per-device drift this store replaced.
func openUIStateStore(dir string) *uistate.Store {
	st, err := uistate.NewStore(dir)
	if err != nil {
		slog.Warn("ui arrangement starting empty", "error", err)
	}
	return st
}

// openRunLeaseStore opens the durable run-lease store.
//
// It ALWAYS returns a store: runlease.NewStore hands back a usable empty one
// alongside the error, because a lease carries a run's wall clock and its
// unattended mark. Refusing to open it would leave every run unbounded, which is
// the opposite of what the record is for — so an unreadable file warns, starts
// empty and lets the next launch mint fresh leases.
func openRunLeaseStore(dir string) *runlease.Store {
	st, err := runlease.NewStore(dir)
	if err != nil {
		slog.Warn("run leases starting empty; runs from before this boot will not be swept", "error", err)
	}
	return st
}

// startScheduleRunner starts the schedule sweep when scheduling is available.
//
// The runner reuses Runtime.Launch, which already launches a PARENTLESS run on
// its own bridge, so a scheduled run needs no host chat and never shows up in
// the chat list.
//
// ctx must be the APP lifetime, not Build's own: Runner.Run's only exit is
// its ctx.Done arm, so handing it Build's context (context.Background() in
// production) makes that arm unreachable and the ticker outlives App.Shutdown.
// stopPRPoller's comment records the same defect for the sibling loop.
func startScheduleRunner(ctx context.Context, st *schedule.Store, l schedule.Launcher) {
	if st == nil {
		return
	}
	go schedule.NewRunner(st, l).Run(ctx)
}

// startPRStatusPoller starts the CI-flip notifier and returns its stop function.
//
// The repo resolver is the join this root owns: git answers which repositories are
// checked out and where their origins point, forges answers which of those hosts
// has a connected account, and neither package should reach into the other. The
// lookup runs per SWEEP rather than once here, so a repo cloned after boot and a
// forge logged into after boot both start being watched without a restart.
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

// backgroundStopGrace bounds how long a stop function waits for its goroutine.
//
// The wait itself is the point — an unwaited cancel lets a sweep already inside a
// forge subprocess keep going after Shutdown returned, which is the shape of the
// leak this replaced. The BOUND is the counterweight: cancellation reaches the
// subprocess through its context, so a sweep unwinds in milliseconds, and anything
// that does not is a bug to log rather than a shutdown to hang on.
const backgroundStopGrace = 5 * time.Second

// runBackground starts fn on a cancellable child of ctx and returns the function
// that stops it.
//
// It exists because "shutdown is context cancellation" is only true when someone
// holds the cancel. Production passes context.Background() into Build, so a loop
// given that context directly has no owner: the process exiting is what stopped it,
// which makes the component's contract untestable and false for any caller that
// shuts services down without exiting.
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
