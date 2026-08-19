package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/push"
	"github.com/cplieger/vibekit/internal/translate"
)

// BridgeCoordinator encapsulates bridge lifecycle management: creating,
// loading, priming, forwarding notifications, model switching, and
// turn finalization. Hub delegates to this coordinator, reducing the
// hub's role to HTTP/SSE dispatch.
type BridgeCoordinator struct {
	bridge         *bridgePlane
	chatStore      api.ChatStore
	broadcast      func(ctx context.Context, e api.ServerEvent)
	translateEvent func(chatID api.ChatID, msg *api.RPCResponse)
	push           api.PushService
	mcpRegistry    *mcpRegistry
	lifecycle      *lifecyclePlane
	preBridgeSpawn func(context.Context)
	// replayProjection is the session/load replay-projection lifecycle,
	// injected from the Hub for the same reason as flushPending: Forward and
	// tryLoadSession drive it, without the coordinator importing the full Hub.
	// Nil in tests that do not exercise a load.
	replayProjection replayProjector
	// onSessionRehydrated fires after a successful session/load, off the
	// spawn path (own goroutine). The hub hangs the restart-paused run resume
	// sweep here: a rehydrated chat is exactly the moment its runs should heal,
	// because the chat's process dying is what paused them. Nil in tests.
	onSessionRehydrated func(api.ChatID)
	// secretStorage reports whether the hub holds a credential store, read at
	// SPAWN time rather than captured as a bool, because newBridgeCoordinator
	// runs before NewHub opens the store — a snapshot here would be false for
	// every bridge this process ever starts. It gates the
	// `_meta.kiro.secretStorage` declaration: see api.StartOpts.SecretStorage
	// for why declaring it without a store breaks an MCP connect.
	secretStorage func() bool
	// chatStatus reads a chat's last self-declared status, which is what the
	// agent-finished push body says instead of a fixed literal. A FUNCTION rather
	// than the cache itself so the coordinator keeps taking no dependency on the
	// SSE plane's internals, and nil-safe for tests that build a coordinator
	// without one.
	chatStatus func(api.ChatID) api.ChatStatusPayload
	// primeFrom notes which chat's transcript should prime a chat's FIRST
	// session, for the tangent whose session/fork was refused (command/fork.go).
	// Consumed by the next spawn and deleted there, so it is a handoff between
	// the fork command and one launch rather than state anything can read twice.
	//
	// Not persisted, deliberately. It describes the launch of one session; after
	// a restart the tangent has its own conversation and the parent's history is
	// no longer owed to it. Bounded by the same fact: a note only exists between
	// a fork refusal and that chat's first prompt.
	//
	// Sits among the pointer fields for govet fieldalignment; its mutex is a
	// non-pointer and stays at the end of the struct.
	primeFrom map[api.ChatID]api.ChatID
	// agentEngine is the kiro-cli agent engine for every bridge this
	// coordinator spawns. Hard-pinned to v3 (KAS) by resolveAgentEngine;
	// vibekit is v3-only.
	agentEngine string
	// acpArgs are the filtered operator launch flags (VIBEKIT_KIRO_ACP_ARGS).
	// Set on the CHAT spawns below and deliberately NOT threaded to the utility
	// bridge, whose work is title generation and catalog fetches — an
	// `--effort max` there would spend real credits on a two-word summary.
	acpArgs     []string
	primeFromMu sync.Mutex
}

// PrimeFromChat records that chatID's first session should be primed with
// sourceChatID's transcript. See BridgeCoordinator.primeFrom.
func (bc *BridgeCoordinator) PrimeFromChat(chatID, sourceChatID api.ChatID) {
	if chatID == "" || sourceChatID == "" || chatID == sourceChatID {
		return
	}
	bc.primeFromMu.Lock()
	defer bc.primeFromMu.Unlock()
	if bc.primeFrom == nil {
		bc.primeFrom = make(map[api.ChatID]api.ChatID, 1)
	}
	bc.primeFrom[chatID] = sourceChatID
}

// takePrimeFrom claims and clears a chat's prime note. Claiming rather than
// reading: the note is spent by the session it primes, so a later bridge for the
// same chat must not re-inject a history that session has already read.
func (bc *BridgeCoordinator) takePrimeFrom(chatID api.ChatID) api.ChatID {
	bc.primeFromMu.Lock()
	defer bc.primeFromMu.Unlock()
	src, ok := bc.primeFrom[chatID]
	if !ok {
		return ""
	}
	delete(bc.primeFrom, chatID)
	return src
}

// newBridgeCoordinator constructs a BridgeCoordinator from the Hub's
// fields. Called once from NewHub after all options are applied.
func newBridgeCoordinator(h *Hub) *BridgeCoordinator {
	return &BridgeCoordinator{
		bridge:         h.bridge,
		chatStore:      h.chatStore,
		broadcast:      h.Broadcast,
		translateEvent: h.translateACPEvent,
		push:           h.push,
		mcpRegistry:    h.mcpRegistry,
		lifecycle:      h.lifecycle,
		preBridgeSpawn: h.preBridgeSpawn,
		// h implements replayProjector via load_projection.go.
		replayProjection: h,
		chatStatus:       h.sse.chatStatus.Get,
		agentEngine:      resolveAgentEngine(),
		acpArgs:          h.acpArgs,
		secretStorage:    func() bool { return h.secrets != nil },
		onSessionRehydrated: func(chatID api.ChatID) {
			ctx, cancel := h.hubContext()
			defer cancel()
			h.resumeRestartPausedRuns(ctx, chatID)
		},
	}
}

