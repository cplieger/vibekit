package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/command"
	"github.com/cplieger/vibekit/internal/push"
	"github.com/cplieger/vibekit/internal/settings"
	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// BridgeCoordinator encapsulates bridge lifecycle management: creating,
// loading, priming, forwarding notifications, model switching, and
// turn finalization. Runtime delegates to this coordinator, reducing the
// runtime's role to HTTP/SSE dispatch.
type BridgeCoordinator struct {
	bridge    *bridges
	chatStore bridgeChatRecords
	// turns is the per-chat turn lifecycle: one record per turn, and the
	// exclusion every terminal step claims through. See turn.go.
	turns          *turnRegistry
	broadcast      func(ctx context.Context, e vibekit.ServerEvent)
	translateEvent func(chatID vibekit.ChatID, msg *vibekit.RPCResponse)
	// push is optional: WithPush is not passed in tests, and every send site
	// nil-checks — no push service means no notification, which is the right
	// degradation rather than a refusal to run.
	push        pushNotifier `wiring:"optional"`
	mcpRegistry *mcpRegistry
	lifecycle   *lifetime
	// installed later by SetPreBridgeSpawn from the composition root.
	preBridgeSpawn func(context.Context) `wiring:"optional"`
	// replayProjection is the session/load replay-projection lifecycle,
	// injected from the Runtime for the same reason as flushPending: Forward and
	// tryLoadSession drive it, without the coordinator importing the full Runtime.
	// Nil in tests that do not exercise a load.
	replayProjection replayProjector
	// onSessionRehydrated fires after a successful session/load, off the
	// spawn path (own goroutine). The runtime hangs the restart-paused run resume
	// sweep here: a rehydrated chat is exactly the moment its runs should heal,
	// because the chat's process dying is what paused them. Nil in tests.
	onSessionRehydrated func(vibekit.ChatID)
	// onTurnClosed fires from the WINNING closer, once a turn has finalized, so a
	// registry keyed on the turn's identity learns the boundary from the lifecycle
	// rather than from a caller that may have lost the claim. The agent-terminal
	// registry is its one consumer: it evicts that turn's retired output.
	//
	// A back-edge, because the terminals are built after the coordinator; a
	// closure over the runtime rather than the collaborator itself, so a field
	// still nil at this literal cannot be captured. Nil in tests, which is why
	// finalizeTurn guards it.
	onTurnClosed func(vibekit.ChatID, vibekit.TurnEpoch)
	// secretStorage reports whether the runtime holds a credential store, read
	// at SPAWN time rather than captured as a bool (newBridgeCoordinator runs
	// before NewHub opens the store, so a snapshot here would be false for
	// every bridge this process ever starts). Gates the `_meta.kiro.secretStorage`
	// declaration; nil means declare it off.
	secretStorage func() bool `wiring:"optional"`
	// chatStatus reads a chat's last self-declared status, which is what the
	// agent-finished push body says instead of a fixed literal. A FUNCTION rather
	// than the cache itself so the coordinator keeps taking no dependency on the
	// event bus's internals, and nil-safe for tests that build a coordinator
	// without one.
	chatStatus func(vibekit.ChatID) vibekit.ChatStatusPayload
	// unknownStops records the stop reasons this process has already warned about,
	// so a wire value vibekit does not map produces one line rather than one per
	// turn. The set of distinct values is bounded by what KAS can send, and every
	// member is a value a human wants to see exactly once.
	unknownStops sync.Map
	// primeFrom notes which chat's transcript should prime a chat's FIRST
	// session, for the tangent whose session/fork was refused (command/fork.go).
	// Consumed and deleted by the next spawn, so it is a handoff between the
	// fork command and one launch, not state anything can read twice. Not
	// persisted: after a restart the tangent has its own conversation and the
	// parent's history is no longer owed to it.
	//
	// Sits among the pointer fields for govet fieldalignment; its mutex is a
	// non-pointer and stays at the end of the struct.
	primeFrom map[vibekit.ChatID]vibekit.ChatID
	// agentEngine is the kiro-cli agent engine for every bridge this
	// coordinator spawns. Hard-pinned to v3 (KAS) by resolveAgentEngine;
	// vibekit is v3-only.
	agentEngine string
	// acpArgs are the filtered operator launch flags (VIBEKIT_KIRO_ACP_ARGS).
	// Set on the CHAT spawns below and deliberately NOT threaded to the utility
	// bridge, whose work is title generation and catalog fetches — an
	// `--effort max` there would spend real credits on a two-word summary.
	// operator launch flags; absent is the normal case.
	acpArgs     []string `wiring:"optional"`
	primeFromMu sync.Mutex
}

// PrimeFromChat records that chatID's first session should be primed with
// sourceChatID's transcript. See BridgeCoordinator.primeFrom.
func (bc *BridgeCoordinator) PrimeFromChat(chatID, sourceChatID vibekit.ChatID) {
	if chatID == "" || sourceChatID == "" || chatID == sourceChatID {
		return
	}
	bc.primeFromMu.Lock()
	defer bc.primeFromMu.Unlock()
	if bc.primeFrom == nil {
		bc.primeFrom = make(map[vibekit.ChatID]vibekit.ChatID, 1)
	}
	bc.primeFrom[chatID] = sourceChatID
}

// takePrimeFrom claims and clears a chat's prime note. Claiming rather than
// reading: the note is spent by the session it primes, so a later bridge for the
// same chat must not re-inject a history that session has already read.
func (bc *BridgeCoordinator) takePrimeFrom(chatID vibekit.ChatID) vibekit.ChatID {
	bc.primeFromMu.Lock()
	defer bc.primeFromMu.Unlock()
	src, ok := bc.primeFrom[chatID]
	if !ok {
		return ""
	}
	delete(bc.primeFrom, chatID)
	return src
}

