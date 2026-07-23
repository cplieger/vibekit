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

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/checkpoint"
	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/dedup"
	"github.com/cplieger/vibekit/internal/ignore"
	"github.com/cplieger/vibekit/internal/kirosession"
	"github.com/cplieger/vibekit/internal/pending"
	"github.com/cplieger/vibekit/internal/translate"
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
	factory       api.ACPBridgeFactory
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
	// turnMirror replicates each chat's in-flight turn from the emit
	// stream for the connect-time turn_state replay (see turn_mirror.go).
	turnMirror *turnMirror
}

// permPlane groups Hub fields related to permissions and supervision.
type permPlane struct {
	pending    *pending.Store
	supervised *supervisedState
	ignore     *ignore.Matcher
}

// Hub is the central coordinator.
type Hub struct {
	lifecycle    *lifecyclePlane
	bridge       *bridgePlane
	sse          *ssePlane
	perm         *permPlane
	bufLifecycle *buffer.Lifecycle
	coord        *BridgeCoordinator

	push               api.PushService
	chatStore          api.ChatStore
	mcpConfig          api.MCPConfig
	mcpRegistry        *mcpRegistry
	shellMgr           *ShellManager
	preBridgeSpawn     func(context.Context) // optional; fired before each new bridge starts
	chatHandlers       map[string]chatHandler
	sessUpdateHandlers map[api.ACPUpdateKind]sessionUpdateHandler
	noopMethods        map[string]struct{}
	dispatcher         *command.Dispatcher
	translator         *translate.Translator
	checkpoints        api.CheckpointService
	sessionReaper      *kirosession.Reaper
	sessionRefs        func(context.Context) map[string]struct{}
	lines              *buffer.LineTracker
	agentTerms         *agentTerminals
	hookStatus         *hookStatusCache
	governance         *governanceCache

	// Code-intelligence activation inputs + in-flight guard (code_intel.go).
	ciGate func() bool
	ciPath string
	ciBusy atomic.Bool
}

// Option configures optional Hub parameters.
type Option func(*Hub)

// WithConfigDir sets the configuration directory for permissions,
// checkpoints, and ignore rules.
func WithConfigDir(dir string) Option {
	return func(h *Hub) { h.lifecycle.configDir = dir }
}

// WithPush wires the push notification service at construction time.
func WithPush(p api.PushService) Option {
	return func(h *Hub) { h.push = p }
}

// WithMCPConfig wires the MCP configuration store. The hub reads
// ACPServers() on every bridge spawn. Accepts a lazy thunk to resolve
// circular dependencies (mcpStore→hub broadcast).
func WithMCPConfig(c api.MCPConfig) Option {
	return func(h *Hub) { h.mcpConfig = c }
}

// WithSessionReaper wires the KAS session reaper and the referenced-session
// thunk. The reaper removes on-disk kiro-cli/KAS session state: promptly on
// chat delete (via cleanupChatState) and via a periodic orphan sweep that
// spares any session id refs reports as still referenced by a live or
// archived chat. Unset in tests → session reaping is a no-op.
func WithSessionReaper(r *kirosession.Reaper, refs func(context.Context) map[string]struct{}) Option {
	return func(h *Hub) {
		h.sessionReaper = r
		h.sessionRefs = refs
	}
}