// hasSecretStorage reports whether this process holds a credential store, and
// therefore whether a bridge may declare `_meta.kiro.secretStorage`. Nil-safe:
// a coordinator built without the resolver (every test that does not exercise
// the store) declares the capability off, which is the honest answer for a hub
// that has no store either.
func (bc *BridgeCoordinator) hasSecretStorage() bool {
	return bc.secretStorage != nil && bc.secretStorage()
}

// processLifetimeCtx returns the context that bounds a spawned kiro-cli
// subprocess: the hub's shutdown context, so a bridge outlives the turn that
// happened to create it and still dies with the hub.
//
// It must never be the caller's ctx. Every spawn here is reached from a command
// handler, and CmdPrompt's is a per-turn context it cancels on return — see
// api.StartOpts.Lifetime for what that measured like.
//
// It is a plain field read: hub.New requires the hub's lifetime context, so
// there is nothing to be nil-safe against. The context.Background() fallback
// that used to sit here existed for tests building a coordinator without a
// lifecycle plane, and it was the third uncancellable substitution on this one
// path — a test that wants a Stop-owned subprocess says so in its own
// StartOpts now.
func (bc *BridgeCoordinator) processLifetimeCtx() context.Context {
	return bc.lifecycle.shutdownCtx
}

// resolveAgentEngine returns the kiro-cli agent engine, hard-pinned to v3
// (KAS). vibekit is v3-only: the v2 (_kiro.dev/*) wire and its handlers
// were removed, so a stray KIRO_AGENT_ENGINE=v1/v2 is deliberately
// ignored — honoring it would launch a legacy engine vibekit can no
// longer talk to (session/new stalls, every turn fails). v3 requires the
// host to answer _kiro/auth/getAccessToken + _kiro/terminal/shell_type
// (internal/hub/bridge_v3_auth.go). The v2→v3 wire comparison is in
// kiro-cli-research.md.
func resolveAgentEngine() string {
	return api.AgentEngineV3
}

// GetOrCreateBridge returns an existing bridge for chatID, or creates one.
// Concurrent callers for the same chatID coalesce via singleflight, keyed by
// bridgeSpawnKey.
//
//nolint:revive // unexported-return: sharedBridge is package-internal; callers within hub use the methods on it. Exporting would leak ACP wiring outside the hub package.
func (bc *BridgeCoordinator) GetOrCreateBridge(ctx context.Context, chatID api.ChatID, modelOverride string) (*sharedBridge, error) {
	// Fast path: bridge already exists.
	if sb := bc.bridge.mgr.get(chatID); sb != nil {
		return sb, nil
	}

	// Coalesce concurrent spawn attempts for the same chatID+model.
	// Including the model override in the key ensures callers with
	// different model parameters don't coalesce onto the wrong bridge.
	sfKey := bridgeSpawnKey(chatID, modelOverride)
	v, err, _ := bc.bridge.mgr.spawnSF.Do(sfKey, func() (any, error) {
		return bc.spawnBridge(ctx, chatID, modelOverride)
	})
	if err != nil {
		return nil, err
	}
	b, _ := v.(*sharedBridge)
	return b, nil
}

// bridgeSpawnKey composes the bridge-spawn singleflight key over
// (chatID, modelOverride).
//
// NEITHER field can carry keyenc's ':' separator today — api.ValidChatID
// restricts a chat id to [a-zA-Z0-9_-] and api.ValidIdent restricts a model
// id to [A-Za-z0-9_.-] — so this key is unambiguous either way, and because
// both alphabets are separator-free the encoded key is BYTE-IDENTICAL to a
// plain "chatID:modelOverride" concatenation. keyenc is here for uniformity
// with the repo's other composite keys and so the key stays injective if
// either field's alphabet is ever widened: a model id taken verbatim from an
// upstream catalog, say, which is an edit to a validator in another package
// that would not look like it touched key encoding.
//
// Consequence of a collision, concretely: singleflight hands every coalesced
// caller the leader's result, so two distinct (chat, model) pairs sharing a
// key mean the second caller receives the FIRST caller's bridge — a chat
// talking to another chat's kiro-cli session, or a model-override request
// silently served by a bridge running a different model. The pre-keyenc form
// used a 0x00 separator, which is unreachable through both validators rather
// than merely absent from them; the ':' form trades that for an encoding that
// stays injective whatever the fields contain. The key's bytes changed (0x00
// became ':'); nothing persists it, the singleflight group lives on the
// in-memory bridge manager for the life of one spawn.
func bridgeSpawnKey(chatID api.ChatID, modelOverride string) string {
	return keyenc.Join(string(chatID), modelOverride)
}

