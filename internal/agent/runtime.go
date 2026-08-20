// Package hub coordinates the server's per-chat runtime: SSE fan-out,
// ACP bridge lifecycle, and POST /api/command dispatch — plus the
// service surfaces that ride the shared utility bridge (knowledge,
// specs, hooks, governance, account usage, policy), checkpoint HTTP,
// agent terminals, the browser PTY shell shim, and the MCP runtime
// registry.
//
// This file defines Runtime and its top-level wiring. Command dispatch is
// hosted via internal/command (adapters in command_deps.go), ACP
// translation via internal/translate (adapters in translate_deps.go),
// SSE transport in sse.go, bridge lifecycle in bridge_lifecycle.go and
// bridge_coord.go, the shell shim in shell.go, agent terminals in
// agent_terminal.go, utility-bridge services in knowledge.go / spec.go /
// hooks.go / governance.go / account_usage.go / permissions_policy.go,
// and checkpoint HTTP in checkpoint_http.go.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
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
	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp"
	"github.com/cplieger/webhttp/sse"
)

const (
	replayBufSize = 1024

	// keepaliveInterval is the shared interval for SSE keepalive
	// comments and WebSocket pings. iOS Safari kills idle connections
	// after ~30s in background; 15s keeps both transports alive.
	keepaliveInterval = 15 * time.Second

	// outputBufferLimit is the byte budget for subprocess output ring
	// buffers (agent terminals and the PTY shell scrollback). 64 KB
	// covers a full terminal screen at 200 cols × 50 rows with
	// generous ANSI escapes.
	outputBufferLimit = buffer.DefaultOutputCap
)

// lifetime groups Runtime fields related to process lifecycle,
// shutdown coordination, and workspace paths.
type lifetime struct {
	// shutdownCtx is the runtime's own cancellable child of the lifetime context
	// New requires, and it is a lifetime HANDLE rather than a stashed caller
	// context: it is read by the shell manager, the agent terminals, the
	// projection writer and every Broadcast, none of which a run-method
	// parameter could reach. Shutdown() cancels it, so the runtime can be torn
	// down on its own without the app's lifetime ending — which is what
	// App.Shutdown relies on.
	//
	// It used to be rooted at context.Background() inside New, which is why
	// callers had to fetch it back out through an exported ShutdownCtx()
	// accessor. That accessor is gone: the composition root owns the app's
	// lifetime and hands the same one to every component, and the one consumer
	// that needed a context DERIVED from this asks for that instead (see
	// TurnContext).
	shutdownCtx    context.Context
	done           chan struct{}
	shutdownCancel context.CancelFunc
	workDir        string
	configDir      string
	inflight       sync.WaitGroup
	// loops covers the background goroutines New starts, which exit on done.
	// It is a SEPARATE group from inflight and must stay one: Shutdown waits on
	// inflight only after every bridge has been stopped, and the two groups are
	// reported apart so a shutdown that times out names a wedged handler or a
	// wedged loop rather than "something".
	loops    sync.WaitGroup
	mu       sync.Mutex
	draining atomic.Bool
}

// derivedContext returns a cancellable child of the process lifetime, for work
// that must outlive the request that started it. Runtime's hubContext() is the same
// two lines; this is the copy the planes use, so a plane does not need a *Runtime to
// ask a question about the lifetime this struct owns.
func (lc *lifetime) derivedContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(lc.shutdownCtx)
}

