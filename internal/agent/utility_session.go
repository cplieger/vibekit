// Utility session: the shared kiro-cli subprocess + ACP session behind
// vibekit's ambient AI features.
//
// The utility runtime is split across three files by role:
//
//	utility_session.go  the session-holder: subprocess + ACP session
//	                    lifecycle, the forward goroutine, host-request
//	                    answering, and the generation guard. Its mutex
//	                    protects LIFECYCLE only and is never held across
//	                    a bridge Call.
//	utility_rpc.go      stateless KAS RPC wrappers (account usage,
//	                    knowledge, specs, policy, hooks) over the session.
//	                    These are instant reads: they acquire the session,
//	                    Call outside any lock, and never wait behind a
//	                    text-generation turn.
//	utility_agent.go    the cheap text-generation agent (UtilityPrompt):
//	                    per-task effort, recycle policy, and response
//	                    draining, serialized one turn at a time by its
//	                    own turn mutex.
//
// The split exists because the old single-mutex utilityBridge coupled the
// two roles: a specs-board or permissions read queued behind an in-flight
// 60-second generation turn for no reason. Now only text turns serialize
// with text turns.
//
// Concurrency model: callers acquire() a lease {bridge, gen, chunks} and
// use the bridge OUTSIDE the session mutex (Bridge.Call is safe for
// concurrent use; readLoop matches responses by id). On error they call
// resetIf(gen): the generation counter, incremented on every start, makes
// the reset idempotent — a stale failure can't tear down a session that
// was already recycled and restarted by someone else.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/kiroauth"
	"github.com/cplieger/vibekit/internal/secretstore"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// utilityRuntime bundles the two halves of the utility subsystem the runtime
// lazily constructs together: the session-holder and the text-gen agent.
type utilityRuntime struct {
	session *utilitySession
	// textgen is the text-generation agent, named for what it does rather than
	// what it is. It was `agent`, and inside package agent a field by that name
	// invites a local that shadows the package.
	textgen *utilityAgent
}

// newUtilityRuntime wires a session and its agent.
func newUtilityRuntime(shutdownCtx context.Context, factory ACPBridgeFactory, models func() []vibekit.SessionModel, hooks utilitySessionHooks, secrets *secretstore.Store, enableHooks bool) *utilityRuntime {
	session := newUtilitySession(shutdownCtx, factory, models, hooks, secrets, enableHooks)
	return &utilityRuntime{session: session, textgen: newUtilityAgent(session)}
}

// utilitySessionHooks are the runtime callbacks the session's forward goroutine
// invokes. Constructor-injected and immutable after construction, so the
// forward goroutine reads them without locks.
type utilitySessionHooks struct {
	// onHooksChanged broadcasts an hooks_changed SSE on _kiro/hooks/didChange.
	onHooksChanged func()
	// onGovernanceState captures the _kiro/governance/state notification the
	// session receives on session/new (its notifications bypass the main
	// dispatcher), so GET /api/governance is warm with no chat open.
	onGovernanceState func(json.RawMessage)
	// tokenSource answers the _kiro/auth/getAccessToken callback (the runtime's
	// kiroAccessTokenResult). nil = not wired (older tests) → RPC error.
	tokenSource func(context.Context) (map[string]any, error)
}

// utilitySession owns the dedicated kiro-cli subprocess + ACP session.
// It has its own session so ambient work never pollutes any chat's
// context. Lazily started on first acquire, culled after 30 minutes of
// inactivity (same as chat bridges).
type utilitySession struct {
	// shutdownCtx is the RUNTIME's lifetime, required by the constructor and never a
	// request context. It is a lifetime handle rather than a stashed caller
	// context, and it has no run method to take it: the session is started
	// lazily from acquire, whose ctx belongs to whichever request happened to
	// arrive first. The two are deliberately not merged — startLocked's ctx picks
	// the model and dies with the request, while the subprocess this field bounds
	// must outlive it.
	shutdownCtx   context.Context
	bridgeFactory ACPBridgeFactory
	models        func() []vibekit.SessionModel
	// secrets is the runtime's credential store, shared not copied, so a
	// registration obtained on any bridge is visible from every other one.
	// Nil when the runtime has no configDir; see bridge_v3_secret.go.
	secrets *secretstore.Store
	hooks   utilitySessionHooks

	// lastActiveAt, bridge, gen, started, responseCh, forwardDone are the
	// lifecycle fields guarded by mu. mu is held only for short
	// bookkeeping sections — NEVER across a bridge Call — so lifecycle
	// operations can't be starved by a slow turn.
	lastActiveAt time.Time
	bridge       utilityBridge
	responseCh   chan utilityChunkPayload
	forwardDone  chan struct{}
	gen          uint64
	mu           sync.Mutex

	// enableHooks opts this session into KAS's v2 hook engine (StartOpts.
	// EnableHooks → _meta.kiro.hooks); always true in production so the
	// hooks list/setEnabled RPCs are available.
	enableHooks bool
	started     bool
}