// spawnBridge creates (or returns the just-created) bridge for chatID.
// It runs inside the singleflight so concurrent callers coalesce. It
// resolves the model (the override beats the chat's stored value),
// tries session/load when an ACP session id is stored, and otherwise
// starts a fresh session/new. On any start failure it rolls the
// half-registered bridge back out of the map and returns the error.
func (bc *BridgeCoordinator) spawnBridge(ctx context.Context, chatID api.ChatID, modelOverride string) (*sharedBridge, error) {
	// Double-check after winning the singleflight race.
	sb, existed := bc.bridge.mgr.getOrInsert(chatID)
	if existed {
		return sb, nil
	}

	setupErr := func(err error) error {
		bc.bridge.mgr.removeIfSame(chatID, sb)
		sb.state = bridgeIdle
		return err
	}

	chat, exists := bc.chatStore.Get(ctx, chatID)
	if !exists {
		return nil, setupErr(fmt.Errorf("chat %s not found", chatID))
	}

	model := chat.Model
	if modelOverride != "" && modelOverride != modelAuto {
		model = modelOverride
	}
	// Withhold a model the account cannot run, SILENTLY, and let the session take
	// the backend's own default.
	//
	// This is the inherited-value case: `model` here comes from the persisted
	// chat field (written from the client's cross-device `last_model`) or from a
	// caller's override, not from a pick the user just made. kiro-cli accepts the
	// `--model` flag either way and only the service rejects it, mid-prompt, on
	// every later turn -- so a stale entitlement makes every turn of every new
	// chat fail while the picker still shows the model as selected. Silence is
	// right for a value nobody chose in this moment; an explicit pick is refused
	// loudly instead, in cmdSwitchModel.
	//
	// The evidence is the LAST session's advertised set, because the launch flag
	// is built before this session has one. Empty means unknowable and allows.
	if !api.ModelServed(model, chat.ServedModelIDs) {
		slog.Warn("withholding a model this account does not serve; using the backend default",
			"chat_id", chatID, "model", model)
		model = ""
	}

	// No mcpServers parameter. KAS reads the user's servers from its own
	// hot-reloading config file, which vibekit renders (internal/mcp/kasfile.go).
	// Sending them inline as well would WIN over the file (KAS merges
	// `client > file-based`) and make every config edit look like a no-op.
	if bc.preBridgeSpawn != nil {
		bc.preBridgeSpawn(ctx)
	}

	if chat.ACPSessionID != "" {
		if bc.tryLoadSession(ctx, chatID, sb, chat.ACPSessionID, model) {
			return sb, nil
		}
	}

	// EnableHooks:true opts chat bridges into KAS's v2 hook engine
	// (_meta.kiro.hooks={enabled,v2} in the initialize handshake) so the
	// workspace's user-authored .kiro/hooks/*.json hooks AUTOFIRE on their
	// triggers (SessionStart / UserPromptSubmit / PreToolUse / PostToolUse)
	// during a turn. In v2 mode KAS loads the hook files itself and RUNS
	// runCommand hooks internally (its own process runner, in the workspace),
	// exactly as the Kiro IDE and kiro-cli TUI do — it does NOT call back the
	// client's _kiro/hooks/executeHook for autofire (verified on the live v3
	// wire: chat autofire produced zero executeHook callbacks; the hook
	// commands ran internally). So no chat-bridge executeHook handler is
	// needed. Same trust model as vibekit's `!cmd` interception: the hooks are
	// the user's own automation. The utility bridge keeps its own EnableHooks
	// for the hooks dashboard (list/setEnabled/Run-now); see hub/hooks.go.
	//
	// Forward MUST be draining NotifCh before Start: on v3 (KAS) the agent
	// sends _kiro/auth/getAccessToken and _kiro/terminal/shell_type as
	// server->client REQUESTS on the session-creation critical path, and
	// session/new does not return until they are answered. Attaching the
	// forward goroutine after Start deadlocks every fresh session. If Start
	// fails it stops the bridge (NotifCh closes), so this goroutine exits.
	go bc.Forward(chatID, sb.bridge)
	// Supervised is passed at creation and only here: the session/load path below
	// does not repeat it, because KAS persists `autopilot` in its own session
	// metadata and a loaded session already carries the value.
	if err := sb.bridge.Start(ctx, &api.StartOpts{Lifetime: bc.processLifetimeCtx(), Model: model, Mode: chat.CurrentModeID, Effort: chat.Effort, AgentEngine: bc.agentEngine, EnableHooks: true, ExtraArgs: bc.acpArgs, Supervised: chat.SupervisedMode, SecretStorage: bc.hasSecretStorage()}); err != nil {
		return nil, setupErr(err)
	}
	bc.persistNewSessionMetadata(ctx, chatID, sb.bridge)

	sb.primed = false
	// There is no rewind degrade path. It existed because a rewind FORKED a
	// session, and a failed fork left a second chat showing a truncated
	// transcript the fresh session knew nothing about. A rewind reverts the
	// session it is already in, so there is nothing to re-inject.
	//
	// A TANGENT's refused fork is the same SHAPE and a different operation, and
	// it does need the injection: this session has never seen the conversation
	// the user opened it from, and that conversation lives in another chat. The
	// note is claimed here rather than read at prime time so it is spent by
	// exactly one session (see takePrimeFrom).
	if src := bc.takePrimeFrom(chatID); src != "" {
		sb.primeReason = primeReasonFork
		sb.primeFrom = src
	}
	sb.state = bridgeIdle

	return sb, nil
}