// newBridgeCoordinator constructs a BridgeCoordinator from the Runtime's
// fields. Called once from NewHub after all options are applied.
func newBridgeCoordinator(h *Runtime) *BridgeCoordinator {
	return &BridgeCoordinator{
		bridge:         h.bridge,
		chatStore:      h.chatStore,
		turns:          newTurnRegistry(),
		broadcast:      h.bus.Broadcast,
		translateEvent: h.translateACPEvent,
		push:           h.push,
		mcpRegistry:    h.mcpRegistry,
		lifecycle:      h.lifecycle,
		// preBridgeSpawn is deliberately NOT copied from anywhere: the
		// composition root installs it through SetPreBridgeSpawn after New
		// returns, directly onto this field — its one reader.
		// h implements replayProjector via load_projection.go.
		replayProjection: h.replay,
		chatStatus:       h.bus.chatStatus.Get,
		agentEngine:      resolveAgentEngine(),
		acpArgs:          h.acpArgs,
		secretStorage:    func() bool { return h.secrets != nil },
		onSessionRehydrated: func(chatID vibekit.ChatID) {
			ctx, cancel := h.lifecycle.derivedContext()
			defer cancel()
			h.runs.resumeInterruptedRuns(ctx, chatID)
		},
		onTurnClosed: func(chatID vibekit.ChatID, epoch vibekit.TurnEpoch) {
			h.agentTerms.CloseTurn(chatID, epoch)
		},
	}
}

// hasSecretStorage reports whether this process holds a credential store, and
// therefore whether a bridge may declare `_meta.kiro.secretStorage`. Nil-safe:
// a coordinator built without the resolver (every test that does not exercise
// the store) declares the capability off, which is the honest answer for a runtime
// that has no store either.
func (bc *BridgeCoordinator) hasSecretStorage() bool {
	return bc.secretStorage != nil && bc.secretStorage()
}

// processLifetimeCtx returns the context that bounds a spawned kiro-cli
// subprocess: the runtime's shutdown context, so a bridge outlives the turn
// that happened to create it and still dies with the runtime. Must never be
// the caller's ctx — CmdPrompt's is per-turn and cancels on return.
func (bc *BridgeCoordinator) processLifetimeCtx() context.Context {
	return bc.lifecycle.shutdownCtx
}

// resolveAgentEngine returns the kiro-cli agent engine, hard-pinned to v3
// (KAS). vibekit is v3-only: the v2 wire and its handlers were removed, so a
// stray KIRO_AGENT_ENGINE=v1/v2 is deliberately ignored — honoring it would
// launch a legacy engine vibekit can no longer talk to (session/new stalls,
// every turn fails).
func resolveAgentEngine() string {
	return vibekit.AgentEngineV3
}