// newUtilitySession constructs a stopped session with the initialization
// invariants explicit: started=false, gen=0, lastActiveAt=zero.
//
// shutdownCtx is required, positionally: it is the session's lifetime, and every
// default for a lifetime is a lifetime nothing can cancel.
func newUtilitySession(shutdownCtx context.Context, factory ACPBridgeFactory, models func() []vibekit.SessionModel, hooks utilitySessionHooks, secrets *secretstore.Store, enableHooks bool) *utilitySession {
	return &utilitySession{
		shutdownCtx:   shutdownCtx,
		bridgeFactory: factory,
		models:        models,
		secrets:       secrets,
		hooks:         hooks,
		enableHooks:   enableHooks,
	}
}

// sessionLease is a caller's snapshot of the live session: the bridge to
// Call on, the generation to hand back to resetIf on failure, and the
// chunk channel the forward goroutine feeds (nil until started via the
// normal path; a closed channel just ends a drain early).
type sessionLease struct {
	bridge acpSessionCaller
	chunks <-chan utilityChunkPayload
	gen    uint64
}

// acquire ensures the session is started and returns a lease on it,
// bumping the idle clock. The caller uses the lease's bridge outside the
// session mutex and reports failures via resetIf(lease.gen).
func (us *utilitySession) acquire(ctx context.Context) (sessionLease, error) {
	us.mu.Lock()
	defer us.mu.Unlock()
	if !us.started {
		if err := us.startLocked(ctx); err != nil {
			return sessionLease{}, fmt.Errorf("utility bridge start: %w", err)
		}
	}
	us.lastActiveAt = time.Now()
	return sessionLease{bridge: us.bridge, gen: us.gen, chunks: us.responseCh}, nil
}

// ensureStarted lazily starts the session without issuing any request, so
// a caller that only needs a live acp session (e.g. warming
// GET /api/governance, whose state arrives unsolicited on session/new)
// can trigger it without spending a turn.
func (us *utilitySession) ensureStarted(ctx context.Context) error {
	_, err := us.acquire(ctx)
	return err
}

// startLocked spawns a fresh subprocess + session. Caller holds us.mu.
func (us *utilitySession) startLocked(ctx context.Context) error {
	bridge := us.bridgeFactory()
	model := cheapestModel(ctx, us.models())

	// The forward goroutine must be draining NotifCh BEFORE Start: on v3
	// (KAS) session/new blocks until the host answers the
	// _kiro/auth/getAccessToken and _kiro/terminal/shell_type requests,
	// which arrive on NotifCh and are answered by forward's
	// answerHostRequest. Channels are locals so a failed Start (bridge
	// stopped, NotifCh closed, goroutine exits) leaves no session state
	// behind.
	responseCh := make(chan utilityChunkPayload, 64)
	forwardDone := make(chan struct{})
	go us.forward(bridge, bridge.NotifCh(), responseCh, forwardDone)

	// Start with the runtime's shutdown context as the subprocess lifecycle
	// context: the subprocess must outlive individual requests (the
	// per-request ctx is only used for the model pick above). Runs v3
	// (KAS) like every chat bridge — without the engine it would default
	// to v2, which vibekit can no longer talk to.
	//
	// Passing a process-lifetime context as the HANDSHAKE ctx is safe because
	// Start bounds the handshake itself (bridge.handshakeBudget). It was not
	// always: this call runs under us.mu, so before that budget existed a
	// session/new KAS never answered held the utility mutex for the life of the
	// process, and every utility-backed endpoint — the config template,
	// governance, knowledge, hooks — hung behind it with no error and no log
	// line. Do not "tidy" this to a per-request ctx: the subprocess would then
	// die with the request that happened to start it.
	if err := bridge.Start(us.shutdownCtx, &vibekit.StartOpts{Lifetime: us.shutdownCtx, Model: model, AgentEngine: resolveAgentEngine(), EnableHooks: us.enableHooks, SecretStorage: us.secrets != nil}); err != nil {
		return err
	}
	us.bridge = bridge
	us.started = true
	// A new generation per start: resetIf calls carrying a stale gen
	// (from a lease on the previous subprocess) become no-ops, and the
	// agent's per-session counters resync on the mismatch.
	us.gen++
	us.lastActiveAt = time.Now()
	us.responseCh = responseCh
	us.forwardDone = forwardDone

	// The session id is logged because this bridge's session is the one that
	// appears in `session/list` owned by no chat, so a row nobody can explain is
	// diagnosed by grepping for it. See toResumable.
	slog.Info("utility bridge started", "model", model, "session_id", string(bridge.SessionID()))
	return nil
}

