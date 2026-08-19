// Package hub coordinates the server's per-chat runtime: SSE fan-out,
// ACP bridge lifecycle, and POST /api/command dispatch — plus the
// service surfaces that ride the shared utility bridge (knowledge,
// specs, hooks, governance, account usage, policy), checkpoint HTTP,
// agent terminals, the browser PTY shell shim, and the MCP runtime
// registry.
//
// This file defines Hub and its top-level wiring. Command dispatch is
// hosted via internal/command (adapters in command_deps.go), ACP
// translation via internal/translate (adapters in translate_deps.go),
// SSE transport in sse.go, bridge lifecycle in bridge_lifecycle.go and
// bridge_coord.go, the shell shim in shell.go, agent terminals in
// agent_terminal.go, utility-bridge services in knowledge.go / spec.go /
// hooks.go / governance.go / account_usage.go / permissions_policy.go,
// and checkpoint HTTP in checkpoint_http.go.
package hub

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/dedup"
	"github.com/cplieger/vibekit/internal/ignore"
	"github.com/cplieger/vibekit/internal/kiroauth"
	"github.com/cplieger/vibekit/internal/kirosession"
	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/schedule"
	"github.com/cplieger/vibekit/internal/secretstore"
	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
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

// lifecyclePlane groups Hub fields related to process lifecycle,
// shutdown coordination, and workspace paths.
type lifecyclePlane struct {
	// shutdownCtx is the hub's own cancellable child of the lifetime context
	// New requires, and it is a lifetime HANDLE rather than a stashed caller
	// context: it is read by the shell manager, the agent terminals, the
	// projection writer and every Broadcast, none of which a run-method
	// parameter could reach. Shutdown() cancels it, so the hub can be torn
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
	mu             sync.Mutex
	draining       atomic.Bool
}

// bridgePlane groups Hub fields related to ACP bridge management
// and the utility runtime (session + text-gen agent).
type bridgePlane struct {
	factory       ACPBridgeFactory
	mgr           *bridgeManager
	assistantBufs *buffer.Store
	utility       *utilityRuntime
	utilityOnce   sync.Once
}

// ssePlane groups Hub fields related to SSE transport, replay,
// idempotency, and pending permissions. The transport (fan-out, replay
// ring, Last-Event-ID resume, keepalives, eviction) is webhttp/sse's hub;
// vibekit layers chat-topic filtering and pending-state replay on top.
type ssePlane struct {
	hub          *sse.Hub
	idempotency  *dedup.Cache
	pendingPerms *pendingPermsTracker
	// chatStatus holds each chat's last self-declared status, the one
	// turn_state input that lives on no message and in no replay
	// (chat_status.go). The in-flight MESSAGE comes from the assistant
	// buffer, not from a replica.
	chatStatus *chatStatusCache
}

// permPlane groups Hub fields related to permissions and supervision.
type permPlane struct {
	ignore *ignore.Matcher
}

// Hub is the central coordinator.
type Hub struct {
	lifecycle *lifecyclePlane
	bridge    *bridgePlane
	sse       *ssePlane
	perm      *permPlane
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
	schedules          *schedule.Store
	// leases is what vibekit knows about the runs it put on the wire: the
	// envelope around a run KAS owns (see internal/runlease). Reach it through
	// leaseStore(), which supplies an in-memory registry when the durable store
	// was not wired — a lease carries the run's wall clock, so there is no
	// "leases off" mode the way there is a "scheduling off" mode.
	leases        *runlease.Store
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
	// runBounds holds the ceiling arms, the termination claims and the recorded
	// abnormal terminations that let a run's row say what actually happened to it.
	// See run_bounds.go.
	//
	// It carries pointers (three maps and a slice) but ENDS in a plain counter, so
	// govet fieldalignment wants it after the pointer-only fields above rather than
	// among them: embedded higher up, that trailing word sits inside the struct's
	// pointer prefix and costs 8 bytes of scan.
	runBounds runBoundsState
	ciBusy    atomic.Bool
	// unattendedMu guards runBounds. Non-pointer, so it sits in the tail with
	// ciBusy rather than among the pointer fields.
	//
	// It used to guard a second map, unattendedRuns, which held a run's schedule
	// mark; that fact lives on the run's lease now, and the lease store carries its
	// own mutex. Where the deadline callback needs BOTH atomically it takes this
	// one first and the store's second (claimExpiredDeadline), which is the only
	// place two are held and the only order they are ever taken in.
	unattendedMu sync.Mutex
}

// Option configures optional Hub parameters.
type Option func(*Hub)

// WithConfigDir sets the configuration directory for permissions,
// checkpoints, and ignore rules.
func WithConfigDir(dir string) Option {
	return func(h *Hub) { h.lifecycle.configDir = dir }
}