// New constructs a Hub. Bridges spawn with a fixed kiro-cli acp arg set
// (agent engine + model + effort); tool-call authorization is owned by
// kiro-cli's native Cedar policy on v3, not by CLI trust flags.
func New(workDir string, factory api.ACPBridgeFactory, chatStore api.ChatStore, opts ...Option) *Hub {
	sseHub := sse.NewHub(sse.WithReplay(replayBufSize), sse.WithKeepalive(keepaliveInterval))
	lc := &lifecyclePlane{
		workDir: workDir,
		done:    make(chan struct{}),
	}
	lc.shutdownCtx, lc.shutdownCancel = context.WithCancel(context.Background())

	h := &Hub{
		lifecycle: lc,
		bridge: &bridgePlane{
			factory:       factory,
			mgr:           newBridgeManager(factory, &lc.inflight),
			assistantBufs: buffer.NewStore(),
		},
		sse: &ssePlane{
			hub:          sseHub,
			idempotency:  dedup.New(dedup.DefaultTTL, dedup.DefaultMaxEntries, dedup.DefaultMaxResult),
			pendingPerms: newPendingPermsTracker(),
			turnMirror:   newTurnMirror(),
		},
		perm: &permPlane{
			pending: pending.New(),
		},
		chatStore:    chatStore,
		hookStatus:   newHookStatusCache(kiroSettingsPath()),
		governance:   newGovernanceCache(),
		chatHandlers: make(map[string]chatHandler),
		noopMethods:  make(map[string]struct{}),
	}
	h.perm.supervised = newSupervisedState(h.Broadcast)
	for _, o := range opts {
		o(h)
	}
	h.translator = translate.New(h)
	h.dispatcher = command.New(h, command.WithPrompter(h))
	h.registerCommandHandlers()
	h.initDispatch()
	h.mcpRegistry = newMCPRegistry(h)
	h.coord = newBridgeCoordinator(h)
	h.shellMgr = NewShellManager(lc.shutdownCtx, workDir)
	h.lines = buffer.NewLineTracker()
	h.agentTerms = newAgentTerminals()
	h.bufLifecycle = &buffer.Lifecycle{
		ConfigDir: lc.configDir,
		Store:     h.bridge.assistantBufs,
	}
	if lc.configDir != "" {
		h.perm.ignore = ignore.NewMatcher(lc.configDir, workDir)
		h.checkpoints = checkpoint.NewStore(lc.configDir, workDir, func(chatID string, p *checkpoint.ConflictPayload) {
			h.broadcastConflict(api.ChatID(chatID), p)
		})
	}
	go h.cleanIdempotency()
	go h.cullIdleBridges()
	go h.sweepSessionsLoop()
	return h
}

// UtilityPrompt delegates to the utility text-gen agent, satisfying
// api.UtilityPrompter. The runtime is lazily constructed on first call.
func (h *Hub) UtilityPrompt(ctx context.Context, prompt string, effort api.EffortLevel) (string, error) {
	return h.ensureUtility().agent.UtilityPrompt(ctx, prompt, effort)
}

// CleanupCheckpoints removes a chat's checkpoint event log (the
// content-addressed blob store is GC'd separately — there is no git
// repository involved; see internal/checkpoint/events.go "Why JSONL
// instead of git"). Safe to call even if the checkpoint store is nil
// (no configDir).
func (h *Hub) CleanupCheckpoints(ctx context.Context, chatID api.ChatID) {
	if h.checkpoints != nil {
		h.checkpoints.Cleanup(ctx, chatID)
	}
}

// OnChatArchiving is the pre-archive hook wired to chat.WithPreArchive.
// It runs the SAME in-memory teardown a delete performs — flush the
// in-flight turn (CloseBridge), kill agent terminals, clear pending perms
// + supervised trust, close+remove the .partial — EXCEPT it does not remove
// the chat file (Archive moves it) and does not reap checkpoints (archive
// is reversible; checkpoints are reaped only at purge / hard delete).
//
// It MUST run before the chat file is moved to the archive dir so that:
//   - a live bridge can't outlive its chat record (invariant #3),
//   - archiving mid-turn can't strand the in-flight turn (the moved file +
//     tombstone would make Store.Mutate refuse the persist), and
//   - no orphan .partial survives for RecoverPartials to resurrect as a
//     ghost active chat after a restart.
func (h *Hub) OnChatArchiving(chatID api.ChatID) {
	ctx, cancel := h.hubContext()
	defer cancel()
	h.cleanupChatState(ctx, chatID, false)
}

// OnChatArchived is the post-archive callback wired to chat.WithOnArchive.
// The in-memory teardown (including the line tracker) already ran in
// OnChatArchiving before the file moved; checkpoints are deliberately NOT
// reaped here (archive is reversible — a restored chat keeps its
// file-restore/undo history; only purge / hard delete reap them). All that
// remains is kicking off the async summary under the hub's inflight
// WaitGroup so Shutdown can drain it. Skipped entirely when the hub is
// already draining: no point spawning new work that's about to race teardown.
func (h *Hub) OnChatArchived(chatID api.ChatID) {
	if h.lifecycle.draining.Load() {
		return
	}
	// Derive a fresh done-aware context for the summary goroutine
	// (cancelled by h.lifecycle.done).
	sumCtx, sumCancel := h.hubContext()
	h.lifecycle.inflight.Go(func() {
		defer sumCancel()
		h.summarizeOnArchive(sumCtx, chatID)
	})
}