// OpenBridge returns an existing bridge for chatID, or creates one.
// Concurrent callers for the same chatID coalesce via singleflight, keyed by
// bridgeSpawnKey.
//
//nolint:revive // unexported-return: sharedBridge is package-internal; callers within agent use the methods on it. Exporting would leak ACP wiring outside the runtime package.
func (bc *BridgeCoordinator) OpenBridge(ctx context.Context, chatID vibekit.ChatID, modelOverride string) (*sharedBridge, error) {
	// Fast path: bridge already exists.
	if sb := bc.bridge.mgr.get(chatID); sb != nil {
		bc.repairEffort(ctx, chatID, sb)
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
	// The bridge-ready wake: a prompt parked on the admission slot answers on
	// its holder's source, and the bridge becoming live is the transition that
	// changes the answer without moving any registry state.
	bc.turns.wakeChat(chatID)
	return b, nil
}

// bridgeSpawnKey composes the bridge-spawn singleflight key over
// (chatID, modelOverride). Both fields are separator-free (ids.ValidChatID
// and ids.ValidIdent), so the keyenc-joined key is unambiguous; a collision
// would hand a coalesced caller another chat's bridge, which is why the
// encoding stays injective even if either alphabet widens later. Not
// persisted — the singleflight group lives in memory for one spawn.
func bridgeSpawnKey(chatID vibekit.ChatID, modelOverride string) string {
	return keyenc.Join(string(chatID), modelOverride)
}

// spawnBridge creates (or returns the just-created) bridge for chatID.
// It runs inside the singleflight so concurrent callers coalesce. It
// resolves the model (the override beats the chat's stored value),
// tries session/load when an ACP session id is stored, and otherwise
// starts a fresh session/new. On any start failure it rolls the
// half-registered bridge back out of the map and returns the error.
func (bc *BridgeCoordinator) spawnBridge(ctx context.Context, chatID vibekit.ChatID, modelOverride string) (*sharedBridge, error) {
	// Double-check after winning the singleflight race.
	sb, existed := bc.bridge.mgr.orInsert(chatID)
	if existed {
		return sb, nil
	}

	setupErr := func(err error) error {
		bc.bridge.mgr.removeIfSame(chatID, sb)
		sb.setState(bridgeIdle)
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
	// This is the inherited-value case: `model` comes from the persisted chat
	// field or a caller's override, not from a pick the user just made, and
	// kiro-cli only rejects a bad model mid-prompt on every later turn. So an
	// inherited value that isn't served is silently withheld here; an explicit
	// pick is refused loudly instead, in cmdSwitchModel. The evidence is the
	// LAST session's advertised set (empty means unknowable and allows).
	if !vibekit.ModelServed(model, chat.ServedModelIDs) {
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

	// A spawn carrying a model OVERRIDE that differs from the record is the
	// switch-by-restart path: the chat's stored tier was chosen under the model
	// being switched away from, so the session resolves against the TARGET
	// (seed when picked under it, else its default) exactly as the fast path
	// does. Every other spawn keeps the chat's own resolution.
	effort := bc.effortFor(ctx, chat)
	if model != "" && model != chat.Model {
		effort = bc.EffortForSwitch(ctx, chat, model)
	}

	if chat.ACPSessionID != "" {
		if bc.tryLoadSession(ctx, chatID, sb, chat.ACPSessionID, model, effort) {
			return sb, nil
		}
	}

	// EnableHooks:true opts chat bridges into KAS's v2 hook engine so the
	// workspace's user-authored .kiro/hooks/*.json hooks AUTOFIRE on their
	// triggers during a turn. KAS loads and runs them internally in v2 mode
	// (verified: zero executeHook callbacks on autofire), so no chat-bridge
	// executeHook handler is needed. Same trust model as vibekit's `!cmd`
	// interception. The utility bridge keeps its own EnableHooks for the
	// hooks dashboard; see agent/hooks.go.
	//
	// Forward MUST drain NotifCh before Start: on v3 the agent sends
	// _kiro/auth/getAccessToken and _kiro/terminal/shell_type as server->client
	// REQUESTS on the session-creation critical path, and session/new does not
	// return until they are answered. Attaching Forward after Start deadlocks
	// every fresh session.
	go bc.Forward(chatID, sb.bridge)
	// Supervised is passed at creation only: session/load below does not
	// repeat it, because KAS persists `autopilot` in its own session metadata.
	if err := sb.bridge.Start(ctx, &vibekit.StartOpts{Lifetime: bc.processLifetimeCtx(), Model: model, Mode: chat.CurrentModeID, Effort: effort, AgentEngine: bc.agentEngine, EnableHooks: true, ExtraArgs: bc.acpArgs, Supervised: chat.SupervisedMode, SecretStorage: bc.hasSecretStorage(), Presets: securityPresets(ctx, bc.lifecycle.configDir), ToolSearch: toolSearchEnabled(ctx, bc.lifecycle.configDir), Knowledge: knowledgeEnabled(ctx, bc.lifecycle.configDir), Memory: memoryEnabled(ctx, bc.lifecycle.configDir)}); err != nil {
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
	sb.setState(bridgeIdle)

	return sb, nil
}

// tryLoadSession attempts session/load against the stored ACP session id.
func (bc *BridgeCoordinator) tryLoadSession(
	ctx context.Context, chatID vibekit.ChatID, sb *sharedBridge, acpSessionID, model, effort string,
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
	if err := sb.bridge.Start(ctx, &vibekit.StartOpts{Lifetime: bc.processLifetimeCtx(), SessionID: acpSessionID, Model: model, Effort: effort, AgentEngine: bc.agentEngine, EnableHooks: true, ExtraArgs: bc.acpArgs, SecretStorage: bc.hasSecretStorage(), Presets: securityPresets(ctx, bc.lifecycle.configDir), ToolSearch: toolSearchEnabled(ctx, bc.lifecycle.configDir), Knowledge: knowledgeEnabled(ctx, bc.lifecycle.configDir), Memory: memoryEnabled(ctx, bc.lifecycle.configDir)}); err != nil {
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
		if mErr := bc.chatStore.Mutate(ctx, chatID, func(c *vibekit.Chat, ex bool) bool {
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
	if mErr := bc.chatStore.Mutate(ctx, chatID, func(c *vibekit.Chat, ex bool) bool {
		if !ex {
			return false
		}
		applyLoadedSessionFacts(c, sb.bridge, title)
		return true
	}); mErr != nil {
		slog.Error("refresh session metadata", "chat_id", chatID, "error", mErr)
	}
	sb.primed = true
	sb.setState(bridgeIdle)
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
func adoptKASTitle(c *vibekit.Chat, title string) {
	if title == "" || title == kasDefaultSessionTitle || c.Name != vibekit.DefaultChatName {
		return
	}
	c.Name = title
}

// applyLoadedSessionFacts copies what a RESUMED session reported onto the chat
// record, writing each field only when the load result actually carried it.
//
// The guard belongs here rather than one layer down: applySessionResultLocked
// already keeps-on-absent, but that keep is worthless on a resume because the
// bridge is FRESHLY constructed, so every accessor answers the zero value for
// whatever the result omitted. `session/load` omits the model catalog
// routinely (KAS resolves ListAvailableModels asynchronously) — models
// self-heal from a later config_option_update, but modes have no repair
// channel, so an emptied mode list stays empty for the rest of the session.
//
// session/new's own persistNewSessionMetadata keeps unconditional writes on
// purpose: there the overwrite IS the check (reportModeNotApplied compares
// the mode asked for against the mode that landed).
func applyLoadedSessionFacts(c *vibekit.Chat, facts acpSessionFacts, title string) {
	if mode := facts.CurrentMode(); mode != "" {
		c.CurrentModeID = mode
	}
	if modes := facts.Modes(); len(modes) > 0 {
		c.AvailableModes = modes
	}
	if models := facts.Models(); len(models) > 0 {
		c.AvailableModels = models
	}
	adoptKASTitle(c, title)
}

func (bc *BridgeCoordinator) persistNewSessionMetadata(ctx context.Context, chatID vibekit.ChatID, bridge acpSessionFacts) {
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
	if err := bc.chatStore.Mutate(ctx, chatID, func(c *vibekit.Chat, ex bool) bool {
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

// reportModeNotApplied tells the user when the session did not get the mode
// the chat asked for.
//
// Storing the ACTUAL mode elsewhere is right (the mode pill must not claim a
// role the agent is not running under), but that was also the ONLY record of
// the request: a single transient `session/set_mode` failure silently and
// permanently converted a chat pinned to `spec` into a default-mode chat,
// because the next spawn's requested id then EQUALS the current one and
// applyInitialMode's own guard means no retry is ever attempted.
//
// A visible banner rather than an automatic retry: the truthful pill plus a
// named mismatch puts the choice back in front of a present user, and a
// retry would need a second persisted field to remember an intent the
// record no longer holds. Not a privilege gate — tool authorization is
// Cedar's.
func (bc *BridgeCoordinator) reportModeNotApplied(ctx context.Context, chatID vibekit.ChatID, requested, actual string) {
	if requested == "" || requested == actual {
		return
	}
	slog.Error("session mode not applied; the chat's mode was reset to the session's",
		"chat_id", chatID, "requested", requested, "actual", actual)
	bc.broadcast(ctx, vibekit.NewEvent(vibekit.EventError, chatID, vibekit.ErrorPayload{
		Code: vibekit.ErrCodeModeNotApplied,
		Message: "Could not start this chat in \"" + requested + "\" mode; it is running as \"" +
			actual + "\". Pick the mode again to retry.",
	}))
}

// Bridge returns the bridge for chatID, or nil.
//
//nolint:revive // unexported-return: see OpenBridge above.
func (bc *BridgeCoordinator) Bridge(chatID vibekit.ChatID) *sharedBridge {
	return bc.bridge.mgr.get(chatID)
}

// HasLiveBridge reports whether a chat currently has a bridge, i.e. whether it
// is in active use. Retention's exemption reads this: a chat with a live bridge
// is open work and is never purged, however old (see archive.WithLiveChats).
func (rt *Runtime) HasLiveBridge(chatID vibekit.ChatID) bool {
	return rt.bridge.mgr.get(chatID) != nil
}

// CloseBridge stops a bridge and removes it from the map.
func (bc *BridgeCoordinator) CloseBridge(chatID vibekit.ChatID) {
	bc.bridge.mgr.close(chatID)
}

// replayProjector is the slice of the Runtime's replay-projection lifecycle the
// coordinator drives. See agent/load_projection.go for the settle barrier.
type replayProjector interface {
	OpenReplayProjection(vibekit.ChatID)
	MarkReplayLoadDone(vibekit.ChatID)
	DiscardReplayProjection(vibekit.ChatID)
	SettleReplayProjection(chatID vibekit.ChatID, buffered int, force bool)
}

// Forward is the ACP notification → domain event translator, run as a
// goroutine per bridge.
func (bc *BridgeCoordinator) Forward(chatID vibekit.ChatID, bridge ACPBridge) {
	ch := bridge.NotifCh()
	// This goroutine IS the folder, so the position it reports is the only one a
	// local settle can order itself against. The generation is what keeps a
	// straggler from the previous bridge — still draining a closed channel while
	// this one attaches — from advancing a counter that restarted at zero.
	gen := bc.turns.attachForward(chatID)
	for n := range ch {
		bc.consumeFrame(chatID, gen, n)
		// Settle a session/load replay projection here rather than at Start's
		// return: this goroutine is the one draining the frames, so its own
		// view of the channel depth is the only sound completion signal.
		// len() on a receive-only channel is the whole barrier — no timeout,
		// no extra bridge API. Rationale in agent/load_projection.go.
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
	// No frame can advance the position now, so anything parked on one has to be
	// told rather than left to its context. Before the death closer, so a woken
	// settle has already deferred by the time that closer runs.
	bc.turns.sealPosition(chatID, gen)

	slog.Info("bridge exited", "chat_id", chatID)

	// Still registered means nobody removed it, so the process died on its own
	// rather than being torn down: the third actor closes whatever turn is still
	// open, because no other closer is coming for it.
	if bc.bridge.mgr.removeIfBridge(chatID, bridge) {
		bc.closeTurnOnBridgeDeath(bc.lifecycle.shutdownCtx, chatID)
	}

	// Flush staged writes for the chat. A bridge exit (crash, or a
	// model-switch CloseBridge) leaves the supervised fs-handler goroutine
	// parked on its resume channel and a phantom "awaiting approval"
	// pending op that would replay to reconnecting clients. Cancel, delete,
	// and mode-disable already flush; this is the bridge-exit sibling.
	lastBridge := bc.bridge.mgr.count() == 0

	if lastBridge {
		bc.mcpRegistry.clearAll(bc.lifecycle.shutdownCtx)
	}
	// A run chat has no record and no turn lifecycle of its own beyond the
	// position bookkeeping above, and nothing ever calls cleanupChatState for one,
	// so its lifecycle is dropped here or it outlives the run.
	if isRunChat(chatID) {
		bc.turns.forget(chatID)
	}
}

// consumeFrame translates one frame and then advances the chat's observed
// position, whatever the frame did.
//
// DEFERRED, and for every frame rather than for every fold: the advance
// acknowledges work that is done, and at least eight paths through the
// session-update cascade consume a frame without touching a turn. See observe.
func (bc *BridgeCoordinator) consumeFrame(chatID vibekit.ChatID, gen uint64, n vibekit.Notification) {
	defer bc.turns.observe(chatID, gen, n.Seq)
	bc.translateEvent(chatID, n.Msg)
}

// PrimeIfNeeded sends the chat history as an ephemeral priming prompt on the
// current bridge, unless this session already has it.
//
// The primed flag is claimed HERE rather than by the caller. It used to be the
// prompt handler's three-step check-then-set-then-prime over a Bridge interface,
// which put the decision in the command package and forced the runtime to assert the
// interface back down to *sharedBridge on the way in. Claiming it here means one
// owner for the flag and no assertion: a chat with no bridge is simply nothing
// to prime.
func (bc *BridgeCoordinator) PrimeIfNeeded(ctx context.Context, chatID vibekit.ChatID) {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return
	}
	if !sb.claimPriming() {
		return
	}
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
	// The prime is a real session/prompt, so it opens a real turn and closes it
	// like any other. Then it AWAITS its own epoch before returning, and that is
	// what keeps the unacknowledged set from ever holding two: the caller's own
	// pre-open cannot happen until this turn has finalized, so a wire turn_start
	// can only ever bind to one candidate.
	epoch := bc.StartTurn(ctx, chatID, vibekit.TurnSourcePrime)
	defer bc.ReleaseTurn(chatID, epoch)
	resp, seq, err := sb.bridge.CallAt(ctx, vibekit.MethodPrompt, command.SessionParams(sb, map[string]any{
		"prompt": []map[string]any{vibekit.TextBlock(prime)},
	}))
	if err != nil {
		slog.Error("prime failed", "chat_id", chatID, "error", err)
		bc.AbandonInFlightTurn(ctx, chatID, epoch, "The priming prompt failed.")
		return
	}
	bc.SettleTurnOnResponse(ctx, chatID, epoch, seq, resp)
	if _, aErr := bc.AwaitTurn(ctx, chatID, epoch); aErr != nil {
		slog.Warn("prime: no turn outcome", "chat_id", chatID, "error", aErr)
	}
}

// There is no effortForModel and no modelEffortSetting. Effort was one GLOBAL
// `model_effort` setting shaped `{last_model, effort}`, so it was keyed by the
// LAST model rather than by the chat: two chats could not disagree, and
// switching models discarded the previous model's level outright. It is a field
// on the chat record now (vibekit.Chat.Effort), read straight off the chat at spawn
// and written by CmdSetEffort, so the launch flag needs no settings read.

// NotifyPush sends a push notification about one CHAT if the push service is
// configured.
//
// It keeps its chat-id parameter rather than taking an vibekit.PushSubject: every
// caller here is chat-scoped (a turn ended, a permission ask, an agent question),
// so the conversion belongs at this one boundary instead of at each of them. A
// notification with no chat behind it — the PR poller's — does not come through
// here at all; it calls push.Send with its own subject.
func (bc *BridgeCoordinator) NotifyPush(ctx context.Context, body string, kind vibekit.PushKind, chatID vibekit.ChatID) {
	if bc.push == nil || !bc.push.HasSubscribers() {
		return
	}
	bc.lifecycle.inflight.Go(func() {
		bc.push.Send(ctx, push.DefaultTitle, body, kind, vibekit.ChatSubject(chatID))
	})
}

// SettleTurnOnResponse closes the turn named by epoch on the response that settled
// it — once the folder has consumed everything queued behind that response, and
// only if the wire's own turn_end did not get there first.
//
// seq is the read loop position the response arrived at. Zero skips the wait,
// which is what the two paths that deliberately reach no bracket want: an oversize
// frame and a cancel-grace expiry fail the call while the bridge stays alive.
func (bc *BridgeCoordinator) SettleTurnOnResponse(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch, seq uint64, resp *vibekit.RPCResponse) {
	bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerPromptResponse, Resp: resp, Epoch: epoch, Seq: seq})
}

// TurnOpenedAfter reports whether any turn on the chat opened after epoch — the
// structural half of the empty-turn gate. See turnRegistry.openedAfter.
func (bc *BridgeCoordinator) TurnOpenedAfter(chatID vibekit.ChatID, epoch vibekit.TurnEpoch) bool {
	return bc.turns.openedAfter(chatID, epoch)
}

// statusDescription reads the chat's self-declared status description (KAS's
// focus_update channel), for the push notification's body.
//
// Must be read BEFORE the turn_ended broadcast, not at the push site: the
// status cache is deliberately cleared at turn end (so a bare replay cannot
// resurrect a stale "in_progress"), inside the same emit() that fires
// turn_ended — which sits between the top of EmitTurnEndedWithStats and this
// push. Reading the cache after that broadcast would always find it gone.
//
// Empty is legitimate (an agent need never call update_session_information),
// so the caller's fallback literal is the honest default, not a defensive
// branch.
func (bc *BridgeCoordinator) statusDescription(chatID vibekit.ChatID) string {
	if bc.chatStatus == nil {
		return ""
	}
	return bc.chatStatus(chatID).Description
}

// defaultAgentFinishedBody is the body for a turn whose agent never declared what
// it was doing.
const defaultAgentFinishedBody = "Agent finished"

// agentFinishedBodyFrom picks the body from a chat's self-declared description.
// Split out from the cache read so the choice is testable without a Runtime.
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
func (bc *BridgeCoordinator) persistTurn(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) {
	if err := bc.chatStore.AppendMessage(ctx, chatID, msg); err != nil {
		slog.Error("persist assistant turn; the replay projection is the fallback",
			"chat_id", chatID, "error", err)
	}
}

// TryFastModelSwitch attempts an in-session model swap via
// session/set_config_option (configId "model") on the running bridge, then
// re-applies effort so the swap does not carry the level away with it.
func (bc *BridgeCoordinator) TryFastModelSwitch(ctx context.Context, chatID vibekit.ChatID, model, effort string) bool {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return false
	}
	return bc.applyModelSwitch(ctx, chatID, sb, model, effort)
}

// applyModelSwitch swaps the model on a bridge the caller ALREADY HOLDS, then
// re-applies the level.
//
// Takes the bridge rather than the chat id because the two callers reach it
// differently and only one of them can look it up safely: the fast path resolves
// it from the manager, while the restart fallback has just been handed a
// freshly-loaded bridge by OpenBridge. Re-resolving by id there could answer with a
// DIFFERENT bridge — another goroutine closing the chat, or a spawn that raced the
// old bridge's exit cleanup — so the pick would land on a session that is not the
// one the caller loaded, or on nothing at all.
func (bc *BridgeCoordinator) applyModelSwitch(
	ctx context.Context, chatID vibekit.ChatID, sb *sharedBridge, model, effort string,
) bool {
	if err := sb.bridge.SetModel(ctx, model); err != nil {
		slog.Info("model switch: fast path failed, falling back to restart",
			"chat_id", chatID, "model", model, "error", err)
		return false
	}
	// Re-assert the level after the swap, or the swap can take it away. KAS
	// reconciles the session's effortLevel against the NEW model's tier list and
	// replaces it with that model's default whenever the current level is absent
	// from the list — measured on 2.19.1: a swap between two models offering the
	// same five tiers KEEPS the level, and a swap to `auto`, which offers none,
	// destroys it. SetModel clears the bridge's cached level for exactly this
	// reason, so EnsureEffort asserts here rather than matching a stale value.
	//
	// Best-effort: the swap already landed and is the operation the user asked for,
	// so a failure here logs and leaves the level at the service's own choice rather
	// than reporting the switch as failed and driving the caller into a restart.
	if effort != "" {
		if err := sb.bridge.EnsureEffort(ctx, effort); err != nil {
			slog.Warn("model switch: reasoning effort not re-applied after the swap",
				"chat_id", chatID, "model", model, "effort", effort, "error", err)
		}
	}
	slog.Info("model switch: fast path succeeded (session/set_config_option)",
		"chat_id", chatID, "model", model)
	return true
}

// repairEffort re-asserts the chat's reasoning-effort level on a bridge that
// is ALREADY OPEN — the one checkpoint that catches a level KAS changed on
// its own (its own pinSessionModelId settling an unset model on first
// prompt, or a model switch made from the Kiro IDE/TUI on a shared session).
//
// At the prompt rather than reactively on the notification: a reactive Call
// from the Forward goroutine would block the drain it is waiting on, needs
// its own loop guard, and covers only enumerated triggers. OpenBridge is on
// every prompt's path, and Bridge.EnsureEffort compares before it calls, so
// the normal case is one comparison and no round trip.
//
// Best-effort and log-only: a chat must not fail to answer because a
// preference could not be re-applied.
func (bc *BridgeCoordinator) repairEffort(ctx context.Context, chatID vibekit.ChatID, sb *sharedBridge) {
	chat, ok := bc.chatStore.Get(ctx, chatID)
	if !ok {
		return
	}
	level := bc.effortFor(ctx, chat)
	if level == "" {
		return
	}
	if err := sb.bridge.EnsureEffort(ctx, level); err != nil {
		slog.Warn("reasoning effort not re-applied on the open session",
			"chat_id", chatID, "effort", level, "error", err)
	}
}

// healEffort re-asserts the chat's chosen reasoning-effort level when a
// config_option_update reports the session running at a different one. It wraps
// the config-option handler in the dispatch table (see initDispatch).
//
// It exists because repairEffort has a hole its own doc comment cannot cover: it
// runs on OpenBridge's ALREADY-OPEN path, and the turn that SPAWNS the bridge
// never takes that path. So the one moment the repair is named for — KAS's
// first-prompt pinSessionModelId settling the model and applying that model's
// default tier — is the one moment nothing repairs, and a chat whose whole life is
// a single turn keeps the wrong level. Measured on the live volume: 2 of 13
// opus-5 chats sat at effort_active=high against a chosen max, and both had
// exactly one turn.
//
// Reactive rather than at the next prompt, because the divergence appears DURING
// the turn and the next prompt may never come. Runs AFTER next, so the client
// renders what the session reported before anything changes it and so the chat
// record already carries this frame's level.
//
// LATCHED once per bridge: the repair itself produces another
// config_option_update, so an unbounded reactive repair is a loop. Past the latch
// the prompt-time repairEffort owns it, which is the right place for a level
// something keeps moving back.
//
// Best-effort, log-only, and on its own goroutine — this runs on the forward
// goroutine, so a bridge Call issued inline would block the drain the reply has to
// arrive on.
func (bc *BridgeCoordinator) healEffort(next sessionUpdateHandler) sessionUpdateHandler {
	return func(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage, attr translate.FrameAttribution) {
		next(ctx, chatID, raw, attr)
		// The chat's OWN frame only. A workflow step's session reports the level
		// IT runs at, and a subagent's frame would be attributed too; neither says
		// anything about the level this chat chose. Both fields are tested,
		// because an empty SubSessionID alone does not mean the chat owns the
		// frame — a step has one too.
		if attr.Step || attr.SubSessionID != "" {
			return
		}
		sb := bc.Bridge(chatID)
		if sb == nil {
			return
		}
		chat, ok := bc.chatStore.Get(ctx, chatID)
		if !ok {
			return
		}
		// The frame IS the session reporting its level, and the bridge forwards
		// this channel unread, so hand the report over before deciding anything:
		// it is what lets EnsureEffort assert here AND at the next prompt, rather
		// than comparing equal against the level the session door asked for.
		running := chat.EffortActive
		sb.bridge.ObserveEffort(running)
		want := bc.effortFor(ctx, chat)
		if want == "" || running == "" || want == running {
			return
		}
		if !sb.claimEffortHeal() {
			slog.Debug("reasoning effort still diverges after one repair; leaving it to the next prompt",
				"chat_id", chatID, "want", want, "running", running)
			return
		}
		slog.Info("re-asserting the chat's reasoning effort: the session reported a different level",
			"chat_id", chatID, "want", want, "running", running)
		// On inflight rather than untracked: Shutdown stops every bridge BEFORE it
		// waits on this group, which is the ordering a blocked bridge Call needs to
		// unblock through, and it is what the fs handlers beside it already do.
		bc.lifecycle.inflight.Go(func() {
			hctx, cancel := bc.lifecycle.derivedContext()
			defer cancel()
			if err := sb.bridge.EnsureEffort(hctx, want); err != nil {
				slog.Warn("reasoning effort not re-asserted after the session reported another level",
					"chat_id", chatID, "want", want, "running", running, "error", err)
			}
		})
	}
}

// effortFor resolves the reasoning-effort level a chat's next session starts
// at: the chat's own choice, else the last level the user picked anywhere
// (settings.KeyLastEffort). Empty means send nothing and let the service
// apply the model's own default.
//
// It is a FALLBACK never written onto the chat record — stamping it would
// pin an unchosen chat to today's value for every later session, the same
// mistake as seeding from a service default. It is also MODEL-SCOPED
// (settings.KeyLastEffortModel): an explicit tier is a judgement about one
// model, and applying it under a different model would override that
// model's own default with another model's choice.
//
// Validated here because config.json is user-editable and an unknown level
// must not reach the wire; a level the current MODEL does not offer is
// KAS's to reconcile via config_option_update.
func (bc *BridgeCoordinator) effortFor(ctx context.Context, chat *vibekit.Chat) string {
	if chat.Effort != "" {
		return chat.Effort
	}
	return bc.effortSeedFor(ctx, chat.Model)
}

// effortSeedFor answers the remembered level for exactly one model: the
// KeyLastEffort/KeyLastEffortModel pair when the recorded model IS `model`,
// else "". The one seed read, shared by the session-start resolution above and
// the model-switch target below so the two cannot disagree about scope.
func (bc *BridgeCoordinator) effortSeedFor(ctx context.Context, model string) string {
	if model == "" {
		return ""
	}
	var level, seedModel string
	if !settings.FieldInto(ctx, bc.lifecycle.configDir, settings.KeyLastEffort, &level) {
		return ""
	}
	if !settings.FieldInto(ctx, bc.lifecycle.configDir, settings.KeyLastEffortModel, &seedModel) {
		return ""
	}
	if seedModel != model {
		return ""
	}
	if !vibekit.EffortLevel(level).Valid() {
		return ""
	}
	return level
}

// EffortForSwitch resolves the level a chat runs at AFTER a model switch: the
// seed when it was picked under the TARGET model, else the target model's own
// default from the chat's catalog, else "" (KAS reconciles on its own).
//
// Deliberately NOT effortFor: the chat's stored choice was made under the model
// being switched away from, so honouring it here is what carried `max` from one
// model onto the next (user report, 2026-08-31). The switch clears that choice
// (PersistModelSwitch) and this names its replacement explicitly — explicit,
// because KAS KEEPS a fitting level across a swap, so without the assert a
// cleared choice would silently keep running.
func (bc *BridgeCoordinator) EffortForSwitch(ctx context.Context, chat *vibekit.Chat, model string) string {
	if level := bc.effortSeedFor(ctx, model); level != "" {
		return level
	}
	for _, m := range chat.AvailableModels {
		if m.ID == model {
			return m.DefaultEffortLevel
		}
	}
	return ""
}

// PersistModelSwitch records the switch event and updates the chat's
// model + resets usage counters.
func (bc *BridgeCoordinator) PersistModelSwitch(ctx context.Context, chatID vibekit.ChatID, model string, contextSize int) {
	evt := vibekit.Message{
		ID:        newMessageID(),
		Role:      vibekit.RoleEvent,
		Ts:        time.Now().UnixMilli(),
		EventKind: vibekit.EventModelSwitched,
		Content:   model,
	}
	if err := bc.chatStore.AppendMessage(ctx, chatID, &evt); err != nil {
		slog.Error("switch_model: append event", "chat_id", chatID, "error", err)
	}
	if err := bc.chatStore.Mutate(ctx, chatID, func(c *vibekit.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.Model = model
		// The chat's chosen tier was a judgement about the model being switched
		// AWAY from, so it does not survive the switch: resolution falls to the
		// model-scoped seed, else the new model's own default (EffortForSwitch
		// asserts that on the session, because KAS keeps a fitting level on its
		// own). Switching back re-applies the seed when the pick was made under
		// that model.
		c.Effort = ""
		c.Usage = vibekit.Usage{ContextSize: contextSize}
		return true
	}); err != nil {
		slog.Error("switch_model: persist model", "chat_id", chatID, "error", err)
	}
}

// FlushInFlightTurnOnSwitch discards the chat's turn before a bridge restart,
// announcing the interruption when a turn was in flight.
func (bc *BridgeCoordinator) FlushInFlightTurnOnSwitch(ctx context.Context, chatID vibekit.ChatID) {
	bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerModelSwitch, AnyOpen: true})
}

// assistantTurnMessage builds the persisted assistant message from a finished
// turn's content. Extracted so the interrupted path and the normal path cannot
// drift: every field below exists because something in the client reads it after a
// reload, so a second hand-written literal would quietly lose one of them.
//
// It takes the SNAPSHOT rather than the buffer, so every field comes from ONE
// guarded read: the eight field reads this used to make ran off the dispatch
// goroutine, against a fold that was already past TurnFoldTarget when the closer
// claimed.
func assistantTurnMessage(snap *buffer.TurnContent, stats turnStats, model string, c vibekit.TurnConclusion) vibekit.Message {
	return vibekit.Message{
		ID:        snap.MessageID,
		Role:      vibekit.RoleAssistant,
		Ts:        time.Now().UnixMilli(),
		Content:   snap.Content,
		Reasoning: snap.Reasoning,
		ToolCalls: snap.ToolCalls,
		// Blocks captures the chronological text/tool/thinking
		// emission order; client renderers prefer it over
		// Content+ToolCalls so a turn renders the way the agent
		// actually produced it (text, tool, more text, another
		// tool, …) rather than collapsing all text to the top.
		Blocks: snap.Blocks,
		// CodeReferences persists the turn's licensed-code
		// attributions so the chip survives reload (the streamed
		// assistant turn is never re-broadcast as message_appended).
		CodeReferences: snap.CodeReferences,
		// Refusal metadata (kiro-cli 2.13): stamped from the refusal
		// explanation chunk; persisting it here is what makes the
		// refusal callout survive reload.
		Refusal: snap.Refusal,
		// Turn summary (credits · elapsed · files changed) — persisted on
		// the message so the turn footer survives reload. The same values
		// ride the turn_ended SSE for the live render; omitempty drops the
		// zero/nil cases (a read-only or zero-cost turn carries no footer).
		TurnCredits:   stats.CreditsDelta,
		TurnElapsedMs: stats.ElapsedMs,
		ChangedFiles:  snap.ChangedFiles,
		// Which model answered, latched when the turn opened rather than read
		// from the chat now: the chat's Model is the CURRENT one, and a footer
		// derived from it would relabel history on every switch.
		TurnModel: model,
		// How the turn ENDED, durable at last. A live stop reason is broadcast and
		// never stored, so before these three a reloaded transcript derived the
		// outcome from whichever event messages happened to survive — which reads a
		// failed turn as completed and suppresses its footer.
		TurnOutcome:       c.Outcome,
		TurnStopReasonRaw: c.RawStop,
		TurnTruncated:     c.Truncated,
	}
}

// AbandonInFlightTurn finalizes a turn whose prompt call could not finish it,
// PERSISTING the partial rather than dropping it.
//
// Without a call that takes the assistant buffer, that buffer survived with
// Started == true and the next prompt extended the dead turn's blocks under the
// dead turn's message id — one message holding two turns' replies.
//
// It waits for no read loop position: the two failures that reach it settle
// locally with the bridge still alive, so no bracket is coming.
func (bc *BridgeCoordinator) AbandonInFlightTurn(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch, reason string) {
	bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerPromptFailure, Reason: reason, Epoch: epoch})
}

// FinalizeLocalShellTurn closes a `!cmd` turn vibekit ran itself.
func (bc *BridgeCoordinator) FinalizeLocalShellTurn(ctx context.Context, chatID vibekit.ChatID, epoch vibekit.TurnEpoch) {
	bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerLocalShell, Epoch: epoch})
}

// WireTurnStart is the engine's own turn_start bracket.
//
// It binds the single pending pre-open when there is one, PROVISIONALLY — the
// bracket cannot tell a prompted turn from an agent-initiated one. Otherwise the
// previous turn's end never arrived, so that turn closes `unknown` and a
// wireTurnStart turn opens in its place. An acknowledged start still passes
// through that branch rather than bypassing it: a wireTurnStart turn holds no
// prompt slot for admission control to have refused.
func (bc *BridgeCoordinator) WireTurnStart(ctx context.Context, chatID vibekit.ChatID) {
	bound, displaced := bc.turns.bindPending(chatID)
	if !bound && displaced != 0 {
		// A pre-open is owed this bracket while another turn is still folding, so
		// that turn's own end never arrived. Close it and bind on the retry rather
		// than binding over it: the pre-open's frames must not fold into the other
		// turn's buffer.
		bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerWireDisplaced, Epoch: displaced})
		bound, _ = bc.turns.bindPending(chatID)
	}
	if bound {
		return
	}
	bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerWireDisplaced, AnyOpen: true})
	model, credits := bc.turnOpenFacts(ctx, chatID, vibekit.TurnSourceWireTurnStart)
	bc.turns.openWire(ctx, chatID, model, credits)
}