// stopLocked stops the subprocess and waits for the forward goroutine to
// exit (it returns when NotifCh closes after Stop), so a recycle leaks no
// goroutine. Safe against a wedged forward: forwardChunk's send is
// non-blocking, so a full responseCh can't park forward and stall the
// <-forwardDone. Caller holds us.mu.
func (us *utilitySession) stopLocked() {
	us.bridge.Stop()
	if us.forwardDone != nil {
		<-us.forwardDone
	}
	us.started = false
	us.responseCh = nil
	us.forwardDone = nil
}

// resetIf stops the session ONLY when the caller's generation is still
// the live one. A lease-holder that hit an error calls this; if the
// session was already recycled/restarted since that lease was taken, the
// reset is a stale complaint about a dead subprocess and is dropped.
func (us *utilitySession) resetIf(gen uint64) {
	us.mu.Lock()
	defer us.mu.Unlock()
	if !us.started || us.gen != gen {
		return
	}
	us.stopLocked()
}

// Stop stops the session if it is running. Thread-safe. Called from the runtime
// Shutdown (after inflight.Wait(), so no lease-holder is mid-Call).
func (us *utilitySession) Stop() {
	us.mu.Lock()
	defer us.mu.Unlock()
	if !us.started {
		return
	}
	us.stopLocked()
	slog.Info("utility bridge stopped")
}

// stopIfIdle stops the session when it has been inactive since before
// cutoff, reporting whether it did. The victim is stopped asynchronously
// and forward's exit is not awaited (the cull must never block on a slow
// teardown); the old forward exits on its own once the victim's NotifCh
// closes, and any lease-held chunk channel just closes.
func (us *utilitySession) stopIfIdle(cutoff time.Time) bool {
	us.mu.Lock()
	shouldStop := us.started && !us.lastActiveAt.IsZero() && us.lastActiveAt.Before(cutoff)
	var victim acpStopper
	if shouldStop {
		// Capture the victim INSIDE the lock: startLocked reassigns
		// us.bridge under us.mu, so reading it after the unlock could
		// stop a freshly-restarted bridge.
		victim = us.bridge
		us.started = false
		us.responseCh = nil
		us.forwardDone = nil
	}
	us.mu.Unlock()
	if shouldStop {
		go victim.Stop()
	}
	return shouldStop
}

// liveID returns the ACP session id of the running session, or ""
// when stopped. Used by the runtime's orphan-session sweep to exempt the live
// utility session's on-disk KAS state: it is referenced by no chat, so
// without this the hourly sweep could delete the session dir out from
// under the live subprocess once it idles past the reaper's age guard.
func (us *utilitySession) liveID() string {
	us.mu.Lock()
	defer us.mu.Unlock()
	if !us.started {
		return ""
	}
	return string(us.bridge.SessionID())
}

// shuttingDown reports whether the runtime is shutting down (the session's
// lifecycle context is cancelled). The agent's drain uses it to skip a
// redundant reset that would race Stop during graceful shutdown.
func (us *utilitySession) shuttingDown() bool {
	return us.shutdownCtx.Err() != nil
}

