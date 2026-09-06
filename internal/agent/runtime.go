// Package agent coordinates the server's per-chat runtime: SSE fan-out, ACP
// bridge lifecycle, POST /api/command dispatch, the service surfaces that ride
// the shared utility bridge, checkpoint HTTP, agent terminals, the browser PTY
// shell shim, and the MCP runtime registry.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/ignore"
	"github.com/cplieger/vibekit/internal/kiroauth"
	"github.com/cplieger/vibekit/internal/kirosession"
	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/schedule"
	"github.com/cplieger/vibekit/internal/secretstore"
	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/v2"
	"github.com/cplieger/webhttp/v2/sse"
)

const (
	replayBufSize = 1024

	// keepaliveInterval is shared by SSE keepalive comments and WebSocket pings.
	// iOS Safari kills idle background connections after ~30s.
	keepaliveInterval = 15 * time.Second

	// outputBufferLimit is the byte budget for subprocess output ring buffers.
	// 64 KB covers a 200x50 terminal screen with generous ANSI escapes.
	outputBufferLimit = buffer.DefaultOutputCap
)

// lifetime groups Runtime fields related to process lifecycle,
// shutdown coordination, and workspace paths.
type lifetime struct {
	// shutdownCtx is the runtime's own cancellable child of the lifetime context
	// New requires, so Shutdown can tear the runtime down without ending the app.
	shutdownCtx    context.Context
	done           chan struct{}
	shutdownCancel context.CancelFunc
	// workRoot is the kernel-confined handle on workDir, so an ancestor swapped
	// for a symlink after the check redirects nothing outside the tree (see
	// confineInWorkDir). Deliberately NOT closed: only process exit ends its
	// lifetime. nil when workDir could not be opened, and the fs handlers then
	// REFUSE rather than falling back to ambient os calls.
	workRoot  *os.Root
	workDir   string
	configDir string
	inflight  sync.WaitGroup
	// loops covers the background goroutines that exit on done. A SEPARATE group
	// from inflight, so a shutdown that times out names a wedged handler or loop.
	loops    sync.WaitGroup
	mu       sync.Mutex
	draining atomic.Bool
}

// derivedContext returns a cancellable child of the process lifetime, for
// work that must outlive the request that started it.
func (lt *lifetime) derivedContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(lt.shutdownCtx)
}