// CheckpointOldestTag returns the earliest available checkpoint tag
// for chatID, or "" when none exist. The chat store reads this when
// building ChatHeader so the client can decide which turns still have
// working Restore buttons.
func (h *Hub) CheckpointOldestTag(ctx context.Context, chatID api.ChatID) string {
	if h.checkpoints == nil {
		return ""
	}
	return string(h.checkpoints.OldestTag(ctx, chatID))
}

// StartCheckpointBackgroundTasks kicks off the blob GC ticker and
// runs the initial sweep. Called from main.go after Hub construction
// so tests can opt out.
func (h *Hub) StartCheckpointBackgroundTasks() {
	if h.checkpoints == nil {
		return
	}
	h.checkpoints.StartBackgroundTasks(h.lifecycle.shutdownCtx)
}

// StopCheckpointBackgroundTasks halts the blob GC goroutine. Called
// from Hub.Shutdown.
func (h *Hub) StopCheckpointBackgroundTasks() {
	if h.checkpoints == nil {
		return
	}
	h.checkpoints.Stop()
}

// MCPConfig returns the MCP configuration store.
func (h *Hub) MCPConfig() api.MCPConfig {
	return h.mcpConfig
}

// MCPRegistry returns the in-memory registry of currently-connected MCP
// servers as an api.RouteHandler (the only surface main.go needs — the
// registry registers its own HTTP routes). Exposing the concrete type
// would leak an unexported name from an exported method.
func (h *Hub) MCPRegistry() api.RouteHandler { return h.mcpRegistry }