// tryLoadSession attempts session/load against the stored ACP session id.
func (bc *BridgeCoordinator) tryLoadSession(
	ctx context.Context, chatID api.ChatID, sb *sharedBridge, acpSessionID, model string,
) bool {
	// EnableHooks:true — the session/load path must opt into the hook engine
	// too, so hooks autofire on resumed sessions after a container restart
	// (same rationale as spawnBridge's session/new path above).
	//
	// Forward attaches BEFORE Start for the same reason as the fresh
	// session/new path: v3 session/load also blocks on the host answering
	// _kiro/auth/getAccessToken. On load failure the old bridge is swapped
	// out of sb before it is stopped, so this goroutine's exit cleanup
	// (removeIfBridge, identity-compared) cannot evict the replacement.
	// Open the replay projection BEFORE Forward attaches, so the first replayed
	// frame already has somewhere to land. KAS starts replaying as soon as it
	// accepts session/load, which is inside Start below.
	if bc.replayProjection != nil {
		bc.replayProjection.OpenReplayProjection(chatID)
	}
	go bc.Forward(chatID, sb.bridge)
	if err := sb.bridge.Start(ctx, &api.StartOpts{Lifetime: bc.processLifetimeCtx(), SessionID: acpSessionID, Model: model, AgentEngine: bc.agentEngine, EnableHooks: true, ExtraArgs: bc.acpArgs, SecretStorage: bc.hasSecretStorage()}); err != nil {
		slog.Warn("session/load failed, starting new",
			"chat_id", chatID, "acp_session", acpSessionID, "error", err)
		// A failed load has no transcript to adopt, so whatever partial replay
		// arrived must not survive into the fresh session.
		if bc.replayProjection != nil {
			bc.replayProjection.DiscardReplayProjection(chatID)
		}
		old := sb.bridge
		sb.bridge = bc.bridge.mgr.factory()
		old.Stop()
		// The replacement session has never seen this chat. Record why it needs
		// priming, or maybePrime's default arm returns and the agent answers the
		// next prompt with no idea what came before it.
		sb.primeReason = primeReasonReload
		if mErr := bc.chatStore.Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
			if !ex {
				return false
			}
			// Detach from the stale session but KEEP it in the chain: its
			// directory still holds that period's transcript and pre-images,
			// and blanking the id outright made the reaper sweep it as an
			// orphan — vibekit deleting its own history.
			c.RecordSession("")
			return true
		}); mErr != nil {
			slog.Error("clear stale acp_session_id", "chat_id", chatID, "error", mErr)
		}
		return false
	}
	// The load returned, which is the other half of the settle condition. The
	// settle itself belongs to Forward — see load_projection.go's header for why
	// this goroutine cannot safely decide the replay is drained.
	if bc.replayProjection != nil {
		bc.replayProjection.MarkReplayLoadDone(chatID)
	}
	title := sb.bridge.SessionTitle()
	if mErr := bc.chatStore.Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.CurrentModeID = sb.bridge.CurrentMode()
		c.AvailableModes = sb.bridge.Modes()
		c.AvailableModels = sb.bridge.Models()
		adoptKASTitle(c, title)
		return true
	}); mErr != nil {
		slog.Error("refresh session metadata", "chat_id", chatID, "error", mErr)
	}
	sb.primed = true
	sb.state = bridgeIdle
	// The chat is back; heal its restart-paused runs. Off the spawn path — the
	// user's prompt must not wait behind a run-list round trip — and AFTER the
	// state flip, so the resume's own bridge Call finds an idle bridge.
	if bc.onSessionRehydrated != nil {
		go bc.onSessionRehydrated(chatID)
	}
	return true
}

// persistNewSessionMetadata stores the ACP session id, model, and session-
// level metadata into the chat after a fresh session/new call.
// kasDefaultSessionTitle is KAS's own placeholder title, spread onto every
// session/new result's _meta (DEFAULT_SESSION_TITLE in the KAS bundle). It
// carries no information, so adopting it would swap vibekit's placeholder for
// a worse one AND make the chat non-default-named, which then rejects the real
// title that arrives later. Probed 2026-08-02: session/new always returns it.
const kasDefaultSessionTitle = "New Session"

// adoptKASTitle names a chat from KAS's own session title, but only while the
// chat has no name of its own and only when the title is real.
//
// Precedence on chat naming is: agent-authored focus_update title > local
// first-prompt label > KAS's session title. So this fires for a chat vibekit
// never named — in practice a session/load whose stored title KAS still has and
// vibekit lost. It deliberately never overwrites: `titleIsPromptDerived`
// (translate/focus.go) implements the top of that ordering on the focus channel,
// and this implements the bottom.
func adoptKASTitle(c *api.Chat, title string) {
	if title == "" || title == kasDefaultSessionTitle || c.Name != api.DefaultChatName {
		return
	}
	c.Name = title
}