// TurnContext returns the context a turn runs under, plus the teardown its
// handler must defer.
//
// It replaced an exported ShutdownCtx() accessor, and the replacement is the
// point: a command handler never wanted the raw lifetime context, it wanted a
// turn context derived from it, and handing out the lifetime made every consumer
// responsible for deriving one correctly. This plane is what holds the lifetime,
// so the derivation lives on it. It was a Runtime method forwarding to
// h.lifecycle.shutdownCtx, which put the runtime in the path of a question only this
// struct's own fields can answer.
//
// The turn is DETACHED from reqCtx's cancellation while keeping its values: the
// prompt POST's context dies when the handler returns, and a turn that died with
// it failed before it could finalize and persist the assistant buffer, even
// though kiro-cli kept running the turn to completion. Cancellation is
// re-attached to the shutdown context via AfterFunc so the turn still dies on
// shutdown; the returned cancel tears it down on handler return and unregisters
// that AfterFunc so it cannot leak. Explicit user cancellation is unaffected —
// it goes through session/cancel (Notify), not this context.
//
// This mirrors the pattern in agent_terminal.go, which runs agent-spawned
// subprocesses under context.WithCancel(context.WithoutCancel(ctx)) +
// AfterFunc(shutdownCtx, cancel) for the same reason: a per-request ctx must not
// tear down longer-lived work.
func (lc *lifetime) TurnContext(reqCtx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(reqCtx))
	stop := context.AfterFunc(lc.shutdownCtx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// InflightAdd increments the inflight counter Shutdown waits on.
func (lc *lifetime) InflightAdd(delta int) {
	lc.inflight.Add(delta)
}

// InflightDone decrements the inflight counter.
func (lc *lifetime) InflightDone() {
	lc.inflight.Done()
}

// bridges groups Runtime fields related to ACP bridge management.
//
// The utility runtime left: it was a field plus a sync.Once here, guarded by the
// process-lifetime mutex at its three readers and by nothing at its writer. It is
// utilityLease now, which owns its own lock — see utility_lease.go.
type bridges struct {
	factory       ACPBridgeFactory
	mgr           *bridgeManager
	assistantBufs *buffer.Store
}

// bus groups Runtime fields related to SSE transport, replay,
// and pending permissions. The transport (fan-out, replay
// ring, Last-Event-ID resume, keepalives, eviction) is webhttp/sse's hub;
// vibekit layers chat-topic filtering and pending-state replay on top.
type bus struct {
	hub          *sse.Hub
	pendingPerms *pendingPermsTracker
	// chatStatus holds each chat's last self-declared status, the one
	// turn_state input that lives on no message and in no replay
	// (chat_status.go). The in-flight MESSAGE comes from the assistant
	// buffer, not from a replica.
	chatStatus *chatStatusCache
}

// perms groups Runtime fields related to permissions and supervision.
type perms struct {
	ignore *ignore.Matcher
}

// Runtime is the central coordinator.
type Runtime struct {
	lifecycle *lifetime
	bridge    *bridges
	bus       *bus
	perm      *perms
	coord     *BridgeCoordinator

	push               pushService
	chatStore          chatRecords
	mcpConfig          mcpNameSets
	mcpRegistry        *mcpRegistry
	shellMgr           *ShellManager
	kiroToken          *kiroauth.CLISource
	preBridgeSpawn     func(context.Context) // optional; fired before each new bridge starts
	chatHandlers       map[string]chatHandler
	sessUpdateHandlers map[vibekit.ACPUpdateKind]sessionUpdateHandler
	noopMethods        map[string]struct{}
	dispatcher         *command.Dispatcher
	translator         *translate.Translator
	// config owns the KAS configuration surface (knowledge, hooks, governance,
	// policy), all of it over the utility bridge. See config_plane.go.
	config *Settings
	// runs owns the workflow-run surface: 74 methods and the four fields only it
	// touched (see run_plane.go). Runtime reaches it like any collaborator.
	runs          *Runs
	utility       *utilityLease
	sessionReaper *kirosession.Reaper
	sessionRefs   func(context.Context) (map[string]struct{}, bool)
	lines         *buffer.LineTracker
	agentTerms    *agentTerminals
	hookStatus    *hookStatusCache
	governance    *governanceCache
	// authLatch remembers the last outcome of vending a KAS access token, so
	// readiness can report a dead sign-in without asking kiro-cli (see
	// bridge_v3_auth.go).
	authLatch *authTokenLatch

	// secrets holds the credential blobs KAS asks vibekit to persist on its
	// behalf (_kiro/secret/*, bridge_v3_secret.go). ONE store for every
	// bridge: KAS's key namespace is global, so sharing it is what lets a
	// second bridge reuse the first one's MCP registration. Nil in tests and
	// when no configDir is set → the handlers report "absent", which degrades
	// to the pre-capability behaviour rather than failing an MCP connect.
	secrets *secretstore.Store

	// In-flight session/load replay projections (load_projection.go).
	// Embedded ahead of the scalars below to keep govet fieldalignment happy:
	// it carries pointers, so it must not sit after ciBusy.
	projectionState

	// Code-intelligence activation inputs + in-flight guard (code_intel.go).
	ciGate func() bool
	ciPath string
	// acpArgs are the filtered operator kiro-cli launch flags
	// (VIBEKIT_KIRO_ACP_ARGS via WithACPArgs). Chat bridges only. Ordered last
	// among the pointer-bearing fields for govet fieldalignment: a slice is 8
	// of 24 pointer bytes, less dense than a string's 8 of 16.
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

// WithACPArgs sets the operator-supplied kiro-cli launch flags appended to
// every CHAT bridge's argv. Pass them already filtered (bridge.ParseACPArgs);
// the runtime does not re-validate. Never reaches the utility bridge — see
// BridgeCoordinator.acpArgs.
func WithACPArgs(args []string) Option {
	return func(h *Runtime) { h.acpArgs = args }
}

// WithSchedules wires the workflow-schedule store. Absent, the schedule routes
// are not registered at all and nothing fires — scheduling is off rather than
// half-present.
func WithSchedules(st *schedule.Store) Option {
	return func(h *Runtime) { h.runs.schedules = st }
}

// WithRunLeases wires the DURABLE run-lease store.
//
// Unlike WithSchedules, absent does not mean off: the runtime falls back to an
// in-memory registry (leaseStore), because a lease carries the run's wall clock
// and its unattended mark. What this option adds is survival across a restart —
// which is the whole point of the record, so production always wires it.
func WithRunLeases(st *runlease.Store) Option {
	return func(h *Runtime) { h.runs.leases = st }
}

// WithPush wires the push notification service at construction time.
func WithPush(p pushService) Option {
	return func(h *Runtime) { h.push = p }
}

// WithMCPConfig wires the MCP configuration store. The hub reads the three
// name sets to classify a status notification's origin and to drop the frames
// of a server the user switched off.
//
// It used to read an ACPServers() that no longer exists: vibekit stopped sending
// servers inline and adopted KAS's own mcp.json, leaving `mcpServers` on the
// session methods as a required-but-empty array (bridge_session.go). The comment
// outlived the method.
func WithMCPConfig(c mcpNameSets) Option {
	return func(h *Runtime) { h.mcpConfig = c }
}

// WithKiroCLIPath wires the v3 auth-callback token source over the active
// kiro-cli binary. resolve is the install manager's path resolver (same
// contract as the bridge factory's cliPath: "" while nothing is installed),
// so a version switch reaches the next callback; env is the manager's PATH
// overlay (kiro-cli resolves its kiro-cli-chat sidecar by bare name on
// PATH, so the overlay is required, same as bridge spawns). Unset in tests
// → the auth callback answers with an RPC error instead of vending a token.
func WithKiroCLIPath(resolve func() string, env func() []string) Option {
	return func(h *Runtime) { h.kiroToken = kiroauth.NewCLISource(resolve, env) }
}

// WithSessionReaper wires the KAS session reaper and the referenced-session
// thunk. The reaper removes on-disk kiro-cli/KAS session state: promptly on
// chat delete (via cleanupChatState) and via a periodic orphan sweep that
// spares any session id refs reports as still referenced by a chat.
// Unset in tests → session reaping is a no-op.
//
// refs returns (set, complete). A false `complete` means the keep-list could
// not be fully determined, and the sweep is SKIPPED rather than run against a
// partial one — see sweepSessionsOnce.
func WithSessionReaper(r *kirosession.Reaper, refs func(context.Context) (map[string]struct{}, bool)) Option {
	return func(h *Runtime) {
		h.sessionReaper = r
		h.sessionRefs = refs
	}
}

// New constructs a Runtime. Bridges spawn with a fixed kiro-cli acp arg set
// (agent engine + model + effort); tool-call authorization is owned by
// kiro-cli's native Cedar policy on v3, not by CLI trust flags.
//
// ctx is the runtime's LIFETIME and is required. The hub has no run method to take
// it — it is handed to server.New as a route provider and the process then
// blocks in ListenAndServe — so the fleet's rule for that case applies: require
// the lifetime at construction rather than default it. There is deliberately no
// nil check and no WithLifetime option: a nil ctx panics in context.WithCancel
// below, at the one construction site, which is the refusal; an option would be
// a default wearing a nicer name, and every default for a lifetime is a
// lifetime nothing can cancel.
//
// New does NOT take ownership of ctx's cancellation. Shutdown cancels the runtime's
// own child of it, so the caller may tear the runtime down first and end the app's
// lifetime afterwards.
//
// chatStore is REQUIRED, on the same terms as ctx: it is read here to wire the
// translator, so a nil one builds a runtime that cannot serve a single chat and
// defers the crash to the first ACP frame. requireWired refuses it at
// construction, which is where a caller can still fix it.
func New(ctx context.Context, workDir string, factory ACPBridgeFactory, chatStore chatRecords, opts ...Option) *Runtime {
	sseHub := sse.NewHub(sse.WithReplay(replayBufSize), sse.WithKeepalive(keepaliveInterval))
	lc := &lifetime{
		workDir: workDir,
		done:    make(chan struct{}),
	}
	lc.shutdownCtx, lc.shutdownCancel = context.WithCancel(ctx)

	// The planes are locals first so the run plane can name the two it depends on
	// (the bridge registry and the pending-decision tracker) rather than reaching
	// them through a *Runtime. Its remaining three collaborators do not exist yet and
	// are assigned below; those assignments are the honest back-edges, in one
	// place, instead of a back-pointer that hides them.
	bridgeP := &bridges{
		factory:       factory,
		mgr:           newBridgeManager(factory),
		assistantBufs: buffer.NewStore(),
	}
	sseP := &bus{
		hub:          sseHub,
		pendingPerms: newPendingPermsTracker(),
		chatStatus:   newChatStatusCache(),
	}
	configP := newSettings(lc, nil) // broadcast assigned below, with the rest
	runs := &Runs{
		bridges:   bridgeP.mgr,
		lifecycle: lc,
		chats:     chatStore,
		perms:     sseP,
	}

	h := &Runtime{
		lifecycle:    lc,
		bridge:       bridgeP,
		bus:          sseP,
		runs:         runs,
		config:       configP,
		perm:         &perms{},
		chatStore:    chatStore,
		hookStatus:   newHookStatusCache(kiroSettingsPath()),
		authLatch:    &authTokenLatch{},
		chatHandlers: make(map[string]chatHandler),
		noopMethods:  make(map[string]struct{}),
	}
	// Options may write the run plane's two stores (WithSchedules, WithRunLeases),
	// so they run after it exists and before anything reads it.
	for _, o := range opts {
		o(h)
	}
	// CONSTRUCTION, then WIRING, in that order and not interleaved.
	//
	// Every role below is bound to its owner BY VALUE, so a field still nil at the
	// literal stays nil forever — the reads no longer happen per call the way they
	// did when every role named h. Three roles (coord, lines, agentTerms) were
	// assigned after the translator and captured nil until this was reordered.
	// TestNew_EveryTranslateRoleIsWired pins it.
	h.utility = &utilityLease{build: h.buildUtility}
	h.mcpRegistry = newMCPRegistry(bridgeP.mgr, sseP, lc, h.mcpConfig)
	h.coord = newBridgeCoordinator(h)
	h.shellMgr = NewShellManager(lc.shutdownCtx, workDir)
	h.lines = buffer.NewLineTracker()
	h.agentTerms = newAgentTerminals(bridgeP.mgr, lc, sseP.Broadcast)
	// A settled session/load replay becomes the chat's transcript. Assigned here
	// (not in the struct literal) because it is a method value on the fully-built
	// Runtime; see load_projection.go.
	h.onProjection = h.swapProjectedTranscript
	// ensureUtility is a thunk, not a value: the utility runtime is built under a
	// sync.Once whose hooks call back into hub surfaces, so a holder must ask for
	// it at use rather than hold one built here.
	runs.utility = h.ensureUtility
	runs.coord = h.coord
	configP.utility = h.ensureUtility
	configP.broadcast = sseP.Broadcast

	h.translator = translate.New(h.translateRoles())
	runs.translate = h.translator
	h.dispatcher = command.New()
	h.registerCommandHandlers()
	h.initDispatch()
	if lc.configDir != "" {
		h.perm.ignore = ignore.NewMatcher(lc.configDir, workDir)
		// Best-effort: a store that cannot be opened leaves h.secrets nil, and
		// bridges then do NOT declare `_meta.kiro.secretStorage` (see
		// vibekit.StartOpts.SecretStorage), so KAS never asks and MCP OAuth
		// re-registers per spawn as it did before the capability, rather than
		// the runtime refusing to construct over a credential cache.
		secrets, err := secretstore.New(lc.configDir)
		if err != nil {
			slog.Error("secretstore: open failed; MCP credentials will not persist", "error", err)
		} else {
			h.secrets = secrets
		}
	}
	lc.loops.Go(h.cullIdleUtilityBridge)
	lc.loops.Go(h.sweepSessionsLoop)
	return h
}

// UtilityPrompt delegates to the utility text-gen agent. The runtime is lazily
// constructed on first call. effort is the per-task reasoning-effort level:
// cheap tasks (summaries, error explanations) pass vibekit.EffortLow, tasks that
// read a diff or merge code (commit messages, PR descriptions, conflict
// resolution) pass vibekit.EffortMedium; "" keeps the session's current level.
// Best-effort — a model with no effort config ignores it.
//
// Its consumers declare the one-method contract themselves (internal/server and
// internal/git); this is not used for chat titles, which come from KAS (see
// translate/focus.go).
func (h *Runtime) UtilityPrompt(ctx context.Context, prompt string, effort vibekit.EffortLevel) (string, error) {
	return h.ensureUtility().textgen.UtilityPrompt(ctx, prompt, effort)
}

// MCPRegistry returns the in-memory registry of currently-connected MCP servers
// as a RouteRegistrar — route mounting is the only surface the composition root
// needs from it, and returning the concrete type would leak an unexported name
// from an exported method.
func (h *Runtime) MCPRegistry() RouteRegistrar { return h.mcpRegistry }

// MCPSnapshot returns a stable-ordered snapshot of the runtime registry
// so callers outside hub (e.g. the steering generator) can read it
// without taking hub internals as a dependency. Only servers in the
// connected state are included: the steering file presents the list as
// "Connected integrations", and a failed or OAuth-pending server has no
// live tools for the agent to call.
func (h *Runtime) MCPSnapshot() []vibekit.MCPSnapshotServer {
	snap := h.mcpRegistry.Snapshot()
	out := make([]vibekit.MCPSnapshotServer, 0, len(snap))
	for i := range snap {
		if snap[i].State != mcpStateConnected {
			continue
		}
		out = append(out, vibekit.MCPSnapshotServer{Name: snap[i].Name})
	}
	return out
}

// SetMCPOnChange wires a callback fired whenever the runtime MCP
// registry changes (server connected, OAuth needed, bridges closed).
// Used by main.go to re-run steering.Generate() so environment.md
// tracks the live integration set.
func (h *Runtime) SetMCPOnChange(fn func()) { h.mcpRegistry.SetOnChange(fn) }

// SetPreBridgeSpawn wires a callback fired right before any kiro-cli
// bridge starts (both fresh `session/new` and `session/load` paths in
// getOrCreateBridge). Used to refresh `environment.md` so the latest
// per-repo steering inventory is on disk by the time kiro-cli reads
// it during session creation.
//
// The callback receives the per-request context so it can short-circuit
// on client disconnection. It runs synchronously on the spawn path, so
// it must be fast (the existing steering.Generate is bounded by the
// workspace walk + skip-if-unchanged write — typically a few ms).
func (h *Runtime) SetPreBridgeSpawn(fn func(context.Context)) { h.preBridgeSpawn = fn }

// RegisterRoutes wires /api/events (SSE), /api/command (POST), and
// /api/shell/ws (WebSocket PTY).
func (h *Runtime) RegisterRoutes(mux *http.ServeMux) {
	// Both of these refuse once Shutdown has flipped draining, and only these
	// two: see refuseWhenDraining for why the gate is a route wrapper rather
	// than a member of the global middleware chain.
	mux.Handle("/api/events", h.refuseWhenDraining(http.HandlerFunc(h.handleSSE)))
	mux.Handle("/api/command", h.refuseWhenDraining(h.dispatcher))
	mux.HandleFunc("/api/shell/ws", h.handleShellWS)
	mux.HandleFunc("POST /api/shell/restart", h.handleShellRestart)
	mux.HandleFunc("/api/file-changes", h.handleFileChanges)
	h.config.registerKnowledgeRoutes(mux)
	h.config.registerHooksRoutes(mux)
	h.config.registerGovernanceRoutes(mux)
	// Pre-session mode + model catalog (kiro-cli 2.14 _kiro/config/template).
	mux.HandleFunc("GET /api/config-template", h.handleConfigTemplate)
	mux.HandleFunc("GET /api/sessions", h.handleSessionList)
	mux.HandleFunc("GET /api/runs/{id}", h.runs.handleRun)
	mux.HandleFunc("POST /api/runs", h.runs.handleRunLaunch)
	mux.HandleFunc("POST /api/runs/{id}/cancel", h.runs.handleRunCancel)
	mux.HandleFunc("POST /api/runs/{id}/pause", h.runs.handleRunPause)
	mux.HandleFunc("POST /api/runs/{id}/resume", h.runs.handleRunResume)
	mux.HandleFunc("POST /api/runs/{id}/retry", h.runs.handleRunRetry)
	mux.HandleFunc("DELETE /api/runs/{id}", h.runs.handleRunDelete)
	mux.HandleFunc("POST /api/runs/{id}/step", h.runs.handleRunStepStatus)
	mux.HandleFunc("GET /api/recipes", h.runs.handleRecipes)
	h.runs.registerScheduleRoutes(mux)
}

// Shutdown drains in-flight prompts and closes all bridges, bounded by ctx.
//
// Bridges are Stopped BEFORE the inflight wait: a stuck prompt Call returns
// only through its own bridge's teardown, so waiting first deadlocks.
//
// ctx is the only bound on those waits, and it must be: webhttp.Run's pre-drain
// hook, where this runs, is synchronous, so an unbounded wait spends the grace
// the HTTP drain needed. The error names the FIRST wait to exceed the budget.
func (h *Runtime) Shutdown(ctx context.Context) error {
	slog.Info("hub draining")
	h.lifecycle.draining.Store(true)

	// 0. Stop background tickers first so they can't race bridge
	//    teardown with a late cull Stop() or session sweep under the
	//    mutex we're about to acquire.
	select {
	case <-h.lifecycle.done:
		// already closed (re-entrant shutdown in tests)
	default:
		close(h.lifecycle.done)
	}
	h.lifecycle.shutdownCancel()

	// 1. Stop every bridge so in-flight Calls unblock.
	bridges := h.bridge.mgr.drain()
	for _, sb := range bridges {
		sb.bridge.Stop()
	}

	// 1b. There is no pending-write flush at shutdown any more. It existed because a
	// staged write blocked an fs handler until a human answered, and a shutting-down
	// server could never deliver that answer — so every such handler had to be
	// unblocked with accepted=false. KAS holds the writes now, so no vibekit
	// goroutine is parked waiting on a verdict and there is nothing to unblock.

	// 1c. Cancel the push service's request context so any pending
	// browser-push HTTP round-trips unblock via context cancellation
	// rather than draining their 10s client Timeout one-by-one. The
	// fan-out sites (bridge_buffer, bridge_fs, translate_permission)
	// launch Send goroutines under h.lifecycle.inflight; without this step
	// inflight.Wait below would serialise across their full timeout
	// budget on a wedged vendor.
	if h.push != nil {
		h.push.Close()
	}

	// Each wait below is bounded by ctx, and an expired one is ABANDONED rather
	// than cancelled: the process is exiting, and the runtime reclaims it.

	// 2. Wait for in-flight prompt handlers to clean up.
	teardownErr := awaitBounded(ctx, "in-flight handlers", h.lifecycle.inflight.Wait)

	// 2b. Join the background loops New started. Step 0 signalled them; this is
	// the wait that makes "the runtime has stopped" true of them.
	if teardownErr == nil {
		teardownErr = awaitBounded(ctx, "background loops", h.lifecycle.loops.Wait)
	}

	// 3. Stop utility bridge.
	h.stopUtilityBridge()

	// 4b. Kill all agent terminal subprocesses. Their exit waiters decrement
	// inflight, so this is instant once step 2 returned — and carries its own
	// bound for the case where step 2 did not.
	if teardownErr == nil {
		teardownErr = awaitBounded(ctx, "agent terminals", h.agentTerms.drainAll)
	}

	// 5. Tear down shell + SSE clients. These run whatever the budget did: the
	// PTY child and the SSE clients must be told to go even when nothing is left
	// to watch them leave with — kill signals first and only then waits on ctx.
	h.shellMgr.kill(ctx)
	h.bus.hub.Shutdown()
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

// Broadcast sends a ServerEvent to every connected SSE client.
//
// The one forward to the bus that survives, and deliberately: it is the
// APP-FACING door. internal/server wants RegisterRoutes, Broadcast and Shutdown
// off one value, and internal/composition publishes prewarm and tool-job events;
// making either hop through an accessor to reach a one-method contract would add
// a call that carries no information. Every caller INSIDE this package uses
// h.bus.Broadcast, because in here the bus is right there and routing through the
// runtime is the topology this refactor removes. Used by the
// chat store (chat_created / chat_updated / message_* etc.) and by the runtime
// itself for turn_ended / permission_needed / error.
func (h *Runtime) Broadcast(_ context.Context, evt vibekit.ServerEvent) {
	h.bus.emit(evt)
}

// refuseWhenDraining answers 503 once Shutdown has flipped draining, for the two
// routes that must stop accepting work before the HTTP drain begins: commands and
// the event stream.
//
// It is a ROUTE wrapper, not a member of the global middleware chain, because the
// chain covers /api/health, /api/version and the static assets too and none of
// those should start failing while the process winds down — a health probe in
// particular is what reports the wind-down.
//
// It is needed at all because webhttp's own drain gate flips LATER: draining goes
// true at the start of hub.Shutdown, the library's gate when srv.Shutdown begins,
// and the window between the two is the last-instant-reconnect race. The library
// gate remains the backstop after this one.
func (h *Runtime) refuseWhenDraining(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.lifecycle.draining.Load() {
			webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable,
				httpreply.ErrorJSON("shutting down"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hubContext returns a context derived from the runtime's shutdownCtx.
// Cancelled when Shutdown fires. Replaces the per-event goroutine
// pattern (context.WithCancel + go select{<-h.lifecycle.done}) with a zero-
// allocation child context.
func (h *Runtime) hubContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(h.lifecycle.shutdownCtx)
}

// sweepSessionsInterval is how often the orphan-session sweep runs after
// its initial boot pass.
const sweepSessionsInterval = 1 * time.Hour

// sweepSessionsLoop runs an initial orphan-session sweep at startup, then
// repeats every sweepSessionsInterval until shutdown. It reaps on-disk KAS
// session state left behind by archived-chat purges, crashes, or a pre-v3
// install — the direct Reap on chat delete handles the common case. The
// goroutine exits immediately when the reaper is unwired (e.g. tests), and
// on h.lifecycle.done otherwise.
func (h *Runtime) sweepSessionsLoop() {
	if h.sessionReaper == nil || h.sessionRefs == nil {
		return
	}
	h.sweepSessionsOnce()
	ticker := time.NewTicker(sweepSessionsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.lifecycle.done:
			return
		case <-ticker.C:
			h.sweepSessionsOnce()
		}
	}
}

// sweepSessionsOnce runs one orphan-session sweep. The keep-list is
//
//	every session in every active + archived chat's CHAIN  ∪  every LIVE session
//
// and both halves are needed for the same reason: age is not evidence that a
// session is disposable.
//
// The live half used to be one ad-hoc exemption for the utility bridge, whose
// own comment named the failure mode — without it the sweep deletes on-disk
// KAS state from under a live subprocess once it ages past the 10-minute
// guard, because that guard is a create-race cushion and not a liveness test.
// Any bridge holding a session that no chat references hits the same bug, so
// the exemption is now general rather than a special case beside a gap.
//
// A sweep is SKIPPED entirely when the keep-list is incomplete. Sweeping with
// a partial list deletes the sessions of whatever chat could not be read; not
// sweeping only postpones reclaiming disk until the next hour.
func (h *Runtime) sweepSessionsOnce() {
	ctx, cancel := h.hubContext()
	defer cancel()
	refs, complete := h.sessionRefs(ctx)
	if !complete {
		slog.Warn("kirosession: skipping orphan sweep, keep-list incomplete (a chat file could not be read)")
		return
	}
	if refs == nil {
		refs = map[string]struct{}{}
	}
	for _, id := range h.liveSessionIDs() {
		refs[id] = struct{}{}
	}
	h.sessionReaper.Sweep(refs)
}

// liveSessionIDs returns the ACP session id of every bridge vibekit currently
// holds: each chat bridge plus the utility session. These are exempt from the
// sweep at any age — a live subprocess is using the directory.
func (h *Runtime) liveSessionIDs() []string {
	var ids []string
	for _, sb := range h.bridge.mgr.all() {
		if id := string(sb.bridge.SessionID()); id != "" {
			ids = append(ids, id)
		}
	}
	if id := h.utilityLiveSessionID(); id != "" {
		ids = append(ids, id)
	}
	return ids
}

// utilityLiveSessionID asks the utility session for its live ACP session id, or
// "" when no runtime has been built or it is stopped.
//
// peek, not get: this is the orphan-session sweep asking whether a utility
// session's id must be spared, and building one to answer would create the
// session it is inspecting. It was also the goroutine on the losing side of the
// race — it read the field under the lifetime mutex while the builder wrote it
// under a sync.Once, which orders nothing against a reader that never builds.
func (h *Runtime) utilityLiveSessionID() string {
	u := h.utility.peek()
	if u == nil {
		return ""
	}
	return u.session.liveSessionID()
}

// stopUtilityBridge stops the utility session if one was built.
//
// take, so the slot is cleared and stopped as one step and nothing else can be
// holding the runtime by the time Stop is called. Only reached from Shutdown,
// after the inflight wait, so an in-flight turn holds a lease on the session
// being stopped only when that wait ran out of budget — the same degradation the
// cull path takes, where a lease-held chunk channel just closes.
func (h *Runtime) stopUtilityBridge() {
	if u := h.utility.take(); u != nil {
		u.session.Stop()
	}
}

// cullIdleUtilityBridge stops the utility session once it has been idle
// for longer than bridgeIdleTimeout. Runs every 60 seconds. Exits on
// Shutdown via h.lifecycle.done so a late tick can't race teardown.
//
// CHAT bridges are deliberately NOT swept. A chat open in a tab owns its
// process for as long as the tab is open: nothing auto-kills the process,
// its turn, or its runs, and closing the tab kills all of it. The utility
// session is the one bridge with no tab to own it — it is a pool of one-shot
// text generators — so an idle timer is the right bound there and only there.
func (h *Runtime) cullIdleUtilityBridge() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-h.lifecycle.done:
			return
		case <-ticker.C:
			h.cullIdleUtilityBridgeOnce()
		}
	}
}

// cullIdleUtilityBridgeOnce runs one utility-session sweep. stopIfIdle owns the
// victim-capture dance (the session mutex is short-held, so this never waits
// behind an in-flight text turn; acquire bumps lastActiveAt at turn start, so a
// live turn is trivially "recently active"). peek rather than get, because a
// sweep that BUILT a utility bridge in order to check whether one was idle would
// be creating work forever. Lowercase rather than inlined so the sweep is
// testable without driving a real ticker.
func (h *Runtime) cullIdleUtilityBridgeOnce() {
	u := h.utility.peek()
	if u != nil && u.session.stopIfIdle(time.Now().Add(-bridgeIdleTimeout)) {
		slog.Info("culled idle utility bridge")
	}
}