// TurnContext returns the context a turn runs under, plus the teardown its
// handler must defer.
//
// The turn is DETACHED from reqCtx's cancellation while keeping its values: the
// prompt POST's context dies when the handler returns, and a turn that died with
// it failed before persisting the assistant buffer. Cancellation is re-attached
// to the shutdown context via AfterFunc, which the returned cancel unregisters.
func (lt *lifetime) TurnContext(reqCtx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(reqCtx))
	stop := context.AfterFunc(lt.shutdownCtx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// InflightAdd increments the inflight counter Shutdown waits on.
func (lt *lifetime) InflightAdd(delta int) {
	lt.inflight.Add(delta)
}

// InflightDone decrements the inflight counter.
func (lt *lifetime) InflightDone() {
	lt.inflight.Done()
}

// bridges groups Runtime fields related to ACP bridge management.
type bridges struct {
	factory ACPBridgeFactory
	mgr     *bridgeManager
}

// bus groups Runtime fields related to SSE transport, replay and pending
// permissions. The transport is webhttp/sse's; vibekit layers chat-topic
// filtering and pending-state replay on top.
type bus struct {
	fanout       *sse.Hub
	pendingPerms *pendingPermsTracker
	// chatStatus holds each chat's last self-declared status, the one turn_state
	// input that lives on no message and in no replay (chat_status.go).
	chatStatus *chatStatusCache
}

// Runtime is the central coordinator.
type Runtime struct {
	lifecycle *lifetime
	bridge    *bridges
	bus       *bus
	coord     *BridgeCoordinator

	push               pushService
	chatStore          chatRecords
	mcpConfig          mcpNameSets
	mcpRegistry        *mcpRegistry
	shellMgr           *ShellManager
	kiroToken          *kiroauth.CLISource
	chatHandlers       map[string]chatHandler
	sessUpdateHandlers map[vibekit.ACPUpdateKind]sessionUpdateHandler
	noopMethods        map[string]struct{}
	dispatcher         *command.Dispatcher
	translator         *translate.Translator
	// config owns the KAS configuration surface, all of it over the utility
	// bridge. See config_plane.go.
	config *Settings
	// runs owns the workflow-run surface (run_plane.go); Runtime reaches it like
	// any collaborator.
	runs          *Runs
	runRoutes     *runRoutes
	inbound       *inbound
	replay        *replay
	utility       *utilityLease
	sessionReaper *kirosession.Reaper
	sessionRefs   func(context.Context) (map[string]struct{}, bool)
	// sweepGate closes when this process has become the one SERVING its config
	// dir, which the destructive session sweep waits for. Nil = no gate.
	sweepGate  <-chan struct{}
	lines      *buffer.LineTracker
	agentTerms *agentTerminals
	hookStatus *hookStatusCache
	// authLatch remembers the last outcome of vending a KAS access token, so
	// readiness can report a dead sign-in without asking kiro-cli.
	authLatch *authTokenLatch

	// secrets holds the credential blobs KAS asks vibekit to persist on its behalf
	// (bridge_v3_secret.go). ONE store for every bridge, because KAS's key
	// namespace is global. Nil → the handlers report "absent" rather than failing
	// an MCP connect.
	secrets *secretstore.Store

	// tabs is the open-tab set and membership the coordinator over it and the chat
	// store; both nil when no store is wired, which the coordinator answers as
	// unavailable. The runtime holds the coordinator only to hand it to retention.
	tabs       *tabs.Store
	membership *command.Membership

	// steerLedger records the mid-turn steers this server sent, which is the only
	// thing that tells the user's own words from a workflow reporting into the same
	// KAS buffer.
	steerLedger *command.SteerLedger

	// Code-intelligence activation inputs + in-flight guard (code_intel.go).
	ciGate func() bool
	ciPath string
	// acpArgs are the filtered operator kiro-cli launch flags (WithACPArgs). Chat
	// bridges only, and last among the pointer-bearing fields for fieldalignment.
	acpArgs []string
	ciBusy  atomic.Bool
}

// Option configures optional Runtime parameters.
type Option func(*Runtime)

// WithConfigDir sets the configuration directory for permissions,
// checkpoints, and ignore rules.
func WithConfigDir(dir string) Option {
	return func(h *Runtime) { h.lifecycle.configDir = dir }
}

// WithACPArgs sets the operator-supplied kiro-cli launch flags appended to every
// CHAT bridge's argv. Pass them already filtered (bridge.ParseACPArgs); never
// reaches the utility bridge.
func WithACPArgs(args []string) Option {
	return func(h *Runtime) { h.acpArgs = args }
}

// WithSchedules wires the workflow-schedule store. Absent, the schedule routes
// are not registered at all and nothing fires.
func WithSchedules(st *schedule.Store) Option {
	return func(h *Runtime) { h.runs.schedules = st }
}

// WithRunLeases wires the DURABLE run-lease store. Unlike WithSchedules, absent
// does not mean off: the runtime falls back to an in-memory registry, because a
// lease carries the run's wall clock and its unattended mark. What this adds is
// survival across a restart.
func WithRunLeases(st *runlease.Store) Option {
	return func(h *Runtime) { h.runs.leases = st }
}

// WithTabs wires the open-tab set, which is what makes the tab commands, the
// tabs_changed event and retention's open-tab predicate live. Absent, every tab
// command answers unavailable and a create writes its chat record with no tab.
func WithTabs(st *tabs.Store) Option {
	return func(h *Runtime) { h.tabs = st }
}

// WithPush wires the push notification service at construction time.
func WithPush(p pushService) Option {
	return func(h *Runtime) { h.push = p }
}

// WithMCPConfig wires the MCP configuration store. The registry reads the three
// name sets to classify a status notification's origin and to drop the frames of
// a server the user switched off.
func WithMCPConfig(c mcpNameSets) Option {
	return func(h *Runtime) { h.mcpConfig = c }
}

// WithKiroCLIPath wires the v3 auth-callback token source over the active
// kiro-cli binary. resolve is the install manager's path resolver ("" while
// nothing is installed), so a version switch reaches the next callback; env is
// its PATH overlay, required because kiro-cli resolves its kiro-cli-chat sidecar
// by bare name. Unset → the auth callback answers with an RPC error.
func WithKiroCLIPath(resolve func() string, env func() []string) Option {
	return func(h *Runtime) { h.kiroToken = kiroauth.NewCLISource(resolve, env) }
}

// WithSessionReaper wires the KAS session reaper and the referenced-session
// thunk. The reaper removes on-disk kiro-cli/KAS session state, promptly on chat
// delete and via a periodic orphan sweep. Unset → session reaping is a no-op.
//
// refs returns (set, complete). A false `complete` means the keep-list could not
// be fully determined, and the sweep is SKIPPED rather than run against a partial
// one — see sweepSessionsOnce.
func WithSessionReaper(r *kirosession.Reaper, refs func(context.Context) (map[string]struct{}, bool)) Option {
	return func(h *Runtime) {
		h.sessionReaper = r
		h.sessionRefs = refs
	}
}

// WithSessionSweepGate holds the orphan-session sweep until gate closes, which the
// composition root closes once the listener has SUCCESSFULLY bound — the cheapest
// ownership evidence at boot, since the reaper's root comes from $KIRO_HOME rather
// than the config dir. The PERIODIC sweep only; reap-on-delete is unaffected.
func WithSessionSweepGate(gate <-chan struct{}) Option {
	return func(h *Runtime) { h.sweepGate = gate }
}

// New constructs a Runtime. Bridges spawn with a fixed kiro-cli acp arg set;
// tool-call authorization is kiro-cli's native Cedar policy, not CLI trust flags.
//
// ctx is the runtime's LIFETIME and is REQUIRED: there is deliberately no nil
// check and no WithLifetime option — a nil ctx panics in context.WithCancel below,
// which is the refusal. New does NOT take ownership of its cancellation; Shutdown
// cancels the runtime's own child. chatStore is REQUIRED on the same terms,
// refused at construction rather than crashing on the first ACP frame.
func New(ctx context.Context, workDir string, factory ACPBridgeFactory, chatStore chatRecords, opts ...Option) *Runtime {
	sseHub := sse.NewHub(sse.WithReplay(replayBufSize), sse.WithKeepalive(keepaliveInterval))
	lc := &lifetime{
		workDir: workDir,
		done:    make(chan struct{}),
	}
	lc.shutdownCtx, lc.shutdownCancel = context.WithCancel(ctx)
	// Best-effort, and fail-CLOSED at the handlers rather than at construction: an
	// unopenable workDir is a deployment fault repaired from inside the container.
	if root, err := os.OpenRoot(workDir); err != nil {
		slog.Error("workspace root: open failed; agent filesystem requests will be refused",
			"work_dir", workDir, "error", err)
	} else {
		lc.workRoot = root
	}

	// Locals first, so the run surface can name the two collaborators it depends on
	// rather than reaching them through a *Runtime. The rest are assigned below.
	bridgeP := &bridges{
		factory: factory,
		mgr:     newBridgeManager(factory),
	}
	sseP := &bus{
		fanout:       sseHub,
		pendingPerms: newPendingPermsTracker(),
		chatStatus:   newChatStatusCache(),
	}
	configP := newSettings(lc, nil) // broadcast assigned below, with the rest
	runs := &Runs{
		bridges:   bridgeP.mgr,
		lifecycle: lc,
		chats:     chatStore,
		perms:     sseP,
		bus:       sseP,
	}

	h := &Runtime{
		lifecycle:    lc,
		bridge:       bridgeP,
		bus:          sseP,
		runs:         runs,
		config:       configP,
		chatStore:    chatStore,
		hookStatus:   newHookStatusCache(kiroSettingsPath()),
		authLatch:    &authTokenLatch{},
		chatHandlers: make(map[string]chatHandler),
		noopMethods:  make(map[string]struct{}),
	}
	// Options may write the run surface's two stores, so they run after it exists.
	for _, o := range opts {
		o(h)
	}
	// CONSTRUCTION, then WIRING, in that order and not interleaved: every role below
	// is bound to its owner BY VALUE, so a field still nil at the literal stays nil
	// forever. TestNew_EveryTranslateRoleIsWired pins it.
	h.utility = &utilityLease{build: h.buildUtility}
	h.runRoutes = &runRoutes{runs: runs}
	h.mcpRegistry = newMCPRegistry(bridgeP.mgr, sseP, lc, h.mcpConfig)
	h.replay = &replay{chats: chatStore, lifetime: lc, projections: map[vibekit.ChatID]*loadProjection{}}
	h.coord = newBridgeCoordinator(h)
	// Built here rather than in the struct literal because two of its collaborators
	// (coord, and the ignore matcher installed below) do not exist yet at that point.
	h.inbound = &inbound{
		lifetime: lc, coord: h.coord, chats: chatStore,
		bus: sseP, kiroToken: h.kiroToken, authLatch: h.authLatch,
	}
	h.shellMgr = NewShellManager(lc.shutdownCtx, workDir)
	h.lines = buffer.NewLineTracker()
	h.agentTerms = newAgentTerminals(bridgeP.mgr, lc, sseP.Broadcast, h.coord.turns.currentEpoch)
	// Assigned here rather than in the struct literal because it is a method value
	// on the fully-built Runtime; see load_projection.go.
	h.replay.onProjection = h.replay.swapProjectedTranscript
	// The LEASE's own accessor, not a Runtime method value, so neither collaborator
	// holds a reference to the Runtime.
	runs.utility = h.utility.get
	runs.coord = h.coord
	configP.utility = h.utility.get
	configP.broadcast = sseP.Broadcast

	// Before both consumers: the steer command writes it and the translator reads it.
	h.steerLedger = command.NewSteerLedger()
	h.translator = translate.New(h.translateRoles())
	runs.translate = h.translator
	h.dispatcher = command.New()
	h.registerCommandHandlers()
	// After the coordinator exists and before initDispatch registers the run frames
	// that reach it. Two directions, one edge each, so neither can be a literal.
	runs.tabs = h.membership
	h.initDispatch()
	if lc.configDir != "" {
		h.inbound.ignore = ignore.NewMatcher(lc.configDir, workDir)
		// Best-effort: a nil store means bridges do NOT declare
		// `_meta.kiro.secretStorage`, so KAS never asks and MCP OAuth re-registers.
		secrets, err := secretstore.New(lc.configDir)
		if err != nil {
			slog.Error("secretstore: open failed; MCP credentials will not persist", "error", err)
		} else {
			h.secrets = secrets
		}
	}
	requireCollaborators(h)
	lc.loops.Go(h.cullIdleUtilityBridge)
	lc.loops.Go(h.sweepSessionsLoop)
	return h
}

// UtilityPrompt delegates to the utility text-gen agent, lazily constructing the
// runtime on first call. effort is the per-task reasoning level; "" keeps the
// session's current one, and a model with no effort config ignores it.
func (rt *Runtime) UtilityPrompt(ctx context.Context, prompt string, effort vibekit.EffortLevel) (string, error) {
	return rt.utility.get().textgen.UtilityPrompt(ctx, prompt, effort)
}

// MCPRegistry returns the in-memory registry of connected MCP servers as a
// RouteRegistrar, since route mounting is all the composition root needs.
func (rt *Runtime) MCPRegistry() RouteRegistrar { return rt.mcpRegistry }

// MCPSnapshot returns a stable-ordered snapshot of the runtime registry so callers
// outside agent can read it without taking agent internals as a dependency. Only
// CONNECTED servers are included: a failed or OAuth-pending one has no live tools.
func (rt *Runtime) MCPSnapshot() []vibekit.MCPSnapshotServer {
	snap := rt.mcpRegistry.Snapshot()
	out := make([]vibekit.MCPSnapshotServer, 0, len(snap))
	for i := range snap {
		if snap[i].State != mcpStateConnected {
			continue
		}
		out = append(out, vibekit.MCPSnapshotServer{Name: snap[i].Name})
	}
	return out
}

// SetMCPOnChange wires a callback fired whenever the runtime MCP registry changes.
// Used by main.go to re-run steering.Generate() so environment.md tracks the live
// integration set.
func (rt *Runtime) SetMCPOnChange(fn func()) { rt.mcpRegistry.SetOnChange(fn) }

// SetPreBridgeSpawn wires a callback fired right before any kiro-cli bridge
// starts, used to refresh `environment.md` before kiro-cli reads it during session
// creation. It runs synchronously on the spawn path, so it must be fast; the
// per-request context lets it short-circuit on client disconnection.
//
// The hook lives on the coordinator — its one reader — because a copy held here
// would be captured by newBridgeCoordinator before the composition root sets it,
// and a nil captured at construction is permanent.
func (rt *Runtime) SetPreBridgeSpawn(fn func(context.Context)) { rt.coord.preBridgeSpawn = fn }

// RegisterRoutes wires /api/events (SSE), /api/command (POST), and
// /api/shell/ws (WebSocket PTY).
func (rt *Runtime) RegisterRoutes(mux *http.ServeMux) {
	// Both refuse once Shutdown has flipped draining, and only these two; see
	// refuseWhenDraining.
	mux.Handle("/api/events", rt.refuseWhenDraining(http.HandlerFunc(rt.handleSSE)))
	mux.Handle("/api/command", rt.refuseWhenDraining(rt.dispatcher))
	mux.HandleFunc("/api/shell/ws", rt.shellMgr.handleWS)
	mux.HandleFunc("POST /api/shell/restart", rt.shellMgr.handleRestart)
	mux.HandleFunc("/api/file-changes", rt.handleFileChanges)
	rt.config.registerKnowledgeRoutes(mux)
	rt.config.registerHooksRoutes(mux)
	rt.config.registerGovernanceRoutes(mux)
	rt.runRoutes.register(mux)
	// Pre-session mode + model catalog (kiro-cli 2.14 _kiro/config/template).
	mux.HandleFunc("GET /api/config-template", rt.handleConfigTemplate)
	mux.HandleFunc("GET /api/sessions", rt.handleSessionList)
}

// Shutdown drains in-flight prompts and closes all bridges, bounded by ctx.
//
// Bridges are Stopped BEFORE the inflight wait: a stuck prompt Call returns only
// through its own bridge's teardown, so waiting first deadlocks. ctx is the only
// bound on those waits — webhttp.Run's pre-drain hook is synchronous, so an
// unbounded wait spends the grace the HTTP drain needed — and the error names the
// FIRST wait to exceed the budget.
func (rt *Runtime) Shutdown(ctx context.Context) error {
	slog.Info("agent runtime draining")
	rt.lifecycle.draining.Store(true)

	// 0. Stop background tickers first so they cannot race bridge teardown with a
	//    late cull Stop() or session sweep under the mutex we are about to acquire.
	select {
	case <-rt.lifecycle.done:
		// already closed (re-entrant shutdown in tests)
	default:
		close(rt.lifecycle.done)
	}

	// 1. Stop every bridge so in-flight Calls unblock.
	bridges := rt.bridge.mgr.drain()
	for _, sb := range bridges {
		sb.bridge.Stop()
	}

	// 1a. Cancelled AFTER the drain: drain() empties the map before any Stop, so the
	// death closer is skipped for every bridge this teardown kills, and a bridge
	// dying on its own cannot reach that closer holding a dead context.
	rt.lifecycle.shutdownCancel()

	// 1c. Cancel the push service's request context so pending browser-push round
	// trips unblock rather than draining their 10s client Timeout one by one.
	if rt.push != nil {
		rt.push.Close()
	}

	// Each wait below is bounded by ctx; an expired one is ABANDONED, not cancelled.

	// 2. Wait for in-flight prompt handlers to clean up.
	teardownErr := awaitBounded(ctx, "in-flight handlers", rt.lifecycle.inflight.Wait)

	// 2b. Join the background loops; step 0 signalled them.
	if teardownErr == nil {
		teardownErr = awaitBounded(ctx, "background loops", rt.lifecycle.loops.Wait)
	}

	// 3. Stop utility bridge.
	rt.stopUtilityBridge()

	// 4b. Kill all agent terminal subprocesses. Their exit waiters decrement
	// inflight, so this carries its own bound for the case step 2 did not return.
	if teardownErr == nil {
		teardownErr = awaitBounded(ctx, "agent terminals", rt.agentTerms.drainAll)
	}

	// 5. Tear down shell + SSE clients. These run whatever the budget did:
	// kill signals first and only then waits on ctx.
	rt.shellMgr.kill(ctx)
	rt.bus.fanout.Shutdown()
	if teardownErr != nil {
		return teardownErr
	}
	slog.Info("runtime shutdown complete")
	return nil
}

// awaitBounded runs wait to completion or until ctx expires, naming what was
// still running in the error for the latter.
//
// The goroutine is what makes the bound possible: sync.WaitGroup.Wait and a
// range over exit channels both block unconditionally, so the only way to arm a
// context against either is to move it off the caller's stack.
func awaitBounded(ctx context.Context, what string, wait func()) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		wait()
	}()
	// AwaitDone rather than a two-case select: a wait that finished in the same
	// instant the budget ran out finished, and a bare select reports it as hung
	// a fraction of the time.
	if webhttp.AwaitDone(ctx, done) {
		return nil
	}
	return fmt.Errorf("%s still running: %w", what, ctx.Err())
}