// WireTurnEnd is the engine's own turn_end bracket, and the closer whose outcome
// is the wire's rather than an inference.
//
// A turn_end for a chat with NO open turn is a no-op. Without that rule a
// cancel-grace expiry that closed its turn locally would meet the later wire
// bracket, and the fold-with-no-open-turn rule would manufacture a spurious
// empty persisted turn out of it. A replayed bracket is filtered upstream, so
// this is the live path only.
func (bc *BridgeCoordinator) WireTurnEnd(ctx context.Context, chatID vibekit.ChatID, stop vibekit.StopReason) {
	bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerWireEnd, Stop: stop, AnyOpen: true})
}

// TurnFoldTarget returns the buffer this chat's frames fold into, opening a
// wireTurnStart turn when none is open. It is what replaced a per-chat buffer
// created lazily on the first frame: a fold with no open turn is a turn vibekit
// did not prompt, and it needs a record for the same reasons every other turn
// does.
//
// The CHEAP question comes first. openWire discards both facts on every call but
// the one that actually opens a turn, and reading them is a full chat-file read
// plus a json.Unmarshal of the whole history under the per-chat mutex — per
// streamed delta and per tool frame. That cost scales with the TRANSCRIPT rather
// than the frame, it contends with every persist on the same chat, and it runs on
// the only consumer of a 256-slot channel, so a large chat could stall the read
// loop under its own bookkeeping. The benign race is already handled: openWire
// re-checks under the lifecycle mutex, and the facts are deliberately NOT read
// under it — the lock order is lifecycle then chat store, never the reverse.
func (bc *BridgeCoordinator) TurnFoldTarget(ctx context.Context, chatID vibekit.ChatID) *buffer.Buffer {
	if buf, ok := bc.turns.foldTarget(chatID); ok {
		return buf
	}
	model, credits := bc.turnOpenFacts(ctx, chatID, vibekit.TurnSourceWireTurnStart)
	t := bc.turns.openWire(ctx, chatID, model, credits)
	if t == nil {
		// ctx died while the chat was finalizing. A throwaway buffer keeps the
		// handler's shape rather than making every fold site nil-check: the frame is
		// lost either way, and the process is shutting down.
		return buffer.New()
	}
	return t.Buf
}