// WithACPArgs sets the operator-supplied kiro-cli launch flags appended to
// every CHAT bridge's argv. Pass them already filtered (bridge.ParseACPArgs);
// the hub does not re-validate. Never reaches the utility bridge — see
// BridgeCoordinator.acpArgs.
func WithACPArgs(args []string) Option {
	return func(h *Hub) { h.acpArgs = args }
}

// WithSchedules wires the workflow-schedule store. Absent, the schedule routes
// are not registered at all and nothing fires — scheduling is off rather than
// half-present.
func WithSchedules(st *schedule.Store) Option {
	return func(h *Hub) { h.schedules = st }
}

// WithRunLeases wires the DURABLE run-lease store.
//
// Unlike WithSchedules, absent does not mean off: the hub falls back to an
// in-memory registry (leaseStore), because a lease carries the run's wall clock
// and its unattended mark. What this option adds is survival across a restart —
// which is the whole point of the record, so production always wires it.
func WithRunLeases(st *runlease.Store) Option {
	return func(h *Hub) { h.leases = st }
}

// WithPush wires the push notification service at construction time.
func WithPush(p pushService) Option {
	return func(h *Hub) { h.push = p }
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
	return func(h *Hub) { h.mcpConfig = c }
}

// WithKiroCLIPath wires the v3 auth-callback token source over the active
// kiro-cli binary. resolve is the install manager's path resolver (same
// contract as the bridge factory's cliPath: "" while nothing is installed),
// so a version switch reaches the next callback; env is the manager's PATH
// overlay (kiro-cli resolves its kiro-cli-chat sidecar by bare name on
// PATH, so the overlay is required, same as bridge spawns). Unset in tests
// → the auth callback answers with an RPC error instead of vending a token.
func WithKiroCLIPath(resolve func() string, env func() []string) Option {
	return func(h *Hub) { h.kiroToken = kiroauth.NewCLISource(resolve, env) }
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
	return func(h *Hub) {
		h.sessionReaper = r
		h.sessionRefs = refs
	}
}