// bridgeIdleTimeout bounds how long the tab-less utility session may sit
// idle before it is stopped. It does NOT apply to chat bridges — those are
// owned by their tab (see cullIdleUtilityBridge).
const bridgeIdleTimeout = 30 * time.Minute

// --- Broadcast ---

// AuthTokenUnavailable reports the last SSO token failure, for the readiness
// endpoint.
func (rt *Runtime) AuthTokenUnavailable() bool {
	if rt.inbound == nil {
		return false
	}
	return rt.inbound.AuthTokenUnavailable()
}

// Broadcast sends a ServerEvent to every connected SSE client.
//
// The one forward to the bus that survives: it is the APP-FACING door.
// Every caller INSIDE this package uses h.bus.Broadcast directly. Used by
// the chat store and by the runtime itself for turn_ended / permission_needed
// / error.
func (rt *Runtime) Broadcast(_ context.Context, evt vibekit.ServerEvent) {
	rt.bus.emit(evt)
}

// refuseWhenDraining answers 503 once Shutdown has flipped draining, for the
// two routes that must stop accepting work before the HTTP drain begins:
// commands and the event stream.
//
// A ROUTE wrapper rather than a member of the global chain, which also covers
// /api/health, /api/version and static assets — none of which should start failing
// while the process winds down. Needed at all because webhttp's own drain gate flips
// LATER, at srv.Shutdown, and the window between the two is the reconnect race.
func (rt *Runtime) refuseWhenDraining(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rt.lifecycle.draining.Load() {
			webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable,
				httpreply.ErrorJSON("shutting down"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sweepSessionsInterval is how often the orphan-session sweep runs after
// its initial boot pass.
const sweepSessionsInterval = 1 * time.Hour

// sweepSessionsLoop runs an initial orphan-session sweep once this process owns
// its config dir, then repeats every sweepSessionsInterval until shutdown. It
// reaps on-disk KAS session state left behind by archived-chat purges, crashes,
// or a pre-v3 install.
func (rt *Runtime) sweepSessionsLoop() {
	if rt.sessionReaper == nil || rt.sessionRefs == nil {
		return
	}
	if !rt.awaitSweepGate() {
		return
	}
	rt.sweepSessionsOnce()
	ticker := time.NewTicker(sweepSessionsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-rt.lifecycle.done:
			return
		case <-ticker.C:
			rt.sweepSessionsOnce()
		}
	}
}

// awaitSweepGate blocks until this process may reap, reporting false when
// shutdown came first. A nil gate proceeds at once (WithSessionSweepGate).
//
// Waiting rather than skipping: the gate closes milliseconds after Build returns,
// so a skip would trade a destructive boot for never reclaiming anything.
func (rt *Runtime) awaitSweepGate() bool {
	if rt.sweepGate == nil {
		return true
	}
	select {
	case <-rt.sweepGate:
		return true
	case <-rt.lifecycle.done:
		return false
	}
}

// sweepSessionsOnce runs one orphan-session sweep. The keep-list is every session in
// every chat's CHAIN union every LIVE session, and both halves are needed: age is not
// evidence that a session is disposable.
//
// The live half exempts a bridge holding a session no chat references, or the sweep
// deletes on-disk KAS state from under a live subprocess once it ages past the
// 10-minute create-race guard. An INCOMPLETE keep-list skips the sweep entirely.
func (rt *Runtime) sweepSessionsOnce() {
	ctx, cancel := rt.lifecycle.derivedContext()
	defer cancel()
	refs, complete := rt.sessionRefs(ctx)
	if !complete {
		slog.Warn("kirosession: skipping orphan sweep, keep-list incomplete (a chat file could not be read)")
		return
	}
	if refs == nil {
		refs = map[string]struct{}{}
	}
	for _, id := range rt.liveSessionIDs() {
		refs[id] = struct{}{}
	}
	rt.sessionReaper.Sweep(refs)
}

// liveSessionIDs returns the ACP session id of every bridge vibekit currently
// holds: each chat bridge plus the utility session. These are exempt from
// the sweep at any age.
func (rt *Runtime) liveSessionIDs() []string {
	var ids []string
	for _, sb := range rt.bridge.mgr.all() {
		if id := string(sb.bridge.SessionID()); id != "" {
			ids = append(ids, id)
		}
	}
	if id := rt.utilityLiveSessionID(); id != "" {
		ids = append(ids, id)
	}
	return ids
}

// utilityLiveSessionID asks the utility session for its live ACP session id,
// or "" when no runtime has been built or it is stopped.
//
// peek, not get: building one to answer would create the session it is
// inspecting.
func (rt *Runtime) utilityLiveSessionID() string {
	u := rt.utility.peek()
	if u == nil {
		return ""
	}
	return u.session.liveID()
}

// stopUtilityBridge stops the utility session if one was built.
//
// take, so the slot is cleared and stopped as one step and nothing else can
// be holding the runtime by the time Stop is called.
func (rt *Runtime) stopUtilityBridge() {
	if u := rt.utility.take(); u != nil {
		u.session.Stop()
	}
}

// RestartUtilitySession drops the utility session so the next use rebuilds
// it.
//
// Its caller is a security-profile change: the presets ride the session
// door, so a session already running still carries the previous profile.
// Recycling is the only way to re-send them, since KAS exposes no method to
// change a live session's policy.
func (rt *Runtime) RestartUtilitySession() { rt.stopUtilityBridge() }

// cullIdleUtilityBridge stops the utility session once it has been idle for
// longer than bridgeIdleTimeout. Runs every 60 seconds.
//
// CHAT bridges are deliberately NOT swept: a chat open in a tab owns its
// process for as long as the tab is open. The utility session is the one
// bridge with no tab to own it.
func (rt *Runtime) cullIdleUtilityBridge() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-rt.lifecycle.done:
			return
		case <-ticker.C:
			rt.cullIdleUtilityBridgeOnce()
		}
	}
}

// cullIdleUtilityBridgeOnce runs one utility-session sweep. peek rather than
// get, because a sweep that BUILT a utility bridge to check whether one was
// idle would be creating work forever.
func (rt *Runtime) cullIdleUtilityBridgeOnce() {
	u := rt.utility.peek()
	if u != nil && u.session.stopIfIdle(time.Now().Add(-bridgeIdleTimeout)) {
		slog.Info("culled idle utility bridge")
	}
}