// utilitySessionParams builds an ACP parameter map with the bridge's
// session ID injected. Mirrors command.SessionParams, and like it takes the
// 1-method session reader rather than a whole bridge — the two are separate
// declarations because the utility session's bridge does not carry the prompt
// slot command.Bridge requires.
func utilitySessionParams(bridge acpSession, extra map[string]any) map[string]any {
	m := map[string]any{vibekit.KeySessionID: bridge.SessionID()}
	maps.Copy(m, extra)
	return m
}

// utilityUpdateBase extracts the sessionUpdate kind discriminator.
// Local to the utility runtime to avoid coupling to translate's wire types.
type utilityUpdateBase struct {
	Kind vibekit.ACPUpdateKind `json:"sessionUpdate"`
}

// utilityChunkPayload is the minimal shape the utility runtime needs
// from an agent_message_chunk notification: just the text content.
type utilityChunkPayload struct {
	Content struct {
		Text string `json:"text"`
	} `json:"content"`
}

// forward is a dedicated goroutine that continuously drains the bridge's
// NotifCh, forwarding agent_chunk text to responseCh. Peer requests
// (msg.ID != nil) are answered via answerHostRequest — the utility session
// runs v3 (KAS) like every chat bridge, so it must vend the host-mediated
// auth token + shell type or session/new stalls. `executeHook` is NOT among
// them (see answerHostRequest). A _kiro/hooks/didChange notification
// fans out an hooks_changed SSE. All other notifications (usage stats, stale
// chunks) are discarded. This prevents NotifCh from filling up between calls,
// which would block readLoop and deadlock all pending Call waiters.
//
// bridge is passed explicitly (not read from us.bridge) so a recycle that
// reassigns us.bridge can't make this goroutine answer on the wrong pipe.
// forward takes NO locks: the hooks callbacks are immutable and the chunk
// send is non-blocking, so a held session mutex can never deadlock it.
func (us *utilitySession) forward(bridge acpResponder, notifCh <-chan *vibekit.RPCResponse, responseCh chan<- utilityChunkPayload, done chan<- struct{}) {
	defer close(done)
	defer close(responseCh)
	for msg := range notifCh {
		switch {
		case msg.ID != nil:
			us.answerHostRequest(bridge, msg)
		case msg.Method == methodKiroHooksDidChange:
			if us.hooks.onHooksChanged != nil {
				us.hooks.onHooksChanged()
			}
		case msg.Method == methodV3Governance:
			if us.hooks.onGovernanceState != nil {
				us.hooks.onGovernanceState(msg.Params)
			}
		default:
			forwardChunk(msg, responseCh)
		}
	}
}

// forwardChunk forwards an agent_message_chunk's text to responseCh, ignoring
// every other notification. Split out of forward to keep it under the
// cognitive-complexity gate.
func forwardChunk(msg *vibekit.RPCResponse, responseCh chan<- utilityChunkPayload) {
	if msg.Method != vibekit.MethodSessionUpdate || msg.Params == nil {
		return
	}
	var base utilityUpdateBase
	if json.Unmarshal(msg.Params, &base) != nil || base.Kind != vibekit.ACPUpdateAgentChunk {
		return
	}
	var chunk utilityChunkPayload
	if json.Unmarshal(msg.Params, &chunk) == nil {
		// Non-blocking send: if responseCh (buffer 64) is full — a wedged
		// or already-drained turn whose leftover chunks nobody reads — drop
		// the chunk instead of blocking here forever. A blocked forward
		// never loops back to observe notifCh closing, so stopLocked's
		// <-forwardDone (taken under us.mu) would never return and the
		// whole utility subsystem would deadlock. Post-turn leftover
		// chunks are noise; dropping them is correct.
		select {
		case responseCh <- chunk:
		default:
		}
	}
}