func (bc *BridgeCoordinator) persistNewSessionMetadata(ctx context.Context, chatID api.ChatID, bridge api.ACPBridge) {
	newSessionID := bridge.SessionID()
	newModelID := bridge.ModelID()
	currentMode := bridge.CurrentMode()
	modes := bridge.Modes()
	models := bridge.Models()
	served := bridge.ServedModels()
	title := bridge.SessionTitle()
	// requestedMode is the mode the chat asked for, read before the line below
	// overwrites it with the mode the session actually got.
	var requestedMode string
	if err := bc.chatStore.Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
		if !ex {
			return false
		}
		requestedMode = c.CurrentModeID
		c.RecordSession(string(newSessionID))
		if newModelID != "" {
			c.Model = string(newModelID)
		}
		c.CurrentModeID = currentMode
		c.AvailableModes = modes
		c.AvailableModels = models
		c.ServedModelIDs = served
		adoptKASTitle(c, title)
		return true
	}); err != nil {
		slog.Error("persist new session metadata",
			"chat_id", chatID,
			"acp_session", newSessionID,
			"model", newModelID,
			"error", err)
	}
	bc.reportModeNotApplied(ctx, chatID, requestedMode, currentMode)
}

// reportModeNotApplied tells the user when the session did not get the mode the
// chat asked for.
//
// Storing the ACTUAL mode above is right: the mode pill must not claim a role the
// agent is not running under. What was wrong is that it was the ONLY record of the
// request, so a single transient `session/set_mode` failure (applyInitialMode
// warns and continues, deliberately, so a mode problem never costs the user their
// session) silently and permanently converted a chat pinned to `spec` into a
// default-mode chat: at the next spawn the requested id now EQUALS the current
// one, so applyInitialMode's own guard means no retry is ever attempted.
//
// A visible banner rather than an automatic retry, because the truthful pill plus
// a named mismatch puts the choice back in front of a user who is present, and a
// retry would need a second persisted mode field to remember an intent the record
// no longer holds. This is not a privilege gate: tool authorization is Cedar's,
// and the engine default is not the least constrained bundled mode.
func (bc *BridgeCoordinator) reportModeNotApplied(ctx context.Context, chatID api.ChatID, requested, actual string) {
	if requested == "" || requested == actual {
		return
	}
	slog.Error("session mode not applied; the chat's mode was reset to the session's",
		"chat_id", chatID, "requested", requested, "actual", actual)
	bc.broadcast(ctx, api.NewEvent(api.EventError, chatID, api.ErrorPayload{
		Code: api.ErrCodeModeNotApplied,
		Message: "Could not start this chat in \"" + requested + "\" mode; it is running as \"" +
			actual + "\". Pick the mode again to retry.",
	}))
}

// GetBridge returns the bridge for chatID, or nil.
//
//nolint:revive // unexported-return: see GetOrCreateBridge above.
func (bc *BridgeCoordinator) GetBridge(chatID api.ChatID) *sharedBridge {
	return bc.bridge.mgr.get(chatID)
}

// HasLiveBridge reports whether a chat currently has a bridge, i.e. whether it
// is in active use. Retention's exemption reads this: a chat with a live bridge
// is open work and is never purged, however old (see archive.WithLiveChats).
func (h *Hub) HasLiveBridge(chatID api.ChatID) bool {
	return h.bridge.mgr.get(chatID) != nil
}

// CloseBridge stops a bridge and removes it from the map.
func (bc *BridgeCoordinator) CloseBridge(chatID api.ChatID) {
	bc.bridge.mgr.close(chatID)
}

// replayProjector is the slice of the Hub's replay-projection lifecycle the
// coordinator drives. See hub/load_projection.go for the settle barrier.
type replayProjector interface {
	OpenReplayProjection(api.ChatID)
	MarkReplayLoadDone(api.ChatID)
	DiscardReplayProjection(api.ChatID)
	SettleReplayProjection(chatID api.ChatID, buffered int, force bool)
}

// Forward is the ACP notification → domain event translator, run as a
// goroutine per bridge.
func (bc *BridgeCoordinator) Forward(chatID api.ChatID, bridge api.ACPBridge) {
	ch := bridge.NotifCh()
	for msg := range ch {
		bc.translateEvent(chatID, msg)
		// Settle a session/load replay projection here rather than at Start's
		// return: this goroutine is the one draining the frames, so its own
		// view of the channel depth is the only sound completion signal.
		// len() on a receive-only channel is the whole barrier — no timeout,
		// no extra bridge API. Rationale in hub/load_projection.go.
		if bc.replayProjection != nil {
			bc.replayProjection.SettleReplayProjection(chatID, len(ch), false)
		}
	}
	// The channel closed, so no further frame can arrive to trigger the check
	// above. Force the settle so a load whose trailing catalog frames never
	// came still completes instead of leaking a projection.
	if bc.replayProjection != nil {
		bc.replayProjection.SettleReplayProjection(chatID, 0, true)
	}

	slog.Info("bridge exited", "chat_id", chatID)

	bc.bridge.mgr.removeIfBridge(chatID, bridge)

	// Flush staged writes for the chat. A bridge exit (crash, or a
	// model-switch CloseBridge) leaves the supervised fs-handler goroutine
	// parked on its resume channel and a phantom "awaiting approval"
	// pending op that would replay to reconnecting clients. Cancel, delete,
	// and mode-disable already flush; this is the bridge-exit sibling.
	lastBridge := bc.bridge.mgr.count() == 0

	if lastBridge {
		bc.mcpRegistry.clearAll(bc.lifecycle.shutdownCtx)
	}
}