// ReviseTurnBinding acts on a frame that PROVES the open turn is the agent's own
// rather than the prompt's: `agentInitiated` rides content frames and never the
// bracket, so this is the only discriminator there is. See
// turnRegistry.reclassify.
func (bc *BridgeCoordinator) ReviseTurnBinding(ctx context.Context, chatID vibekit.ChatID) {
	bc.turns.reclassify(ctx, chatID)
}

// closeTurnOnBridgeDeath is the third actor: after Forward has exited it closes
// any turn still open, because nothing else is going to.
//
// It fires only on an UNEXPECTED exit, and the discriminator is whether the
// bridge was still registered when it died. Every teardown vibekit performs
// itself -- CloseBridge for the model-switch fallback and the empty-turn
// recovery, drain at shutdown -- removes the bridge from the map first and has
// its own closer, so a deliberate stop must not also read as a death.
func (bc *BridgeCoordinator) closeTurnOnBridgeDeath(ctx context.Context, chatID vibekit.ChatID) {
	bc.finalizeTurn(ctx, chatID, turnClose{Closer: closerBridgeDeath, AnyOpen: true})
}

const stopReasonCancelled = vibekit.StopReasonCancelled

// InterruptTurn records why kiro-cli abandoned a turn without answering it, and
// trips that turn's prompt call so the ordinary failure path finalizes it.
// Satisfies translate.TurnInterruptAccess.
//
// The cause lands on the TURN, epoch-scoped and first-wins, so it describes
// only the turn it was raised for.
//
// The bridge is left ALIVE and the ACP session untouched: only the tool call
// was cancelled, so tripping the prompt context frees the slot and the chat
// is immediately promptable again — one Send rather than a bridge restart.
//
// A chat with no open turn, no bridge, or a cause already claimed is not a
// failure: the frame can arrive after a user cancel already ended the same
// turn. Logged at Debug so the no-op stays observable without reading like one.
func (bc *BridgeCoordinator) InterruptTurn(chatID vibekit.ChatID, reason string) {
	epoch, open := bc.turns.openEpoch(chatID)
	if !open {
		slog.Debug("interrupt turn: no turn open", "chat_id", chatID, "reason", reason)
		return
	}
	if !bc.turns.interrupt(chatID, epoch, vibekit.InterruptCause(reason)) {
		slog.Debug("interrupt turn: another cause already claimed this turn",
			"chat_id", chatID, "epoch", epoch, "reason", reason)
		return
	}
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		slog.Debug("interrupt turn: no bridge", "chat_id", chatID)
		return
	}
	if !sb.cancelPromptCall() {
		slog.Debug("interrupt turn: no prompt call in flight",
			"chat_id", chatID, "reason", reason)
	}
}

