// Utility session: the shared kiro-cli subprocess + ACP session behind vibekit's
// ambient AI features. Split by role — this file holds the session (lifecycle,
// forward goroutine, host-request answering), utility_rpc.go the stateless RPC
// wrappers, utility_agent.go the text-generation agent.
//
// Concurrency: acquire() a lease and use its bridge OUTSIDE the session mutex;
// resetIf(gen) is idempotent, so a stale failure cannot tear down a recycled one.

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
	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// utilityRuntime bundles the two halves of the utility subsystem the runtime
// lazily constructs together: the session-holder and the text-gen agent.
type utilityRuntime struct {
	session *utilitySession
	// textgen is the text-generation agent. Named for what it does: inside package
	// agent, a field called `agent` invites a local that shadows the package.
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
	// onGovernanceState captures the _kiro/governance/state notification this session
	// receives on session/new, so GET /api/governance is warm with no chat open.
	onGovernanceState func(json.RawMessage)
	// onPolicyNotification routes _kiro/policy/{changed,error} into the translator the
	// chat dispatch uses: this session's own PolicySession watches the file and its
	// notifications bypass that dispatcher, so a write with no chat open is lost.
	onPolicyNotification func(*vibekit.RPCResponse)
	// tokenSource answers the _kiro/auth/getAccessToken callback (the runtime's
	// kiroAccessTokenResult). nil = not wired (older tests) → RPC error.
	tokenSource func(context.Context) (map[string]any, error)
	// onForeignUpdate offers a `session/update` frame belonging to ANOTHER session to
	// whoever is reading it, reporting whether it was consumed: a workflow step's
	// transcript is read here, and `session/load` replays it carrying that session's
	// id, which forwardChunk otherwise drops as foreign. Consulted BEFORE the Warn.
	onForeignUpdate func(sessionID string, kind vibekit.ACPUpdateKind, update json.RawMessage) bool
	// onFrameDrained reports the read-loop position this session has FOLDED, plus the
	// attachment it belongs to; `force` marks the call after the drain loop ended,
	// where no frame can advance it again. It is what closes a step replay: see
	// stepReplays.settleConsumed.
	onFrameDrained func(at drainPoint, force bool)
	// presets resolves the active security profile into KAS policy preset ids for this
	// session's StartOpts. It matters here rather than only on chat bridges because
	// this is the session answering GET /api/permissions, so a profile absent from it
	// leaves the policy view unable to report what that profile grants. nil sends none.
	presets func(context.Context) []string
}

// utilitySession owns the dedicated kiro-cli subprocess + ACP session.
// It has its own session so ambient work never pollutes any chat's
// context. Lazily started on first acquire, culled after 30 minutes of
// inactivity (same as chat bridges).
type utilitySession struct {
	// shutdownCtx is the RUNTIME's lifetime, never a request context: the session is
	// started lazily from acquire, whose ctx picks the model and dies with the
	// request, while the subprocess this field bounds must outlive it.
	shutdownCtx   context.Context
	bridgeFactory ACPBridgeFactory
	models        func() []vibekit.SessionModel
	// secrets is the runtime's credential store, shared not copied, so a registration
	// obtained on any bridge is visible from every other. Nil when there is no configDir.
	secrets *secretstore.Store
	hooks   utilitySessionHooks

	// These lifecycle fields are guarded by mu, which is held only for short
	// bookkeeping — NEVER across a bridge Call, or a slow turn starves a recycle.
	lastActiveAt time.Time
	bridge       utilityBridge
	responseCh   chan utilityChunkPayload
	forwardDone  chan struct{}
	gen          uint64
	mu           sync.Mutex

	// enableHooks opts this session into KAS's v2 hook engine; always true in
	// production, so the hooks list/setEnabled RPCs are available.
	enableHooks bool
	started     bool
}

// newUtilitySession constructs a stopped session with the initialization invariants
// explicit: started=false, gen=0, lastActiveAt=zero. shutdownCtx is positional
// because every default for a lifetime is a lifetime nothing can cancel.
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

	// The forward goroutine must be draining NotifCh BEFORE Start: on v3, session/new
	// blocks until the host answers _kiro/auth/getAccessToken and
	// _kiro/terminal/shell_type, which arrive on NotifCh. The channels are locals, so
	// a failed Start leaves no session state behind.
	responseCh := make(chan utilityChunkPayload, 64)
	forwardDone := make(chan struct{})
	// Taken BEFORE the goroutine: the position it reports is comparable only within
	// one attachment, and a lease handed out below carries the same number. A bump on
	// a Start that then FAILS is harmless — us.started stays false, so resetIf
	// returns early for every gen.
	us.gen++
	go us.forward(bridge, us.gen, bridge.NotifCh(), responseCh, forwardDone)

	// The subprocess context is us.shutdownCtx, not a per-request ctx: this call runs
	// under us.mu, so a session/new that never answers would hold the mutex for the
	// process lifetime. Safe as the HANDSHAKE ctx too, since Start bounds that itself.
	if err := bridge.Start(us.shutdownCtx, &vibekit.StartOpts{Lifetime: us.shutdownCtx, Model: model, AgentEngine: resolveAgentEngine(), EnableHooks: us.enableHooks, SecretStorage: us.secrets != nil, Presets: us.sessionPresets(ctx)}); err != nil {
		return err
	}
	us.bridge = bridge
	us.started = true
	us.lastActiveAt = time.Now()
	us.responseCh = responseCh
	us.forwardDone = forwardDone

	// The session id is logged because this session appears in `session/list` owned by
	// no chat, so a row nobody can explain is diagnosed by grepping for it.
	slog.Info("utility bridge started", "model", model, "session_id", string(bridge.SessionID()))
	return nil
}