// New constructs a Hub. Bridges spawn with a fixed kiro-cli acp arg set
// (agent engine + model + effort); tool-call authorization is owned by
// kiro-cli's native Cedar policy on v3, not by CLI trust flags.
//
// ctx is the hub's LIFETIME and is required. The hub has no run method to take
// it — it is handed to server.New as a route provider and the process then
// blocks in ListenAndServe — so the fleet's rule for that case applies: require
// the lifetime at construction rather than default it. There is deliberately no
// nil check and no WithLifetime option: a nil ctx panics in context.WithCancel
// below, at the one construction site, which is the refusal; an option would be
// a default wearing a nicer name, and every default for a lifetime is a
// lifetime nothing can cancel.
//
// New does NOT take ownership of ctx's cancellation. Shutdown cancels the hub's
// own child of it, so the caller may tear the hub down first and end the app's
// lifetime afterwards.
func New(ctx context.Context, workDir string, factory ACPBridgeFactory, chatStore chatRecords, opts ...Option) *Hub {
	sseHub := sse.NewHub(sse.WithReplay(replayBufSize), sse.WithKeepalive(keepaliveInterval))
	lc := &lifecyclePlane{
		workDir: workDir,
		done:    make(chan struct{}),
	}
	lc.shutdownCtx, lc.shutdownCancel = context.WithCancel(ctx)

	h := &Hub{
		lifecycle: lc,
		bridge: &bridgePlane{
			factory:       factory,
			mgr:           newBridgeManager(factory),
			assistantBufs: buffer.NewStore(),
		},
		sse: &ssePlane{
			hub:          sseHub,
			idempotency:  dedup.New(dedup.DefaultTTL, dedup.DefaultMaxEntries, dedup.DefaultMaxResult),
			pendingPerms: newPendingPermsTracker(),
			chatStatus:   newChatStatusCache(),
		},
		perm:         &permPlane{},
		chatStore:    chatStore,
		hookStatus:   newHookStatusCache(kiroSettingsPath()),
		governance:   newGovernanceCache(),
		authLatch:    &authTokenLatch{},
		chatHandlers: make(map[string]chatHandler),
		noopMethods:  make(map[string]struct{}),
	}
	for _, o := range opts {
		o(h)
	}
	h.translator = translate.New(h)
	h.dispatcher = command.New(h)
	h.registerCommandHandlers()
	h.initDispatch()
	h.mcpRegistry = newMCPRegistry(h)
	// A settled session/load replay becomes the chat's transcript. Assigned
	// here (not in the struct literal) because it is a method value on the
	// fully-built Hub; see load_projection.go.
	h.onProjection = h.swapProjectedTranscript
	h.coord = newBridgeCoordinator(h)
	h.shellMgr = NewShellManager(lc.shutdownCtx, workDir)
	h.lines = buffer.NewLineTracker()
	h.agentTerms = newAgentTerminals()
	if lc.configDir != "" {
		h.perm.ignore = ignore.NewMatcher(lc.configDir, workDir)
		// Best-effort: a store that cannot be opened leaves h.secrets nil, and
		// bridges then do NOT declare `_meta.kiro.secretStorage` (see
		// vibekit.StartOpts.SecretStorage), so KAS never asks and MCP OAuth
		// re-registers per spawn as it did before the capability, rather than
		// the hub refusing to construct over a credential cache.
		secrets, err := secretstore.New(lc.configDir)
		if err != nil {
			slog.Error("secretstore: open failed; MCP credentials will not persist", "error", err)
		} else {
			h.secrets = secrets
		}
	}
	go h.cleanIdempotency()
	go h.cullIdleUtilityBridge()
	go h.sweepSessionsLoop()
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
func (h *Hub) UtilityPrompt(ctx context.Context, prompt string, effort vibekit.EffortLevel) (string, error) {
	return h.ensureUtility().agent.UtilityPrompt(ctx, prompt, effort)
}

// MCPRegistry returns the in-memory registry of currently-connected MCP servers
// as a RouteRegistrar — route mounting is the only surface the composition root
// needs from it, and returning the concrete type would leak an unexported name
// from an exported method.
func (h *Hub) MCPRegistry() RouteRegistrar { return h.mcpRegistry }

// MCPSnapshot returns a stable-ordered snapshot of the runtime registry
// so callers outside hub (e.g. the steering generator) can read it
// without taking hub internals as a dependency. Only servers in the
// connected state are included: the steering file presents the list as
// "Connected integrations", and a failed or OAuth-pending server has no
// live tools for the agent to call.
func (h *Hub) MCPSnapshot() []vibekit.MCPSnapshotServer {
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
func (h *Hub) SetMCPOnChange(fn func()) { h.mcpRegistry.SetOnChange(fn) }

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
func (h *Hub) SetPreBridgeSpawn(fn func(context.Context)) { h.preBridgeSpawn = fn }

// RegisterRoutes wires /api/events (SSE), /api/command (POST), and
// /api/shell/ws (WebSocket PTY).
func (h *Hub) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/events", h.handleSSE)
	mux.Handle("/api/command", h.dispatcher)
	mux.HandleFunc("/api/shell/ws", h.handleShellWS)
	mux.HandleFunc("POST /api/shell/restart", h.handleShellRestart)
	mux.HandleFunc("/api/file-changes", h.handleFileChanges)
	h.registerKnowledgeRoutes(mux)
	h.registerHooksRoutes(mux)
	h.registerGovernanceRoutes(mux)
	// Pre-session mode + model catalog (kiro-cli 2.14 _kiro/config/template).
	mux.HandleFunc("GET /api/config-template", h.handleConfigTemplate)
	mux.HandleFunc("GET /api/sessions", h.handleSessionList)
	mux.HandleFunc("GET /api/runs/{id}", h.handleRun)
	mux.HandleFunc("POST /api/runs", h.handleRunLaunch)
	mux.HandleFunc("POST /api/runs/{id}/cancel", h.handleRunCancel)
	mux.HandleFunc("POST /api/runs/{id}/pause", h.handleRunPause)
	mux.HandleFunc("POST /api/runs/{id}/resume", h.handleRunResume)
	mux.HandleFunc("POST /api/runs/{id}/retry", h.handleRunRetry)
	mux.HandleFunc("DELETE /api/runs/{id}", h.handleRunDelete)
	mux.HandleFunc("POST /api/runs/{id}/step", h.handleRunStepStatus)
	mux.HandleFunc("GET /api/recipes", h.handleRecipes)
	h.registerScheduleRoutes(mux)
}

// Shutdown drains in-flight prompts and closes all bridges.
//
// Order matters: we Stop bridges BEFORE waiting on inflight, because
// blocking on a stuck prompt is exactly the reason the server is
// shutting down in the first place. Stop closes the bridge's stdin
// and kills the kiro-cli subprocess, which unblocks any Call waiter
// via the readLoop sentinel — allowing the prompt handler goroutine
// to record its error and decrement inflight normally.
//
// Drain vs abort:
//
//	draining=true is set first so new commands get 503. Existing
//	commands are given a chance to finish cleanly; if they can't
//	(stuck on an unresponsive kiro-cli), the HTTP server's own
//	shutdown context kills everything after its grace period.
func (h *Hub) Shutdown() {
	slog.Info("hub draining")
	h.lifecycle.draining.Store(true)

	// 0. Stop background tickers first so they can't race bridge
	//    teardown with a late cull Stop() or idempotency sweep under
	//    the mutex we're about to acquire.
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

	// 2. Wait for in-flight prompt handlers to clean up.
	h.lifecycle.inflight.Wait()

	// 3. Stop utility bridge.
	h.stopUtilityBridge()

	// 4b. Kill all agent terminal subprocesses.
	h.agentTerms.drainAll()

	// 5. Tear down shell + SSE clients.
	h.shellMgr.kill()
	h.sse.hub.Shutdown()
	slog.Info("hub shutdown complete")
}

// bridgeIdleTimeout bounds how long the tab-less utility session may sit
// idle before it is stopped. It does NOT apply to chat bridges — those are
// owned by their tab (see cullIdleUtilityBridge).
const bridgeIdleTimeout = 30 * time.Minute

// --- Broadcast ---

// Broadcast sends a ServerEvent to every connected SSE client. Used by the
// chat store (chat_created / chat_updated / message_* etc.) and by the hub
// itself for turn_ended / permission_needed / error.
func (h *Hub) Broadcast(_ context.Context, evt vibekit.ServerEvent) {
	h.emit(evt)
}

// CheckDedup returns a cached response for the given request ID.
// Satisfies command.Dependencies.
func (h *Hub) CheckDedup(reqID string) ([]byte, bool) {
	return h.sse.idempotency.Check(reqID)
}

// RecordDedup caches a response for idempotent replay.
// Satisfies command.Dependencies.
func (h *Hub) RecordDedup(reqID string, result []byte) {
	h.sse.idempotency.Record(reqID, result)
}

// Draining reports whether the server is shutting down.
// Satisfies command.Dependencies.
func (h *Hub) Draining() bool {
	return h.lifecycle.draining.Load()
}

// hubContext returns a context derived from the hub's shutdownCtx.
// Cancelled when Shutdown fires. Replaces the per-event goroutine
// pattern (context.WithCancel + go select{<-h.lifecycle.done}) with a zero-
// allocation child context.
func (h *Hub) hubContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(h.lifecycle.shutdownCtx)
}