func extractStopReason(resp *vibekit.RPCResponse) vibekit.StopReason {
	if resp == nil || resp.Result == nil {
		return ""
	}
	var result struct {
		StopReason vibekit.StopReason `json:"stopReason"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		slog.Debug("turn_ended: parse result", "error", err)
		return ""
	}
	return result.StopReason
}

// BridgeRespond answers an ACP request on the chat's bridge, and is a no-op when
// that chat has none: a response to a request whose bridge already went away has
// nowhere to go and is not an error.
//
// It lives here rather than in translate_deps.go, where it sat until 2026-08-19,
// because no translate role declares it. Its three callers are all runtime's own
// (agent_terminal.go, translate.go, run_host.go), and this file already owns
// reaching a bridge by chat id.
func (rt *Runtime) BridgeRespond(ctx context.Context, chatID vibekit.ChatID, requestID int64, result any, err error) error {
	sb := rt.bridge.mgr.get(chatID)
	if sb == nil {
		return nil
	}
	return sb.bridge.Respond(ctx, requestID, result, err)
}

// ParentACPSession returns the ACP session id of the running bridge
// for chatID, or "" when no bridge exists. Translator helpers use this
// to short-circuit notifications whose top-level sessionId belongs to
// a subagent rather than the parent chat.
func (bc *BridgeCoordinator) ParentACPSession(chatID vibekit.ChatID) string {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return ""
	}
	return string(sb.bridge.SessionID())
}