// stopLocked stops the subprocess and waits for the forward goroutine to exit, so a
// recycle leaks no goroutine. Safe against a wedged forward: forwardChunk's send is
// non-blocking, so a full responseCh cannot park it. Caller holds us.mu.
func (us *utilitySession) stopLocked() {
	us.bridge.Stop()
	if us.forwardDone != nil {
		<-us.forwardDone
	}
	us.started = false
	us.responseCh = nil
	us.forwardDone = nil
}

// resetIf stops the session ONLY when the caller's generation is still the live one.
// A lease-holder that hit an error calls this; once the session has been recycled,
// the reset is a stale complaint about a dead subprocess and is dropped.
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

// stopIfIdle stops the session when it has been inactive since before cutoff,
// reporting whether it did. The victim is stopped asynchronously and forward's exit
// is not awaited, because the cull must never block on a slow teardown.
func (us *utilitySession) stopIfIdle(cutoff time.Time) bool {
	us.mu.Lock()
	shouldStop := us.started && !us.lastActiveAt.IsZero() && us.lastActiveAt.Before(cutoff)
	var victim acpStopper
	if shouldStop {
		// Captured INSIDE the lock: startLocked reassigns us.bridge under us.mu, so
		// reading it after the unlock could stop a freshly-restarted bridge.
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

// liveID returns the ACP session id of the running session, or "" when stopped. The
// orphan-session sweep needs it to exempt this session's on-disk KAS state: no chat
// references it, so the sweep would delete it out from under the live subprocess.
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

// utilitySessionParams builds an ACP parameter map with the bridge's session id
// injected. Separate from command.SessionParams because the utility session's bridge
// does not carry the prompt slot command.Bridge requires.
func utilitySessionParams(bridge acpSession, extra map[string]any) map[string]any {
	m := map[string]any{vibekit.KeySessionID: bridge.SessionID()}
	maps.Copy(m, extra)
	return m
}

// foreignUpdateHook is utilitySessionHooks.onForeignUpdate as forwardChunk takes it:
// a parameter rather than a field read, because forwardChunk is a package function
// and a nil hook is a legitimate wiring.
type foreignUpdateHook func(sessionID string, kind vibekit.ACPUpdateKind, update json.RawMessage) bool

// updateKind reads the sessionUpdate discriminator off a frame's `update` object,
// reporting false when the object cannot be decoded at all. The two answers are kept
// apart because an unrecognised kind is an ordinary frame to ignore, while an
// UNDECODABLE update is the wire having changed shape.
func updateKind(update json.RawMessage) (vibekit.ACPUpdateKind, bool) {
	var base utilityUpdateBase
	if json.Unmarshal(update, &base) != nil {
		return "", false
	}
	return base.Kind, true
}

// utilityUpdateBase extracts the sessionUpdate kind discriminator, decoded from the
// notification's `update` OBJECT and never from `params` — a session/update's params
// are `{sessionId, update:{sessionUpdate, …}}`, so reading the kind off params
// yields "" for every frame.
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

// forward drains the bridge's NotifCh, forwarding agent_chunk text to responseCh.
// Peer requests (msg.ID != nil) go to answerHostRequest, since this session must vend
// the host-mediated auth token and shell type or session/new stalls. Everything else
// but hooks/policy is discarded, which keeps NotifCh from blocking readLoop. bridge is
// passed explicitly so a recycle cannot make this goroutine answer on the wrong pipe.
func (us *utilitySession) forward(bridge acpSessionResponder, gen uint64, notifCh <-chan vibekit.Notification, responseCh chan<- utilityChunkPayload, done chan<- struct{}) {
	defer close(done)
	defer close(responseCh)
	for n := range notifCh {
		msg := n.Msg
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
		case msg.Method == methodV3PolicyChanged, msg.Method == methodV3PolicyError:
			if us.hooks.onPolicyNotification != nil {
				us.hooks.onPolicyNotification(msg)
			}
		default:
			// Read at the call, never captured at goroutine start: the id is empty
			// until session/new answers. SessionID takes the BRIDGE's own mutex, which
			// no bridge method holds across a round trip, so this cannot park readLoop.
			forwardChunk(msg, string(bridge.SessionID()), responseCh, us.hooks.onForeignUpdate)
		}
		// AFTER the frame is handled, so the position reported is one this goroutine
		// has FOLDED rather than merely received. See stepReplays.settleConsumed.
		us.noteFrameDrained(drainPoint{gen: gen, seq: n.Seq}, false)
	}
	// The channel is closed and drained, so nothing can advance the position again:
	// this call is the seal that completes a replay whose trailing frames never came.
	us.noteFrameDrained(drainPoint{gen: gen}, true)
}

// noteFrameDrained reports the folded position to the runtime, tolerating an
// unwired hook so every existing utility test constructs unchanged.
func (us *utilitySession) noteFrameDrained(at drainPoint, force bool) {
	if us.hooks.onFrameDrained != nil {
		us.hooks.onFrameDrained(at, force)
	}
}

// sessionPresets resolves this session's policy presets, tolerating an unwired
// hook. Nil returns none, which is the Custom profile's wire: no key at all.
func (us *utilitySession) sessionPresets(ctx context.Context) []string {
	if us.hooks.presets == nil {
		return nil
	}
	return us.hooks.presets(ctx)
}

// forwardChunk forwards an agent_message_chunk's text to responseCh, ignoring every
// other notification. TWO decodes, because there are two levels: the outer envelope
// names the session and wraps the frame, the frame carries the kind and the content.
// See utilityUpdateBase for what reading the inner fields off the outer object cost.
func forwardChunk(msg *vibekit.RPCResponse, ownSession string, responseCh chan<- utilityChunkPayload, onForeign foreignUpdateHook) {
	if msg.Method != vibekit.MethodSessionUpdate || msg.Params == nil {
		return
	}
	var env translate.ACPSessionUpdateEnvelope
	if json.Unmarshal(msg.Params, &env) != nil || env.Update == nil {
		return
	}
	kind, kindOK := updateKind(env.Update)
	// KAS can hydrate a CHAT's session into this process, and this session denies every
	// tool and persists nothing, so that turn's text belongs to a transcript rather
	// than to whichever UtilityPrompt is draining. An empty id is the window before
	// session/new answered, which no prompt can be draining in.
	if ownSession != "" && env.SessionID != ownSession {
		// A frame somebody is READING is not a surprise, so it is offered before the
		// Warn. An undecodable update is offered to nobody: the projection would
		// consume it and swallow the one signal that says the wire changed shape.
		if kindOK && onForeign != nil && onForeign(env.SessionID, kind, env.Update) {
			return
		}
		slog.Warn("utility bridge: dropping a frame for a foreign session",
			"frame_session", env.SessionID, "utility_session", ownSession)
		return
	}
	if !kindOK || kind != vibekit.ACPUpdateAgentChunk {
		return
	}
	var chunk utilityChunkPayload
	if json.Unmarshal(env.Update, &chunk) == nil {
		// Non-blocking send: a full responseCh (buffer 64) means a wedged or
		// already-drained turn nobody reads, and a blocked forward never loops back
		// to observe notifCh closing, so stopLocked's <-forwardDone under us.mu
		// would never return. Post-turn leftover chunks are noise.
		select {
		case responseCh <- chunk:
		default:
		}
	}
}

// answerHostRequest answers the v3 host-mediated requests the utility session
// receives. getAccessToken and shell_type are on the session-creation critical path
// (session/new stalls without them). `_kiro/hooks/executeHook` is deliberately NOT
// answered — it would run a shell command a hook file names — and falls to -32601.
// A tool request is refused rather than left pending, which would wedge the turn.
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
		// Unreachable with NO mcpServers, but answered anyway because secretStorage is
		// declared by the SHARED bridge initialize whenever us.secrets is non-nil.
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
		// -32601 rather than a bare error: a capability vibekit deliberately does not
		// offer on this session, not a fault it hit while trying.
		_ = bridge.Respond(ctx, *msg.ID, nil, &vibekit.RPCError{
			Code:    vibekit.RPCCodeMethodNotFound,
			Message: "utility session is text-generation only; tools are unavailable",
		})
	default:
		// Answered rather than left pending, which can wedge the turn.
		slog.Warn("utility bridge: unexpected peer request, refusing", "method", msg.Method, "id", *msg.ID)
		// -32601: a deliberate refusal, where -32603 would blame the wrong side.
		_ = bridge.Respond(ctx, *msg.ID, nil, &vibekit.RPCError{
			Code:    vibekit.RPCCodeMethodNotFound,
			Message: "unsupported on the utility session: " + msg.Method,
		})
	}
}