// PrimeIfNeeded sends the chat history as an ephemeral priming prompt on
// the current bridge.
func (bc *BridgeCoordinator) PrimeIfNeeded(ctx context.Context, chatID api.ChatID, sb *sharedBridge) {
	// Preambles live in translate (PrimePreamble*) because the focus-title
	// derivation filter must recognise a title KAS derives from this prime
	// text — one definition keeps the filter and the prime in lockstep.
	//
	// The reason decides the preamble AND whose history is read. Every reason but
	// the tangent's primes a session with its OWN chat's transcript; a tangent
	// whose fork was refused has no transcript of its own yet and needs the
	// parent's, which is why the source is read off the bridge rather than assumed
	// to be chatID.
	var prime string
	source := chatID
	switch sb.primeReason {
	case primeReasonSwitch:
		prime = translate.PrimePreambleSwitch
	case primeReasonReload:
		prime = translate.PrimePreambleReload
	case primeReasonFork:
		prime = translate.PrimePreambleTangent
		if sb.primeFrom != "" {
			source = sb.primeFrom
		}
	default:
		return
	}

	history := bc.chatStore.BuildHistory(ctx, source)
	if history == "" {
		return
	}
	prime += history

	slog.Info("priming bridge", "chat_id", chatID, "reason", sb.primeReason, "history_from", source)
	_, err := sb.bridge.Call(ctx, api.MethodPrompt, command.SessionParams(sb, map[string]any{
		"prompt": []map[string]any{api.TextBlock(prime)},
	}))
	if err != nil {
		slog.Error("prime failed", "chat_id", chatID, "error", err)
	}
}

// There is no effortForModel and no modelEffortSetting. Effort was one GLOBAL
// `model_effort` setting shaped `{last_model, effort}`, so it was keyed by the
// LAST model rather than by the chat: two chats could not disagree, and
// switching models discarded the previous model's level outright. It is a field
// on the chat record now (api.Chat.Effort), read straight off the chat at spawn
// and written by CmdSetEffort, so the launch flag needs no settings read.

// NotifyPush sends a push notification about one CHAT if the push service is
// configured.
//
// It keeps its chat-id parameter rather than taking an api.PushSubject: every
// caller here is chat-scoped (a turn ended, a permission ask, an agent question),
// so the conversion belongs at this one boundary instead of at each of them. A
// notification with no chat behind it — the PR poller's — does not come through
// here at all; it calls push.Send with its own subject.
func (bc *BridgeCoordinator) NotifyPush(ctx context.Context, body string, kind api.PushKind, chatID api.ChatID) {
	if bc.push == nil || !bc.push.HasSubscribers() {
		return
	}
	bc.lifecycle.inflight.Go(func() {
		bc.push.Send(ctx, push.DefaultTitle, body, kind, api.ChatSubject(chatID))
	})
}

// TakeBuffer returns and removes the chat's assistant buffer.
func (bc *BridgeCoordinator) TakeBuffer(chatID api.ChatID) (*buffer.Buffer, bool) {
	return bc.bridge.assistantBufs.Take(chatID)
}

// EmitTurnEndedWithStats finalizes any in-flight assistant message
// and broadcasts turn_ended with the credit delta and elapsed time.
func (bc *BridgeCoordinator) EmitTurnEndedWithStats(ctx context.Context, chatID api.ChatID, resp *api.RPCResponse, stats command.TurnStats) {
	stopReason := extractStopReason(resp)
	// Read BEFORE the turn_ended broadcast below: emit() clears the chat's status
	// as that event goes out, so a read at the push site always finds nothing. See
	// statusDescription.
	statusDesc := bc.statusDescription(chatID)

	var changedFiles map[string]*api.FileChange
	var refusal *api.RefusalInfo
	var model string

	if buf, ok := bc.TakeBuffer(chatID); ok && buf.Started {
		// Settle whatever the steering-marker filter was still withholding. A
		// carry carrying the committing prefix is an unclosed marker and is
		// dropped; a shorter one is prose that merely looked like the start of
		// one, and goes back into the turn. Must precede assistantTurnMessage,
		// which is what reads the buffer into the persisted message.
		translate.FlushSteerCarry(buf)
		changedFiles = buf.ChangedFiles
		refusal = buf.Refusal
		model = buf.Model
		if stopReason == stopReasonCancelled {
			changed := buf.MarkCancelledToolsFailed()
			for i := range changed {
				bc.broadcast(ctx, api.NewEvent(api.EventToolCallUpdate, chatID, api.ToolCallUpdatePayload{MessageID: buf.MessageID, ToolCall: changed[i]}))
			}
		}

		msg := assistantTurnMessage(buf, stats)
		bc.persistTurn(ctx, chatID, &msg)
	}

	if stopReason == stopReasonCancelled {
		evt := api.Message{
			ID:        newMessageID(),
			Role:      api.RoleEvent,
			Ts:        time.Now().UnixMilli(),
			EventKind: api.EventCancelled,
		}
		if err := bc.chatStore.AppendMessage(ctx, chatID, &evt); err != nil {
			slog.Error("persist cancel event", "chat_id", chatID, "error", err)
		}
	}

	if _, stillExists := bc.chatStore.Get(ctx, chatID); stillExists {
		bc.broadcast(ctx, api.NewEvent(api.EventTurnEnded, chatID, api.TurnEndedPayload{
			StopReason:   stopReason,
			Refusal:      refusal,
			Model:        model,
			CreditsDelta: stats.CreditsDelta,
			ElapsedMs:    stats.ElapsedMs,
			ChangedFiles: changedFiles,
		}))
	}

	if stopReason != stopReasonCancelled {
		bc.NotifyPush(ctx, agentFinishedBodyFrom(statusDesc), api.PushKindAgentFinished, chatID)
	}
}