// MCPSnapshot returns a stable-ordered snapshot of the runtime registry
// so callers outside hub (e.g. the steering generator) can read it
// without taking hub internals as a dependency. Only servers in the
// connected state are included: the steering file presents the list as
// "Connected integrations", and a failed or OAuth-pending server has no
// live tools for the agent to call.
func (h *Hub) MCPSnapshot() []api.MCPSnapshotServer {
	snap := h.mcpRegistry.Snapshot()
	out := make([]api.MCPSnapshotServer, 0, len(snap))
	for i := range snap {
		if snap[i].State != mcpStateConnected {
			continue
		}
		out = append(out, api.MCPSnapshotServer{Name: snap[i].Name})
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
	mux.HandleFunc("/api/file-changes", h.handleFileChanges)
	mux.HandleFunc("/api/pending-changes/", h.handlePendingChange)
	h.registerCheckpointRoutes(mux)
	h.registerKnowledgeRoutes(mux)
	h.registerSpecRoutes(mux)
	h.registerHooksRoutes(mux)
	h.registerGovernanceRoutes(mux)
	// Pre-session mode + model catalog (kiro-cli 2.14 _kiro/config/template).
	mux.HandleFunc("GET /api/config-template", h.handleConfigTemplate)
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

	// 1b. Reject every outstanding Supervised-mode pending op. Without
	// this, an fs handler parked on <-wait in stageFSWrite would block
	// forever: bridge.Stop has already broken kiro-cli's ability to
	// send session/cancel, and the user can't click "Reject" on a
	// shutting-down server. inflight.Wait below would either time out
	// at the HTTP server level or leak goroutines until process exit.
	// Flushing here unblocks every handler with accepted=false; each
	// then returns "change rejected by user" to a kiro-cli that's
	// already dead, which is harmless.
	for _, id := range h.listChatIDsWithPending() {
		h.flushPendingForChat(h.lifecycle.shutdownCtx, id, api.ClearReasonShutdown)
	}

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

	// 4. Stop checkpoint background tasks (blob GC ticker).
	h.StopCheckpointBackgroundTasks()

	// 4b. Kill all agent terminal subprocesses.
	h.agentTerms.drainAll()

	// 5. Tear down shell + SSE clients.
	h.shellMgr.kill()
	h.sse.hub.Shutdown()
	slog.Info("hub shutdown complete")
}

const bridgeIdleTimeout = 30 * time.Minute

// --- Broadcast ---

// Broadcast sends a ServerEvent to every connected SSE client. Used by the
// chat store (chat_created / chat_updated / message_* etc.) and by the hub
// itself for turn_ended / permission_needed / error.
func (h *Hub) Broadcast(_ context.Context, evt api.ServerEvent) {
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

// broadcastConflict fans a cross-chat drift event out to SSE
// subscribers. Wired into checkpoint.NewStore; every call is the
// result of a Snapshot detecting drift, so we just wrap the
// payload into a ServerEvent. Since api.ConflictDetectedPayload is
// a type alias for checkpoint.ConflictPayload, no field copy needed.
func (h *Hub) broadcastConflict(chatID api.ChatID, p *checkpoint.ConflictPayload) {
	h.Broadcast(h.lifecycle.shutdownCtx, api.NewEvent(api.EventConflictDetected, chatID, *p))
}

// parentACPSession returns the ACP session id of the running bridge
// for chatID, or "" when no bridge exists. Translator helpers use this
// to short-circuit notifications whose top-level sessionId belongs to
// a subagent rather than the parent chat.
func (h *Hub) parentACPSession(chatID api.ChatID) string {
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

// sweepSessionsOnce runs one orphan-session sweep against a fresh referenced
// set (every active + archived chat's acp_session_id), plus the live utility
// bridge's session — it is referenced by no chat, and without the exemption
// the sweep would delete its on-disk KAS state from under the live
// subprocess once it idles past the reaper's age guard (10 min guard vs the
// 30 min utility cull leaves a window every hourly sweep can land in).
func (h *Hub) sweepSessionsOnce() {
	ctx, cancel := h.hubContext()
	defer cancel()
	refs := h.sessionRefs(ctx)
	if id := h.utilityLiveSessionID(); id != "" {
		if refs == nil {
			refs = map[string]struct{}{}
		}
		refs[id] = struct{}{}
	}
	h.sessionReaper.Sweep(refs)
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
// serialises with cullIdleBridgesOnce, which reads the same field under
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

// cullIdleBridges stops bridges that have been idle for longer than
// bridgeIdleTimeout. Runs every 60 seconds. The next prompt on a
// culled chat will create a fresh bridge via getOrCreateBridge (which
// already handles session/load for existing ACP sessions). Exits on
// Shutdown via h.lifecycle.done so a late tick can't race bridge teardown.
func (h *Hub) cullIdleBridges() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-h.lifecycle.done:
			return
		case <-ticker.C:
			h.cullIdleBridgesOnce()
		}
	}
}

// cullIdleBridgesOnce runs one sweep of the cull logic: chat bridges
// first (delete-and-Stop in one pass), then the utility bridge under
// its own mutex. Exported via lowercase to keep the hot path testable
// without driving a real ticker.
func (h *Hub) cullIdleBridgesOnce() {
	count := h.bridge.mgr.count()
	timeout := bridgeIdleTimeout
	if count > 5 {
		adaptive := max(bridgeIdleTimeout/time.Duration(count), 5*time.Minute)
		timeout = adaptive
	}
	toClose := h.bridge.mgr.selectIdle(timeout)
	for _, c := range h.bridge.mgr.closeAndStop(toClose) {
		slog.Info("culled idle bridge", "chat_id", c.chatID,
			"idle_since", c.sb.lastActiveAt.UTC().Format(time.RFC3339))
	}
	// Utility session: stopIfIdle owns the victim-capture dance (the
	// session mutex is short-held, so this never waits behind an
	// in-flight text turn; acquire bumps lastActiveAt at turn start, so a
	// live turn is trivially "recently active"). h.bridge.utility itself
	// is read under h.lifecycle.mu (snapshot-and-release) so a concurrent
	// stopUtilityBridge clearing the field can't tear the pointer read.
	h.lifecycle.mu.Lock()
	u := h.bridge.utility
	h.lifecycle.mu.Unlock()
	if u != nil && u.session.stopIfIdle(time.Now().Add(-bridgeIdleTimeout)) {
		slog.Info("culled idle utility bridge")
	}
}