// parentACPSession returns the ACP session id of the running bridge
// for chatID, or "" when no bridge exists. Translator helpers use this
// to short-circuit notifications whose top-level sessionId belongs to
// a subagent rather than the parent chat.
func (h *Hub) parentACPSession(chatID vibekit.ChatID) string {
	sb := h.bridge.mgr.get(chatID)
	if sb == nil {
		return ""
	}
	return string(sb.bridge.SessionID())
}

// --- Idempotency cache ---

func (h *Hub) recordDedup(reqID string, result []byte) {
	h.sse.idempotency.Record(reqID, result)
}

// Compile-time assertion: Hub satisfies command.Dependencies.
func (h *Hub) cleanIdempotency() {
	h.sse.idempotency.StartCleaner(h.lifecycle.done)
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
func (h *Hub) sweepSessionsLoop() {
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
func (h *Hub) sweepSessionsOnce() {
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
func (h *Hub) liveSessionIDs() []string {
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

// utilityLiveSessionID snapshots the utility runtime pointer under
// lifecycle.mu (same order as the cull path) and asks its session for the
// live ACP session id. "" when the runtime was never created or is stopped.
func (h *Hub) utilityLiveSessionID() string {
	h.lifecycle.mu.Lock()
	u := h.bridge.utility
	h.lifecycle.mu.Unlock()
	if u == nil {
		return ""
	}
	return u.session.liveSessionID()
}

// stopUtilityBridge stops the utility session if it exists.
//
// The h.bridge.utility field read + nil is guarded by h.lifecycle.mu so it
// serialises with cullIdleUtilityBridgeOnce, which reads the same field under
// that lock (snapshot-and-release). Only called from Shutdown, where
// inflight.Wait() has already returned, so no in-flight turn or RPC holds
// a lease on the session being stopped.
func (h *Hub) stopUtilityBridge() {
	h.lifecycle.mu.Lock()
	u := h.bridge.utility
	h.bridge.utility = nil
	h.lifecycle.mu.Unlock()
	if u == nil {
		return
	}
	u.session.Stop()
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
func (h *Hub) cullIdleUtilityBridge() {
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

// cullIdleUtilityBridgeOnce runs one utility-session sweep. stopIfIdle owns
// the victim-capture dance (the session mutex is short-held, so this never
// waits behind an in-flight text turn; acquire bumps lastActiveAt at turn
// start, so a live turn is trivially "recently active"). h.bridge.utility
// itself is read under h.lifecycle.mu (snapshot-and-release) so a concurrent
// stopUtilityBridge clearing the field can't tear the pointer read. Lowercase
// rather than inlined so the sweep is testable without driving a real ticker.
func (h *Hub) cullIdleUtilityBridgeOnce() {
	h.lifecycle.mu.Lock()
	u := h.bridge.utility
	h.lifecycle.mu.Unlock()
	if u != nil && u.session.stopIfIdle(time.Now().Add(-bridgeIdleTimeout)) {
		slog.Info("culled idle utility bridge")
	}
}