// agentFinishedBody is what the agent-finished notification SAYS.
//
// It was the fixed literal "Agent finished", which tells a reader nothing about
// which of three background chats just came back. The agent's own one-line
// self-description is already server-side, in memory, on the Hub, keyed by the
// chat id this method is handed: `chat_status` arrives on KAS's focus_update
// channel (the model's update_session_information tool). So no new wire field, no
// new call site — a read at the push site.
//
// ORDERING IS THE TRAP, and it is why the caller reads the description early
// rather than here. The status cache is cleared at turn end by design (so a bare
// replay cannot resurrect a stale "in_progress"), and the clear runs inside emit()
// as the turn_ended event goes out — the broadcast that sits between the top of
// EmitTurnEndedWithStats and this push. Reading the cache at the push site would
// therefore always find the entry already gone and always fall back to the
// literal, silently. So the read happens before the broadcast and travels here as
// a string.
//
// EMPTY IS LEGITIMATE. An agent need never call update_session_information, so the
// description is often "" and the literal is the honest fallback rather than a
// defensive branch. Length needs no cap: fitToCap trims the body against the
// marshaled payload cap and logs a Warn, so an oversize description is delivered
// truncated rather than dropped.
func (bc *BridgeCoordinator) statusDescription(chatID api.ChatID) string {
	if bc.chatStatus == nil {
		return ""
	}
	return bc.chatStatus(chatID).Description
}

// defaultAgentFinishedBody is the body for a turn whose agent never declared what
// it was doing.
const defaultAgentFinishedBody = "Agent finished"

// agentFinishedBodyFrom picks the body from a chat's self-declared description.
// Split out from the cache read so the choice is testable without a Hub.
func agentFinishedBodyFrom(description string) string {
	if d := strings.TrimSpace(description); d != "" {
		return d
	}
	return defaultAgentFinishedBody
}

// persistTurn commits the finalized assistant turn to the chat file.
//
// A failed append used to be survivable: the .partial sidecar held the only
// durable copy and boot recovery re-imported it. That sidecar is gone, and the
// replacement is KAS's own log — measured to flush each sub-message as it
// COMPLETES, so a session/load replay carries the turn and the projection
// rebuilds it. What neither covers is the final streaming fragment, which is
// the durability this deletion gives up; the old .partial gave it up too,
// within its 500ms throttle.
func (bc *BridgeCoordinator) persistTurn(ctx context.Context, chatID api.ChatID, msg *api.Message) {
	if err := bc.chatStore.AppendMessage(ctx, chatID, msg); err != nil {
		slog.Error("persist assistant turn; the replay projection is the fallback",
			"chat_id", chatID, "error", err)
	}
}

// TryFastModelSwitch attempts an in-session model swap via
// session/set_config_option (configId "model") on the running bridge.
func (bc *BridgeCoordinator) TryFastModelSwitch(ctx context.Context, chatID api.ChatID, model string) bool {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return false
	}
	if err := sb.bridge.SetModel(ctx, model); err != nil {
		slog.Info("model switch: fast path failed, falling back to restart",
			"chat_id", chatID, "model", model, "error", err)
		return false
	}
	slog.Info("model switch: fast path succeeded (session/set_config_option)",
		"chat_id", chatID, "model", model)
	return true
}

// PersistModelSwitch records the switch event and updates the chat's
// model + resets usage counters.
func (bc *BridgeCoordinator) PersistModelSwitch(ctx context.Context, chatID api.ChatID, model string, contextSize int) {
	evt := api.Message{
		ID:        newMessageID(),
		Role:      api.RoleEvent,
		Ts:        time.Now().UnixMilli(),
		EventKind: api.EventModelSwitched,
		Content:   model,
	}
	if err := bc.chatStore.AppendMessage(ctx, chatID, &evt); err != nil {
		slog.Error("switch_model: append event", "chat_id", chatID, "error", err)
	}
	if err := bc.chatStore.Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.Model = model
		c.Usage = api.Usage{ContextSize: contextSize}
		return true
	}); err != nil {
		slog.Error("switch_model: persist model", "chat_id", chatID, "error", err)
	}
}

// FlushInFlightTurnOnSwitch drops the assistant buffer for chatID before a
// bridge restart, announcing the interruption when a turn was in flight.
func (bc *BridgeCoordinator) FlushInFlightTurnOnSwitch(ctx context.Context, chatID api.ChatID) {
	buf, ok := bc.TakeBuffer(chatID)
	if !ok || !buf.Started {
		return
	}
	bc.broadcast(ctx, api.NewEvent(api.EventTurnEnded, chatID, api.TurnEndedPayload{StopReason: api.StopReasonInterrupted}))
}