// answerHostRequest answers the v3 (KAS) host-mediated requests the utility
// session receives. getAccessToken + shell_type are on the session-creation
// critical path (session/new stalls without them).
//
// `_kiro/hooks/executeHook` is NOT answered any more and must not be re-added:
// answering it is what made vibekit run a shell command a hook file specifies,
// and its only caller (Run-now) is deleted. It now falls to the default branch's
// -32601, which is the honest reply — vibekit does not offer this capability. KAS
// runs runCommand hooks internally on autofire, so nothing regresses.
//
// Tool-use requests are actively refused: the utility session is
// text-generation only (the agent's system prompt says so), but the model
// is not mechanically prevented from trying a tool. The initialize
// handshake declares fs + terminal capabilities (shared bridge code), so a
// stray tool call arrives here as an A→C request — and an UNANSWERED
// request wedges the turn until the agent drain's 60s ceiling fires and
// resets the whole session. Denying permissions and erroring fs/terminal
// requests turns that wedge into an immediate, model-visible tool failure.
func (us *utilitySession) answerHostRequest(bridge acpResponder, msg *vibekit.RPCResponse) {
	ctx := context.Background()
	switch {
	case msg.Method == methodKiroGetAccessToken:
		if us.hooks.tokenSource == nil {
			_ = bridge.Respond(ctx, *msg.ID, nil, kiroauth.ErrNoSource)
			return
		}
		result, err := us.hooks.tokenSource(ctx)
		if err != nil {
			slog.Error("utility bridge v3 auth: token unavailable", "error", err)
			_ = bridge.Respond(ctx, *msg.ID, nil, err)
			return
		}
		_ = bridge.Respond(ctx, *msg.ID, result, nil)
	case msg.Method == methodKiroShellType:
		_ = bridge.Respond(ctx, *msg.ID, kiroShellTypeResult(), nil)
	case msg.Method == methodKiroSecretGet:
		// The utility session starts with NO mcpServers, so it should never
		// connect an MCP server and never reach this. It is answered anyway
		// because the secretStorage capability is declared by the SHARED
		// bridge initialize: were KAS to ask, the default branch's refusal
		// would be a store/delete rethrow rather than a clean miss. The
		// declaration is conditional on us.secrets being non-nil, so a runtime with
		// no store never offers it here either.
		_ = bridge.Respond(ctx, *msg.ID, secretGetResult(us.secrets, msg.Params), nil)
	case msg.Method == methodKiroSecretStore:
		result, err := secretStoreResult(ctx, us.secrets, msg.Params)
		_ = bridge.Respond(ctx, *msg.ID, result, err)
	case msg.Method == methodKiroSecretDelete:
		result, err := secretDeleteResult(ctx, us.secrets, msg.Params)
		_ = bridge.Respond(ctx, *msg.ID, result, err)
	case msg.Method == vibekit.MethodRequestPermission:
		// Deny: cancelled outcome, the ACP shape for "the user said no".
		slog.Warn("utility bridge: denying tool permission request (text-only session)")
		_ = bridge.Respond(ctx, *msg.ID, vibekit.PermissionOutcomeCancelled(), nil)
	case msg.Method == vibekit.MethodFSRead || msg.Method == vibekit.MethodFSWrite ||
		strings.HasPrefix(msg.Method, methodTermPrefix):
		slog.Warn("utility bridge: refusing tool request (text-only session)", "method", msg.Method)
		// -32601 rather than a bare error, for the reason the sibling refusals
		// carry it: this is a capability vibekit deliberately does not offer on
		// this session, not a fault it hit while trying.
		_ = bridge.Respond(ctx, *msg.ID, nil, &vibekit.RPCError{
			Code:    vibekit.RPCCodeMethodNotFound,
			Message: "utility session is text-generation only; tools are unavailable",
		})
	default:
		// Unknown peer request: answer with an error rather than leaving
		// the request pending (an unanswered request can wedge the turn).
		slog.Warn("utility bridge: unexpected peer request, refusing", "method", msg.Method, "id", *msg.ID)
		// -32601, for the same reason the chat dispatcher uses it: this is a
		// deliberate refusal, not an internal fault, and -32603 would make the
		// log blame the wrong side.
		_ = bridge.Respond(ctx, *msg.ID, nil, &vibekit.RPCError{
			Code:    vibekit.RPCCodeMethodNotFound,
			Message: "unsupported on the utility session: " + msg.Method,
		})
	}
}