// assistantTurnMessage builds the persisted assistant message from a finished
// buffer. Extracted so the interrupted path (AbandonInFlightTurn) and the normal
// path (EmitTurnEndedWithStats) cannot drift: every field below exists because
// something in the client reads it after a reload, so a second hand-written
// literal would quietly lose one of them.
func assistantTurnMessage(buf *buffer.Buffer, stats command.TurnStats) api.Message {
	return api.Message{
		ID:        buf.MessageID,
		Role:      api.RoleAssistant,
		Ts:        time.Now().UnixMilli(),
		Content:   buf.Content.String(),
		Reasoning: buf.Reasoning.String(),
		ToolCalls: buf.ToolCalls,
		// Blocks captures the chronological text/tool/thinking
		// emission order; client renderers prefer it over
		// Content+ToolCalls so a turn renders the way the agent
		// actually produced it (text, tool, more text, another
		// tool, …) rather than collapsing all text to the top.
		Blocks: buf.Blocks,
		// CodeReferences persists the turn's licensed-code
		// attributions so the chip survives reload (the streamed
		// assistant turn is never re-broadcast as message_appended).
		CodeReferences: buf.CodeReferences,
		// Refusal metadata (kiro-cli 2.13): stamped from the refusal
		// explanation chunk; persisting it here is what makes the
		// refusal callout survive reload.
		Refusal: buf.Refusal,
		// Turn summary (credits · elapsed · files changed) — persisted on
		// the message so the turn footer survives reload. The same values
		// ride the turn_ended SSE for the live render; omitempty drops the
		// zero/nil cases (a read-only or zero-cost turn carries no footer).
		TurnCredits:   stats.CreditsDelta,
		TurnElapsedMs: stats.ElapsedMs,
		ChangedFiles:  buf.ChangedFiles,
		// Which model answered, latched when the turn opened rather than read
		// from the chat now: the chat's Model is the CURRENT one, and a footer
		// derived from it would relabel history on every switch.
		TurnModel: buf.Model,
	}
}

// AbandonInFlightTurn finalizes a turn that failed before it could end, and it
// PERSISTS the partial rather than dropping it.
//
// The bug it closes: CmdPrompt's error arm returned without any call that takes
// the assistant buffer, so the buffer survived with Started == true. The next
// prompt's ensureTurnStarted then saw a started buffer, emitted no
// message_created, and extended the dead turn's blocks under the dead turn's
// message id -- one persisted assistant message holding two turns' replies,
// with the second turn's text appearing under the first turn's header.
//
// Persist rather than discard, deliberately. FlushInFlightTurnOnSwitch drops the
// buffer, which is right for a model switch (the user asked for a different
// answer and the old one is moot), and wrong here: the user watched this text
// stream in, and invariant 1 says the client never shows what the server has not
// persisted. Dropping it makes the transcript diverge on reload, which is the
// vanishing-message class this codebase already paid for once. It also restores
// the `interrupted` badge for this path, which vibekit-runtime.md records as a
// casualty of deleting the .partial sidecar.
func (bc *BridgeCoordinator) AbandonInFlightTurn(ctx context.Context, chatID api.ChatID) {
	buf, ok := bc.TakeBuffer(chatID)
	if !ok || !buf.Started {
		return
	}
	// Fail the tool calls still marked in-flight BEFORE building the message.
	// Without this the persisted turn carries running tool cards, and a reload
	// renders permanent spinners for work that stopped when the prompt failed --
	// the same reason EmitTurnEndedWithStats does it on the cancel path.
	changed := buf.MarkCancelledToolsFailed()
	for i := range changed {
		bc.broadcast(ctx, api.NewEvent(api.EventToolCallUpdate, chatID,
			api.ToolCallUpdatePayload{MessageID: buf.MessageID, ToolCall: changed[i]}))
	}

	// No stats: an abandoned turn has no credit delta to attribute and no
	// meaningful elapsed time, so the footer is deliberately empty rather than
	// carrying whatever the failed call happened to have consumed.
	msg := assistantTurnMessage(buf, command.TurnStats{})
	bc.persistTurn(ctx, chatID, &msg)

	evt := api.Message{
		ID:        newMessageID(),
		Role:      api.RoleEvent,
		Ts:        time.Now().UnixMilli(),
		EventKind: api.EventInterrupted,
	}
	if err := bc.chatStore.AppendMessage(ctx, chatID, &evt); err != nil {
		slog.Error("persist interrupted event", "chat_id", chatID, "error", err)
	}
	bc.broadcast(ctx, api.NewEvent(api.EventTurnEnded, chatID,
		api.TurnEndedPayload{StopReason: api.StopReasonInterrupted}))
}

const stopReasonCancelled = api.StopReasonCancelled

func extractStopReason(resp *api.RPCResponse) api.StopReason {
	if resp == nil || resp.Result == nil {
		return ""
	}
	var result struct {
		StopReason api.StopReason `json:"stopReason"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		slog.Debug("turn_ended: parse result", "error", err)
		return ""
	}
	return result.StopReason
}
